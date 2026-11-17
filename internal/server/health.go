package server

import (
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/herbiewalker/alcove/internal/share"
)

// The health page (M8, D-47) answers "is the server in good shape?" in one place, with counts and
// states only: no names, no content. Each row has a level: ok, warn (something to do) or info.
type healthRow struct {
	Label, Value, Level, Note string
}

type healthGroup struct {
	Title string
	Rows  []healthRow
}

// staleAfter is how old a backup or a log check may get before the page says so. It matches the
// monthly reminder the author's own scripts use.
const staleAfter = 30 * 24 * time.Hour

func ago(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	}
	return fmt.Sprintf("%d days ago", int(d.Hours()/24))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// health gathers every row. It only reads.
func (s *Server) health(q *rq) (groups []healthGroup, warns int, err error) {
	now := s.now()
	add := func(g *healthGroup, label, value, level, note string) {
		if level == "warn" {
			warns++
		}
		g.Rows = append(g.Rows, healthRow{label, value, level, note})
	}

	var users, active, disabled, pendingUsers, uploads, deletedUploads, photos, spaceN, logN int
	var lastVerifyTS int64
	var lastVerifyResult string
	err = q.st.View(q.r.Context(), func(tx *sql.Tx) error {
		one := func(dst *int, query string) error { return tx.QueryRow(query).Scan(dst) }
		for _, c := range []struct {
			dst *int
			sql string
		}{
			{&users, `SELECT COUNT(*) FROM user WHERE deleted_at IS NULL`},
			{&active, `SELECT COUNT(*) FROM user WHERE deleted_at IS NULL AND status = 'active'`},
			{&disabled, `SELECT COUNT(*) FROM user WHERE deleted_at IS NULL AND status = 'disabled'`},
			{&pendingUsers, `SELECT COUNT(*) FROM user WHERE deleted_at IS NOT NULL`},
			{&uploads, `SELECT COUNT(*) FROM revision WHERE kind = 'upload' AND deleted_at IS NULL`},
			{&deletedUploads, `SELECT COUNT(*) FROM revision WHERE kind = 'upload' AND deleted_at IS NOT NULL`},
			{&photos, `SELECT COUNT(*) FROM media_blob`},
			{&spaceN, `SELECT COUNT(*) FROM space WHERE deleted_at IS NULL`},
			{&logN, `SELECT COUNT(*) FROM admin_log`},
		} {
			if err := one(c.dst, c.sql); err != nil {
				return err
			}
		}
		err := tx.QueryRow(`SELECT ts, fields FROM admin_log WHERE action = 'adminlog.verify' ORDER BY seq DESC LIMIT 1`).Scan(&lastVerifyTS, &lastVerifyResult)
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	})
	if err != nil {
		return nil, 0, err
	}

	// Storage and protection.
	st := healthGroup{Title: "Storage and protection"}
	add(&st, "Database", "open and unlocked", "ok", "The key is held apart from the data folder; the server refuses to start otherwise.")
	if t := q.st.LastSealed(); t.IsZero() {
		add(&st, "Last sealed to disk", "not since this start", "info", "Sealing happens after a change; an idle server has nothing new to seal.")
	} else {
		add(&st, "Last sealed to disk", ago(now, t), "ok", "")
	}
	if v, e := q.st.SchemaVersionInDB(); e == nil {
		add(&st, "Database schema", "v"+strconv.Itoa(v), "info", "")
	}
	add(&st, "Photo thumbnails in memory", plural(s.thumbs.len(), "picture", "pictures"), "info", "Kept only in memory and cleared by a restart (D-44).")
	groups = append(groups, st)

	// Backups and checks: the things that, if forgotten, hurt later.
	bk := healthGroup{Title: "Backups and checks"}
	lastBackup, lastDrill := s.lastTimes(q)
	switch {
	case !s.backupConfigured():
		add(&bk, "Instance backup", "not configured", "warn", "No backup folder is set, so there is no way back from a lost disk. See the runbook, section 2.")
	case lastBackup == "never":
		add(&bk, "Last instance backup", "never", "warn", "Back up now from Admin, Backups, then verify it.")
	default:
		level, note := "ok", ""
		if ts, _, ok := s.metaTime(q, "backup.last_ok"); ok {
			if now.Sub(ts) > staleAfter {
				level, note = "warn", "Older than 30 days. Back up again."
			}
			lastBackup = ago(now, ts)
		}
		add(&bk, "Last instance backup", lastBackup, level, note)
	}
	if s.backupConfigured() {
		if lastDrill == "never" {
			add(&bk, "Last restore drill", "never", "warn", "A backup you have never restored is a hope, not a backup. Run the drill from Admin, Backups.")
		} else {
			add(&bk, "Last restore drill", lastDrill, "ok", "")
		}
	}
	switch {
	case lastVerifyTS == 0:
		add(&bk, "Admin log chain", "never checked", "warn", "Press Verify the chain now on the log page.")
	case lastVerifyResult == "failed":
		add(&bk, "Admin log chain", "the last check FAILED ("+ago(now, time.Unix(lastVerifyTS, 0))+")", "warn", "Treat the log as tampered with until a check passes.")
	case now.Sub(time.Unix(lastVerifyTS, 0)) > staleAfter:
		add(&bk, "Admin log chain", "last checked "+ago(now, time.Unix(lastVerifyTS, 0)), "warn", "Older than 30 days. Check it again.")
	default:
		add(&bk, "Admin log chain", "verified "+ago(now, time.Unix(lastVerifyTS, 0)), "ok", "")
	}
	groups = append(groups, bk)

	// What the server holds.
	dt := healthGroup{Title: "What it holds"}
	add(&dt, "Accounts", strconv.Itoa(users)+" ("+strconv.Itoa(active)+" active, "+strconv.Itoa(disabled)+" disabled)", "info", "")
	if pendingUsers > 0 {
		add(&dt, "Accounts waiting to be removed", strconv.Itoa(pendingUsers), "info", "They are removed for good when their recovery window ends.")
	}
	add(&dt, "Uploaded backups", plural(uploads, "current or older upload", "current and older uploads"), "info", "")
	if deletedUploads > 0 {
		add(&dt, "Uploads in recently deleted", strconv.Itoa(deletedUploads), "info", "Removed for good when their window ends.")
	}
	add(&dt, "Photos stored", strconv.Itoa(photos), "info", "")
	add(&dt, "Spaces", strconv.Itoa(spaceN), "info", "")
	add(&dt, "Admin log lines", strconv.Itoa(logN), "info", "")
	groups = append(groups, dt)

	// Network and settings.
	nv := s.net.Load().View()
	nt := healthGroup{Title: "Network and settings"}
	if nv.RequireTLS {
		add(&nt, "Encrypted connection", "required", "ok", "")
	} else {
		add(&nt, "Encrypted connection", "not required (plain HTTP on the LAN)", "info", "Fine on a secured wifi. Turn on Require HTTPS if a proxy or Tailscale provides it.")
	}
	if nv.ProxyMode {
		add(&nt, "Reverse proxy mode", "on", "info", "")
	}
	if !nv.PendingUntil.IsZero() {
		add(&nt, "Network list change", "waiting for you to confirm it", "warn", "It undoes itself if you do not confirm in time (Network and security).")
	}
	add(&nt, "Default time zone", share.DefaultZoneName(), "info", "")
	add(&nt, "Version", strings.TrimPrefix(s.opt.Version, "v"), "info", "Up since "+s.started.UTC().Format("2006-01-02 15:04")+" UTC.")
	groups = append(groups, nt)
	return groups, warns, nil
}

// metaTime reads a "<unix seconds>[:tag]" meta value.
func (s *Server) metaTime(q *rq, key string) (time.Time, string, bool) {
	v, ok, err := q.st.GetMeta(key)
	if err != nil || !ok {
		return time.Time{}, "", false
	}
	tag := ""
	if i := strings.IndexByte(v, ':'); i >= 0 {
		v, tag = v[:i], v[i+1:]
	}
	ts, err := strconv.ParseInt(v, 10, 64)
	if err != nil || ts <= 0 {
		return time.Time{}, "", false
	}
	return time.Unix(ts, 0), tag, true
}

func (s *Server) getHealth(q *rq) {
	if err := q.audit("admin.health.view", "console", "health", "", nil); err != nil {
		q.serverError("log health view", err)
		return
	}
	groups, warns, err := s.health(q)
	if err != nil {
		q.serverError("health page", err)
		return
	}
	p := &page{Title: "Health", Active: "admin", Health: groups, HealthWarns: warns}
	q.render(http.StatusOK, "health", p)
}
