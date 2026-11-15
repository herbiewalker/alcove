package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/herbiewalker/alcove/internal/blobstore"
	"github.com/herbiewalker/alcove/internal/ingest"
	"github.com/herbiewalker/alcove/internal/store"
	"github.com/herbiewalker/alcove/internal/trystbk"
)

// The upload page and the streaming upload route (D-29, F7, F8, F30). The form is one multipart
// request read as a stream: the CSRF token and options come first, the file last. The token is
// checked before a byte of the file is accepted.

const (
	uploadSlack   = 1 << 20 // multipart framing and the small fields
	uploadTimeout = 25 * time.Minute
)

func (s *Server) maxUpload() int64 {
	if s.opt.MaxUploadBytes > 0 && s.opt.MaxUploadBytes < trystbk.MaxBackupBytes {
		return s.opt.MaxUploadBytes
	}
	return trystbk.MaxBackupBytes
}

// ingestFor returns the ingest service for the open store (built once per store).
func (s *Server) ingestFor(st *store.Store) *ingest.Service {
	s.ingMu.Lock()
	defer s.ingMu.Unlock()
	if s.ing == nil || s.ingStore != st {
		s.ing = ingest.New(st, blobstore.New(st.DataDir()))
		s.ing.Now = s.now
		s.ing.MaxBytes = s.maxUpload()
		s.ingStore = st
	}
	return s.ing
}

type uploadRow struct {
	ID, Filename, When, Size, Schema string
	Entries, Media                   int
	Current, ReadOnly                bool
}

func uploadRows(list []ingest.UploadInfo) []uploadRow {
	out := make([]uploadRow, 0, len(list))
	for _, u := range list {
		if u.Deleted {
			continue
		}
		out = append(out, uploadRow{ID: u.RevisionID, Filename: u.Filename, When: u.UploadedAt.UTC().Format(timeFmt), Size: humanBytes(u.Size),
			Schema: fmt.Sprintf("Tryst database v%d", u.TrystSchema), Entries: u.Encounters, Media: u.Media,
			Current: u.Head, ReadOnly: u.SchemaStatus == trystbk.SchemaNewer})
	}
	return out
}

