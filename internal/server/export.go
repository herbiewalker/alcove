package server

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/herbiewalker/alcove/internal/edit"
	"github.com/herbiewalker/alcove/internal/export"
	"github.com/herbiewalker/alcove/internal/ingest"
	"github.com/herbiewalker/alcove/internal/trystbk"
)

// minExportPassword is the shortest password Alcove accepts for a new export.
const minExportPassword = 8

// exportInfo is what the export page shows: what the file is based on, what changed, what Tryst's
// restore dialog should say, and what the person has to do.
type exportInfo struct {
	Filename, Based string
	AgeDays         int
	Trysts          int
	Partners        int
	Photos          int
	HasEdits        bool
	Edited, Added   int
	Deleted         int
	PartnersEdited  int
	Saves           int
	Fields          []string
	Remembered      bool
	Newer           bool // the upload is from a newer Tryst than this server understands
}

// loadExport gathers the page's facts. ok is false when there is no usable upload (it has answered).
func (s *Server) loadExport(q *rq) (*exportInfo, bool) {
	svc := s.ingestFor(q.st)
	uid := q.id.user.ID
	list, err := svc.List(q.r.Context(), uid)
	if err != nil {
		q.serverError("list uploads for export", err)
		return nil, false
	}
	var head *ingest.UploadInfo
	for i := range list {
		if list[i].Head {
			head = &list[i]
			break
		}
	}
	if head == nil {
		q.message(http.StatusNotFound, "Nothing to export", "Upload a Tryst backup first. An export is your current data written back out as a new backup.", "/uploads")
		return nil, false
	}
	info := &exportInfo{Filename: head.Filename, Based: head.UploadedAt.Format(timeFmt) + " UTC",
		AgeDays: int(s.now().Sub(head.UploadedAt).Hours() / 24), Remembered: svc.HasStoredPassword(q.r.Context(), uid),
		Newer: head.SchemaStatus == trystbk.SchemaNewer}
	var sum edit.Summary
	err = q.st.View(q.r.Context(), func(tx *sql.Tx) (err error) {
		if sum, err = edit.Summarize(tx, uid); err != nil {
			return err
		}
		for tbl, dst := range map[string]*int{"encounters": &info.Trysts, "partners": &info.Partners, "media": &info.Photos} {
			if err = tx.QueryRow(`SELECT COUNT(*) FROM cache_row WHERE owner_id = ? AND tbl = ?`, uid, tbl).Scan(dst); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		q.serverError("summarize edits", err)
		return nil, false
	}
	info.Edited, info.Added, info.Deleted, info.PartnersEdited, info.Saves = sum.Edited, sum.Added, sum.Deleted, sum.PartnersEdited, sum.Edits
	info.HasEdits = sum.Edits > 0
	seen := map[string]bool{}
	for _, f := range sum.Fields {
		l := fieldLabel(edit.Fields, f)
		if l == f {
			l = fieldLabel(edit.PartnerFields, f)
		}
		if !seen[l] {
			seen[l] = true
			info.Fields = append(info.Fields, l)
		}
	}
	return info, true
}

// getExport is the guided page: what the export is, what changed, what to do on the phone.
func (s *Server) getExport(q *rq) {
	info, ok := s.loadExport(q)
	if !ok {
		return
	}
	q.render(http.StatusOK, "export", &page{Title: "Export to Tryst", Active: "uploads", Export: info, SteppedUp: q.id.sess.SteppedUp(s.now())})
}

// exportError shows the page again with what went wrong, never with a password in it.
func (s *Server) exportError(q *rq, status int, msg string) {
	info, ok := s.loadExport(q)
	if !ok {
		return
	}
	q.render(status, "export", &page{Title: "Export to Tryst", Active: "uploads", Export: info, Error: msg, SteppedUp: q.id.sess.SteppedUp(s.now())})
}

// postExport builds the file and sends it as a download. It asks for the password again, needs the
// "fresh phone backup" confirmation, writes one admin-log line with counts only, and offers nothing
// unless the finished file read back correctly (D-43).
func (s *Server) postExport(q *rq) {
	if q.needStepUp("/export") {
		return
	}
	f := q.r.PostForm
	uid := q.id.user.ID
	svc := s.ingestFor(q.st)
	if f.Get("fresh") != "1" {
		s.exportError(q, http.StatusBadRequest, "Tick the box to confirm you exported a fresh backup from your phone first. Restoring this file replaces what is on the phone, so anything newer than your last upload would be lost.")
		return
	}
	upw := f.Get("upw")
	if upw == "" {
		var err error
		if upw, err = svc.StoredPassword(q.r.Context(), uid); errors.Is(err, ingest.ErrNoPassword) {
			s.exportError(q, http.StatusBadRequest, "Alcove does not keep your backup password, so type it below. It is only used to open your upload for this export.")
			return
		} else if err != nil {
			q.serverError("read stored password", err)
			return
		}
	}
	pw := upw
	if f.Get("use") == "new" {
		pw = f.Get("pw")
		switch {
		case len(pw) < minExportPassword:
			s.exportError(q, http.StatusBadRequest, "The new password must be at least "+strconv.Itoa(minExportPassword)+" characters.")
			return
		case pw != f.Get("pw2"):
			s.exportError(q, http.StatusBadRequest, "The two new passwords are not the same.")
			return
		}
	}
	rc := http.NewResponseController(q.w)
	_ = rc.SetWriteDeadline(s.now().Add(uploadTimeout))
	res, err := svc.Export(q.r.Context(), uid, upw, pw)
	switch {
	case errors.Is(err, ingest.ErrWrongPassword):
		s.exportError(q, http.StatusBadRequest, "That is not the password this backup was made with.")
		return
	case errors.Is(err, ingest.ErrNoRevision):
		q.notFound()
		return
	case errors.Is(err, ingest.ErrCacheStale):
		s.exportError(q, http.StatusConflict, "Your data needs a rebuild before it can be exported. Ask the administrator to press Rebuild now under Backups, then try again.")
		return
	case errors.Is(err, export.ErrSchemaNewer):
		s.exportError(q, http.StatusConflict, "This backup comes from a newer Tryst than this server understands, so it cannot be exported safely yet.")
		return
	case err != nil:
		s.log.Error("export failed", "owner", uid, "err", err.Error())
		s.exportError(q, http.StatusInternalServerError, "The export could not be written correctly, so nothing was offered. Your data is unchanged.")
		return
	}
	fields := "entries:" + strconv.Itoa(res.Rows["encounters"]) + ",partners:" + strconv.Itoa(res.Rows["partners"]) +
		",photos:" + strconv.Itoa(res.Media) + ",left_out:" + strconv.Itoa(res.Dropped)
	if err := q.audit("export.create", "user", uid, fields, nil); err != nil {
		q.serverError("log export", err)
		return
	}
	s.log.Info("export created", "owner", uid, "entries", res.Rows["encounters"], "partners", res.Rows["partners"], "photos", res.Media, "left_out", res.Dropped, "bytes", len(res.File))
	h := q.w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Disposition", `attachment; filename="alcove-`+safeZipName(q.id.user.Username)+"-"+s.now().UTC().Format("20060102")+`.tryst"`)
	h.Set("Content-Length", strconv.Itoa(len(res.File)))
	h.Set("Cache-Control", "no-store")
	setDownloadToken(q)
	_, _ = q.w.Write(res.File)
}
