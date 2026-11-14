package server

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/herbiewalker/alcove/internal/adminlog"
	"github.com/herbiewalker/alcove/internal/backup"
)

// The instance backup page (D-28, F23). Backups, verifications and restore drills can take
// minutes on a large store, longer than a request may live, so they run as one background job
// whose result the page shows (refreshing itself while it works).

type job struct {
	kind    string // "run", "verify", "drill"
	name    string
	started time.Time
	done    bool
	ok      bool
	text    string
}

type jobs struct {
	mu  sync.Mutex
	cur *job
}

func (j *jobs) running() bool { j.mu.Lock(); defer j.mu.Unlock(); return j.cur != nil && !j.cur.done }

func (j *jobs) snapshot() *job {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.cur == nil {
		return nil
	}
	c := *j.cur
	return &c
}

// start runs fn in the background if nothing else is running.
func (j *jobs) start(kind, name string, fn func() (string, error), now func() time.Time) bool {
	j.mu.Lock()
	if j.cur != nil && !j.cur.done {
		j.mu.Unlock()
		return false
	}
	cur := &job{kind: kind, name: name, started: now()}
	j.cur = cur
	j.mu.Unlock()
	go func() {
		text, err := fn()
		j.mu.Lock()
		cur.done, cur.ok, cur.text = true, err == nil, text
		j.mu.Unlock()
	}()
	return true
}