func (s *Server) getUploads(q *rq) {
	svc := s.ingestFor(q.st)
	list, err := svc.List(q.r.Context(), q.id.user.ID)
	if err != nil {
		q.serverError("list uploads", err)
		return
	}
	userDays, _ := s.days()
	var edits int
	_ = q.st.View(q.r.Context(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COUNT(*) FROM revision e WHERE e.owner_id = ? AND e.kind = 'edit' AND e.deleted_at IS NULL
			AND e.upload_id = (SELECT upload_id FROM revision WHERE owner_id = ? AND deleted_at IS NULL ORDER BY created_at DESC, rowid DESC LIMIT 1)`,
			q.id.user.ID, q.id.user.ID).Scan(&edits)
	})
	q.render(http.StatusOK, "uploads", &page{Title: "My data", Active: "uploads", Uploads: uploadRows(list), HasEdits: edits > 0,
		HasPassword: svc.HasStoredPassword(q.r.Context(), q.id.user.ID), MaxMB: int(s.maxUpload() >> 20), UserDays: userDays})
}

// fail answers an upload failure as JSON for the progress script and as a page otherwise. It asks
// the connection to close, because part of the request body may be unread.
func (q *rq) uploadFail(status int, msg string) {
	q.w.Header().Set("Connection", "close")
	if q.r.Header.Get("X-Requested-With") == "alcove" {
		q.w.Header().Set("Content-Type", "application/json")
		q.w.Header().Set("Cache-Control", "no-store")
		q.w.WriteHeader(status)
		_ = json.NewEncoder(q.w).Encode(map[string]any{"ok": false, "message": msg})
		return
	}
	q.message(status, "The upload was not saved", msg, "/uploads")
}

func readSmall(r io.Reader, max int64) (string, error) {
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return "", err
	}
	if int64(len(b)) > max {
		return "", errors.New("field too long")
	}
	return string(b), nil
}

func (s *Server) postUploads(q *rq) {
	w, r := q.w, q.r
	// A phone on slow wifi can take minutes; give this one request longer than the server default.
	rc := http.NewResponseController(w)
	deadline := s.now().Add(uploadTimeout)
	_ = rc.SetReadDeadline(deadline)
	_ = rc.SetWriteDeadline(deadline.Add(2 * time.Minute))

	max := s.maxUpload()
	if r.ContentLength > max+uploadSlack {
		q.uploadFail(http.StatusRequestEntityTooLarge, fmt.Sprintf("This file is larger than the %d MiB Alcove accepts.", max>>20))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, max+uploadSlack)
	mr, err := r.MultipartReader()
	if err != nil {
		q.uploadFail(http.StatusBadRequest, "That was not a file upload. Use the form on the My data page.")
		return
	}
	var password string
	remember, csrfOK := false, false
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			q.uploadFail(http.StatusBadRequest, "No file was sent. Choose your Tryst backup file and try again.")
			return
		}
		if err != nil {
			s.uploadReadError(q, err)
			return
		}
		switch part.FormName() {
		case "csrf":
			v, err := readSmall(part, 256)
			if err != nil || !q.csrfOK(v) {
				q.uploadFail(http.StatusForbidden, "That page expired. Reload it and try again.")
				return
			}
			csrfOK = true
		case "password":
			if password, err = readSmall(part, 1024); err != nil {
				q.uploadFail(http.StatusBadRequest, "That password is too long.")
				return
			}
		case "remember":
			v, _ := readSmall(part, 8)
			remember = v == "1"
		case "file":
			if !csrfOK {
				q.uploadFail(http.StatusForbidden, "That page expired. Reload it and try again.")
				return
			}
			s.receive(q, part, part.FileName(), password, remember)
			return
		default:
			_, _ = io.Copy(io.Discard, io.LimitReader(part, 4096))
		}
	}
}

func (s *Server) uploadReadError(q *rq, err error) {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		q.uploadFail(http.StatusRequestEntityTooLarge, fmt.Sprintf("This file is larger than the %d MiB Alcove accepts.", s.maxUpload()>>20))
		return
	}
	s.log.Warn("upload interrupted", "owner", q.id.user.ID)
	q.uploadFail(http.StatusBadRequest, "The upload was interrupted before it finished. Nothing was saved. Try again.")
}

// receive stores and judges the file part and answers.
func (s *Server) receive(q *rq, file io.Reader, filename, password string, remember bool) {
	svc := s.ingestFor(q.st)
	uid := q.id.user.ID
	if password == "" {
		pw, err := svc.StoredPassword(q.r.Context(), uid)
		if err != nil {
			q.uploadFail(http.StatusBadRequest, "Enter the password you chose when you exported this backup in Tryst.")
			return
		}
		password, remember = pw, true
	}
	info, err := svc.Upload(q.r.Context(), ingest.Params{Owner: uid, CreatedBy: uid, Filename: filename, Password: password, Remember: remember}, file)
	if err != nil {
		var dup *ingest.DuplicateError
		var mbe *http.MaxBytesError
		switch {
		case errors.As(err, &dup):
			s.log.Info("upload refused", "owner", uid, "reason", "duplicate")
			q.uploadFail(http.StatusConflict, dup.Error()+". Nothing was changed.")
		case errors.Is(err, ingest.ErrWrongPassword):
			s.log.Info("upload refused", "owner", uid, "reason", "wrong_password")
			q.uploadFail(http.StatusUnauthorized, "That is not the password this backup was made with. Check it and try again. Nothing was saved.")
		case errors.Is(err, ingest.ErrNotBackup):
			s.log.Info("upload refused", "owner", uid, "reason", "not_a_backup")
			q.uploadFail(http.StatusUnprocessableEntity, "That file is not a Tryst backup. In Tryst, export a backup and choose that file. Nothing was saved.")
		case errors.Is(err, ingest.ErrDamaged):
			s.log.Warn("upload refused", "owner", uid, "reason", "damaged")
			q.uploadFail(http.StatusUnprocessableEntity, "This backup is damaged or is in a form Alcove cannot read. Export a fresh one from Tryst and try again. Nothing was saved.")
		case errors.Is(err, ingest.ErrTooLarge), errors.As(err, &mbe):
			s.log.Info("upload refused", "owner", uid, "reason", "too_large")
			q.uploadFail(http.StatusRequestEntityTooLarge, fmt.Sprintf("This file is larger than the %d MiB Alcove accepts.", s.maxUpload()>>20))
		case errors.Is(err, io.ErrUnexpectedEOF), strings.Contains(err.Error(), "multipart"):
			s.uploadReadError(q, err)
		default:
			s.log.Error("upload failed", "owner", uid, "err", err.Error())
			q.uploadFail(http.StatusInternalServerError, "The server could not save the upload. Nothing was changed. Try again, and check the server log if it keeps happening.")
		}
		return
	}
	s.log.Info("upload accepted", "owner", uid, "bytes", info.Size, "rows", info.Rows, "entries", info.Encounters, "media", info.Media, "photos_stored", info.Photos.Stored, "photos_skipped", info.Photos.Skipped, "schema", info.TrystSchema)
	q.setFlash("uploaded")
	if q.r.Header.Get("X-Requested-With") == "alcove" {
		q.w.Header().Set("Content-Type", "application/json")
		q.w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(q.w).Encode(map[string]any{"ok": true, "redirect": "/uploads"})
		return
	}
	q.redirect("/uploads")
}
