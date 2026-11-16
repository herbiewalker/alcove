package server

import (
	"bytes"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/herbiewalker/alcove/internal/photo"
	"github.com/herbiewalker/alcove/internal/share"
	"github.com/herbiewalker/alcove/internal/spaces"
)

// maxServedPhoto bounds how much of one photo is held in memory while its metadata is removed.
const maxServedPhoto = 64 << 20

// getMyPhoto sends one of the signed-in person's own photos, exactly as stored. A picture the
// browser can show is shown; anything else is offered as a download, never rendered.
func (s *Server) getMyPhoto(q *rq) { s.myPhoto(q, false) }

// getMyThumb sends the small version of one of the person's own photos (D-44), or the picture
// itself when a small one cannot be made.
func (s *Server) getMyThumb(q *rq) { s.myPhoto(q, true) }

func (s *Server) myPhoto(q *rq, thumb bool) {
	id := q.r.PathValue("id")
	var kind photo.Kind
	err := q.st.View(q.r.Context(), func(tx *sql.Tx) (err error) {
		kind, err = share.OwnPhoto(tx, q.id.user.ID, id)
		return
	})
	if errors.Is(err, share.ErrNoEntry) {
		q.notFound()
		return
	}
	if err != nil {
		q.serverError("find photo", err)
		return
	}
	if thumb && kind != photo.Unknown && s.sendThumb(q, q.id.user.ID, id) {
		return
	}
	rc, ok := s.openPhoto(q, q.id.user.ID, id)
	if !ok {
		return
	}
	defer rc.Close()
	h := q.w.Header()
	setPhotoHeaders(h)
	if kind == photo.Unknown {
		h.Set("Content-Type", "application/octet-stream")
		h.Set("Content-Disposition", `attachment; filename="photo"`)
	} else {
		h.Set("Content-Type", kind.MIME())
	}
	_, _ = io.Copy(q.w, rc)
}

// getSpacePhoto sends another member's photo with its metadata removed, only if the space's own
// rules show that photo to this person. Everything else is the same 404 page. The cleaned file is
// built in memory first, so a picture that cannot be cleaned is never sent half done.
func (s *Server) getSpacePhoto(q *rq) { s.spacePhoto(q, false) }

// getSpaceThumb is the small version of a shared photo, behind exactly the same gate (D-44).
func (s *Server) getSpaceThumb(q *rq) { s.spacePhoto(q, true) }

func (s *Server) spacePhoto(q *rq, thumb bool) {
	sp, _, ok := s.gate(q)
	if !ok {
		return
	}
	owner, id := q.r.PathValue("owner"), q.r.PathValue("media")
	var kind photo.Kind
	err := q.st.View(q.r.Context(), func(tx *sql.Tx) (err error) {
		kind, err = share.OpenPhoto(tx, sp.ID, q.id.user.ID, spaces.ConsentVersion, owner, id)
		return
	})
	switch {
	case errors.Is(err, share.ErrNotMember), errors.Is(err, share.ErrNoEntry):
		q.notFound()
		return
	case errors.Is(err, share.ErrNeedConsent):
		q.redirect("/spaces/" + sp.ID)
		return
	case err != nil:
		q.serverError("find shared photo", err)
		return
	}
	if thumb && s.sendThumb(q, owner, id) {
		return
	}
	rc, ok := s.openPhoto(q, owner, id)
	if !ok {
		return
	}
	defer rc.Close()
	var clean bytes.Buffer
	got, err := photo.Strip(io.LimitReader(rc, maxServedPhoto+1), &clean)
	if err != nil || got != kind || clean.Len() > maxServedPhoto {
		s.log.Warn("a shared photo could not be cleaned", "owner", owner)
		q.notFound()
		return
	}
	h := q.w.Header()
	setPhotoHeaders(h)
	h.Set("Content-Type", kind.MIME())
	h.Set("Content-Length", strconv.Itoa(clean.Len()))
	_, _ = q.w.Write(clean.Bytes())
}

// openPhoto opens the decrypting reader for one stored photo. The data key is fetched outside any
// transaction, and zeroed as soon as the reader exists.
func (s *Server) openPhoto(q *rq, owner, id string) (io.ReadCloser, bool) {
	rc, ok := s.openPhotoQuiet(q, owner, id)
	if !ok {
		q.notFound()
	}
	return rc, ok
}

// openPhotoQuiet is openPhoto without writing a response when it fails.
func (s *Server) openPhotoQuiet(q *rq, owner, id string) (io.ReadCloser, bool) {
	dek, err := q.st.DataKey(owner)
	if err != nil {
		s.log.Error("photo key", "err", err.Error())
		return nil, false
	}
	key := dek.Bytes()
	dek.Zero()
	rc, err := s.ingestFor(q.st).Blobs.OpenMedia(owner, id, key)
	for i := range key {
		key[i] = 0
	}
	if err != nil {
		s.log.Warn("a stored photo could not be opened", "owner", owner)
		return nil, false
	}
	return rc, true
}

// setPhotoHeaders keeps a picture from being cached, sniffed or treated as a page.
func setPhotoHeaders(h http.Header) {
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
}