// jobLabel is what a job is called on the page.
func jobLabel(kind string) string {
	switch kind {
	case "run":
		return "backup"
	case "verify":
		return "check of the backup"
	case "drill":
		return "restore drill"
	}
	return kind
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

type backupRow struct {
	Name, When, Size string
	Sidecar          bool
}

func (s *Server) backupConfigured() bool { return s.opt.BackupDir != "" }

func (s *Server) lastTimes(q *rq) (lastBackup, lastDrill string) {
	lastBackup, lastDrill = "never", "never"
	read := func(key string) (int64, string) {
		v, ok, err := q.st.GetMeta(key)
		if err != nil || !ok {
			return 0, ""
		}
		var ts int64
		var tag string
		for i := 0; i < len(v); i++ {
			if v[i] == ':' {
				tag = v[i+1:]
				v = v[:i]
				break
			}
		}
		ts, _ = strconv.ParseInt(v, 10, 64)
		return ts, tag
	}
	if ts, _ := read("backup.last_ok"); ts > 0 {
		lastBackup = time.Unix(ts, 0).UTC().Format(timeFmt) + " UTC"
	}
	if ts, tag := read("backup.last_drill"); ts > 0 {
		lastDrill = time.Unix(ts, 0).UTC().Format(timeFmt) + " UTC, " + tag
	}
	return
}

func (s *Server) backupPage(q *rq) *page {
	p := &page{Title: "Backups", Active: "admin", BackupOK: s.backupConfigured()}
	p.LastBackup, p.LastDrill = s.lastTimes(q)
	if j := s.jobs.snapshot(); j != nil {
		switch {
		case !j.done:
			p.JobRunning, p.Refresh = true, true
			p.JobText, p.JobSince = "Working on a "+jobLabel(j.kind)+", started at "+j.started.UTC().Format("15:04:05")+" UTC.", j.started.Unix()
		case j.ok:
			p.Flash = j.text
		default:
			p.Error = j.text
		}
	}
	if p.BackupOK {
		list, err := backup.List(s.opt.BackupDir)
		if err != nil {
			p.Error = "The backup folder cannot be read. Check that it is mounted and writable."
			p.BackupOK = false
		}
		for _, b := range list {
			p.Backups = append(p.Backups, backupRow{Name: b.Name, When: b.Created.UTC().Format(timeFmt), Size: humanBytes(b.Size), Sidecar: b.Sidecar})
		}
	}
	return p
}

func (s *Server) getBackup(q *rq) {
	if err := q.audit("backup.view", "backup", "all", "", nil); err != nil {
		q.serverError("log backup view", err)
		return
	}
	q.render(http.StatusOK, "backup", s.backupPage(q))
}

func (q *rq) backupError(msg string) {
	p := q.s.backupPage(q)
	p.Error = msg
	q.render(http.StatusBadRequest, "backup", p)
}

// finish writes the audit entry for a finished job (the actor is the admin who started it).
func (s *Server) finish(actor, action, targetType, target, fields string, meta map[string]string) {
	st := s.be.Store()
	if st == nil {
		return
	}
	err := st.Update(backgroundCtx, true, func(tx *sql.Tx) error {
		for k, v := range meta {
			if _, err := tx.Exec(`INSERT INTO meta(key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, k, v); err != nil {
				return err
			}
		}
		_, err := adminlog.Append(tx, s.now(), actor, action, targetType, target, fields)
		return err
	})
	if err != nil {
		s.log.Error("could not log a backup action", "action", action, "err", err.Error())
	}
}

func (s *Server) postBackupRun(q *rq) {
	if q.needStepUp("/admin/backup") {
		return
	}
	if !s.backupConfigured() {
		q.backupError("No backup folder is configured (ALCOVE_BACKUP_DIR).")
		return
	}
	actor, st := q.id.user.ID, q.st
	dir, keep := s.opt.BackupDir, s.opt.BackupKeep
	started := s.jobs.start("backup", "", func() (string, error) {
		_, _ = backup.CleanStale(dir, time.Hour)
		now := s.now()
		info, err := backup.Create(st, dir, now)
		if err == nil {
			_, err = backup.Verify(info.Path)
		}
		if err != nil {
			s.log.Error("backup failed", "err", err.Error())
			s.finish(actor, "backup.run", "backup", "none", "failed", nil)
			return "The backup failed: " + err.Error(), err
		}
		gone, perr := backup.Prune(dir, keep)
		if perr != nil {
			s.log.Error("backup prune failed", "err", perr.Error())
		}
		fields := "archive"
		if len(gone) > 0 {
			fields = "archive,pruned"
		}
		s.finish(actor, "backup.run", "backup", info.Name, fields, map[string]string{"backup.last_ok": strconv.FormatInt(now.Unix(), 10)})
		return fmt.Sprintf("Backup %s written and verified: %d files, %s. %d older backup(s) removed.", info.Name, info.Files, humanBytes(info.Size), len(gone)), nil
	}, s.now)
	s.afterStart(q, started)
}

func (s *Server) afterStart(q *rq, started bool) {
	if !started {
		q.backupError("Another backup task is still running. Wait for it to finish.")
		return
	}
	q.redirect("/admin/backup")
}

func (s *Server) postBackupVerify(q *rq) {
	info, ok := s.pick(q)
	if !ok {
		return
	}
	actor := q.id.user.ID
	started := s.jobs.start("verification", info.Name, func() (string, error) {
		got, err := backup.Verify(info.Path)
		if err != nil {
			s.finish(actor, "backup.verify", "backup", info.Name, "failed", nil)
			return "Verification FAILED for " + info.Name + ": " + err.Error(), err
		}
		s.finish(actor, "backup.verify", "backup", info.Name, "ok", nil)
		return fmt.Sprintf("%s verifies: %d files, every checksum matches.", info.Name, got.Files), nil
	}, s.now)
	s.afterStart(q, started)
}

func (s *Server) postBackupDrill(q *rq) {
	info, ok := s.pick(q)
	if !ok {
		return
	}
	actor, st, dir := q.id.user.ID, q.st, s.opt.BackupDir
	tmp := filepath.Join(s.opt.TmpDir, "drill")
	started := s.jobs.start("restore drill", info.Name, func() (string, error) {
		_, _ = backup.CleanStale(dir, time.Hour)
		key, err := st.KeyForDrill()
		if err != nil {
			return "The restore drill could not start.", err
		}
		defer key.Zero()
		rep, err := backup.Drill(info.Path, key, backup.DrillOptions{Scratch: dir, TmpDir: tmp, AllowNonTmpfs: s.opt.AllowNonTmpfs})
		stamp := strconv.FormatInt(s.now().Unix(), 10)
		if err != nil {
			s.log.Error("restore drill failed", "err", err.Error())
			s.finish(actor, "backup.drill", "backup", info.Name, "failed", map[string]string{"backup.last_drill": stamp + ":failed"})
			return "The restore drill FAILED for " + info.Name + ": " + err.Error(), err
		}
		s.finish(actor, "backup.drill", "backup", info.Name, "ok", map[string]string{"backup.last_drill": stamp + ":ok"})
		return fmt.Sprintf("Restore drill passed for %s: it opened with this server's key, the admin log chain verifies (%d entries), %d account(s), %d upload(s) and %d photo(s) read back.",
			info.Name, rep.LogEntries, rep.Users, rep.UploadBlobs, rep.MediaBlobs), nil
	}, s.now)
	s.afterStart(q, started)
}

// pick finds the backup named in the form, refusing anything that is not one of ours.
func (s *Server) pick(q *rq) (backup.Info, bool) {
	if !s.backupConfigured() {
		q.backupError("No backup folder is configured (ALCOVE_BACKUP_DIR).")
		return backup.Info{}, false
	}
	info, err := backup.Find(s.opt.BackupDir, q.r.PostForm.Get("name"))
	if err != nil {
		q.backupError("That backup was not found.")
		return backup.Info{}, false
	}
	return info, true
}

var backgroundCtx = context.Background()

// postRebuild throws away every owner's derived row cache and search index and refills them from
// each head revision's stored upload (D-29). It is a maintenance action, so it asks for step-up
// and is logged; the result shows on the same page as a backup job.
func (s *Server) postRebuild(q *rq) {
	if q.needStepUp("/admin/backup") {
		return
	}
	svc, actor := s.ingestFor(q.st), q.id.user.ID
	started := s.jobs.start("rebuild", "", func() (string, error) {
		done, failed := svc.RebuildAll(backgroundCtx)
		fields := "cache,search"
		if len(failed) > 0 {
			fields = "cache,search,failed"
			s.log.Warn("cache rebuild left some owners alone", "failed", len(failed))
		}
		s.finish(actor, "cache.rebuild", "cache", "all", fields, nil)
		if len(failed) > 0 {
			return fmt.Sprintf("Rebuilt %d account(s). %d could not be rebuilt (no remembered backup password, or the stored upload failed its checksum).", done, len(failed)), fmt.Errorf("%d failed", len(failed))
		}
		return fmt.Sprintf("Rebuilt the search index and cache for %d account(s) from their stored uploads.", done), nil
	}, s.now)
	s.afterStart(q, started)
}
