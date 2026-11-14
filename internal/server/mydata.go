package server

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/herbiewalker/alcove/internal/retention"
)

// A person's own side of delete, restore and download (D-30, F3, F16, F21). Deleting and
// downloading ask for the password again, because they are the two things that cannot be taken
// back or that hand the data out.

type myDelRow struct {
	ID, Filename, Size, DeletedAt, Until string
	Entries                              int
}

func (s *Server) myDeletedPage(q *rq) (*page, error) {
	list, err := s.retFor(q.st).ListDeleted(q.r.Context(), q.id.user.ID, false)
	if err != nil {
		return nil, err
	}
	p := &page{Title: "Recently deleted", Active: "uploads", SteppedUp: q.id.sess.SteppedUp(s.now())}
	for _, d := range list {
		p.MyDeleted = append(p.MyDeleted, myDelRow{ID: d.RevisionID, Filename: d.Filename, Size: humanBytes(d.Size),
			DeletedAt: d.DeletedAt.Format(timeFmt), Until: d.Until.Format(timeFmt), Entries: d.Entries})
	}
	p.UserDays, p.AdminDays = s.days()
	return p, nil
}

func (s *Server) getMyDeleted(q *rq) {
	p, err := s.myDeletedPage(q)
	if err != nil {
		q.serverError("list deleted uploads", err)
		return
	}
	q.render(http.StatusOK, "mydeleted", p)
}

func (q *rq) myDeletedError(status int, msg string) {
	p, err := q.s.myDeletedPage(q)
	if err != nil {
		q.serverError("list deleted uploads", err)
		return
	}
	p.Error = msg
	q.render(status, "mydeleted", p)
}

func (s *Server) postUploadDelete(q *rq) {
	if q.needStepUp("/uploads") {
		return
	}
	out, err := s.retFor(q.st).DeleteUpload(q.r.Context(), q.id.user.ID, q.r.PostForm.Get("id"))
	switch {
	case errors.Is(err, retention.ErrNotFound), errors.Is(err, retention.ErrNotYours):
		q.message(http.StatusNotFound, "Not found", "That upload was not found.", "/uploads")
		return
	case err != nil:
		q.serverError("delete upload", err)
		return
	}
	switch {
	case out.NowEmpty:
		q.setFlash("deleted-empty")
	case !out.CacheRebuilt:
		q.setFlash("deleted-nocache")
	default:
		q.setFlash("deleted")
	}
	q.redirect("/uploads")
}

func (s *Server) postUploadRestore(q *rq) {
	_, err := s.retFor(q.st).RestoreUpload(q.r.Context(), q.id.user.ID, q.r.PostForm.Get("id"), false)
	switch {
	case errors.Is(err, retention.ErrExpired):
		q.myDeletedError(http.StatusBadRequest, "The time to restore that upload yourself has ended. Ask the administrator, who can restore it for longer.")
		return
	case errors.Is(err, retention.ErrNotFound), errors.Is(err, retention.ErrNotYours):
		q.myDeletedError(http.StatusNotFound, "That upload was not found.")
		return
	case err != nil:
		q.serverError("restore upload", err)
		return
	}
	q.setFlash("restored")
	q.redirect("/uploads")
}

// postDownload streams a zip of the person's own uploads exactly as stored: still encrypted with
// their own backup password (F3). It is a POST with a token and a fresh step-up, never a link.
func (s *Server) postDownload(q *rq) {
	if q.needStepUp("/uploads") {
		return
	}
	uid := q.id.user.ID
	svc := s.ingestFor(q.st)
	list, err := svc.List(q.r.Context(), uid)
	if err != nil {
		q.serverError("list uploads", err)
		return
	}
	rc := http.NewResponseController(q.w)
	_ = rc.SetWriteDeadline(s.now().Add(uploadTimeout))
	name := "alcove-my-data-" + s.now().UTC().Format("20060102") + ".zip"
	h := q.w.Header()
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Disposition", `attachment; filename="`+name+`"`)
	h.Set("Cache-Control", "no-store")
	zw := zip.NewWriter(q.w)
	var readme strings.Builder
	readme.WriteString("Your data from Alcove\n\nEach file below is a Tryst backup exactly as you uploaded it. It is still encrypted with the\nbackup password you chose in Tryst. Keep it safe, and restore it in Tryst when you want it back.\n\n")
	n := 0
	for _, u := range list {
		if u.Deleted {
			continue
		}
		f, info, err := svc.OpenUpload(q.r.Context(), uid, u.ID)
		if err != nil {
			s.log.Error("download: an upload could not be read", "owner", uid, "err", err.Error())
			readme.WriteString("(one upload could not be read and was left out; ask the administrator)\n")
			continue
		}
		n++
		fname := fmt.Sprintf("%02d-%s-%s", n, info.UploadedAt.Format("20060102"), safeZipName(info.Filename))
		w, err := zw.CreateHeader(&zip.FileHeader{Name: fname, Method: zip.Store, Modified: info.UploadedAt})
		if err == nil {
			_, err = io.Copy(w, f)
		}
		_ = f.Close()
		if err != nil {
			s.log.Warn("download interrupted", "owner", uid)
			return // the connection is gone; there is nothing useful left to write
		}
		fmt.Fprintf(&readme, "%s\n    uploaded %s UTC, %d entries, %d photos\n", fname, info.UploadedAt.Format(timeFmt), info.Encounters, info.Media)
	}
	if n == 0 {
		readme.WriteString("You have no uploads right now.\n")
	}
	if w, err := zw.Create("README.txt"); err == nil {
		_, _ = io.WriteString(w, readme.String())
	}
	_ = zw.Close()
	s.log.Info("data downloaded", "owner", uid, "files", n)
}

// safeZipName keeps a file name usable inside the archive: no path, no odd characters.
func safeZipName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r == '/' || r == '\\' || r < 0x20 || r == ':' || r == '*' || r == '?' || r == '"' || r == '<' || r == '>' || r == '|':
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Trim(b.String(), " .")
	if out == "" {
		return "backup.tryst"
	}
	return out
}
