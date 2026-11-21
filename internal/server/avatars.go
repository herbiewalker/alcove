package server

import (
	"bytes"
	"database/sql"
	"errors"
	"io"
	"strconv"

	"github.com/herbiewalker/alcove/internal/photo"
	"github.com/herbiewalker/alcove/internal/share"
	"github.com/herbiewalker/alcove/internal/spaces"
)

// Profile and partner pictures (D-52). They are always sent as the small picture built from pixels
// (D-44), so no metadata can be in them; when one cannot be made the stored file is sent, cleaned of
// metadata first for anyone but the owner. The gates are in internal/share; this file only serves.

// sendAvatar writes one picture after its gate has passed. owner is whose it is; strip is true when
// the viewer is not the owner.
func (s *Server) sendAvatar(q *rq, owner string, a share.AvatarOf, strip bool) {
	if s.sendThumb(q, owner, a.MediaID) {
		return
	}
	rc, ok := s.openPhoto(q, owner, a.MediaID)
	if !ok {
		return
	}
	defer rc.Close()
	h := q.w.Header()
	setPhotoHeaders(h)
	if !strip {
		h.Set("Content-Type", a.Kind.MIME())
		_, _ = io.Copy(q.w, rc)
		return
	}
	var clean bytes.Buffer
	got, err := photo.Strip(io.LimitReader(rc, maxServedPhoto+1), &clean)
	if err != nil || got != a.Kind || clean.Len() > maxServedPhoto {
		s.log.Warn("a shared picture could not be cleaned", "owner", owner)
		q.notFound()
		return
	}
	h.Set("Content-Type", a.Kind.MIME())
	h.Set("Content-Length", strconv.Itoa(clean.Len()))
	_, _ = q.w.Write(clean.Bytes())
}

// getMyAvatar sends one of the signed-in person's own pictures: their profile's, or a partner's.
func (s *Server) getMyAvatar(q *rq) {
	subject := "profile"
	if p := q.r.PathValue("partner"); p != "" {
		subject = "partner:" + p
	}
	var a share.AvatarOf
	err := q.st.View(q.r.Context(), func(tx *sql.Tx) (err error) {
		a, err = share.OwnAvatar(tx, q.id.user.ID, subject)
		return
	})
	if s.avatarGateFailed(q, err) {
		return
	}
	if !viewableKind(a.Kind) {
		q.notFound()
		return
	}
	s.sendAvatar(q, q.id.user.ID, a, false)
}

func viewableKind(k photo.Kind) bool { return k == photo.JPEG || k == photo.PNG }

// avatarGateFailed answers for the errors every picture gate can give and reports whether it did.
func (s *Server) avatarGateFailed(q *rq, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, share.ErrNotMember), errors.Is(err, share.ErrNoEntry):
		q.notFound()
	case errors.Is(err, share.ErrNeedConsent):
		q.redirect("/spaces")
	default:
		q.serverError("find picture", err)
	}
	return true
}

// getSpaceProfilePhoto and getSpacePartnerPhoto serve another member's pictures, behind the space's
// own rules. Everything that is not allowed is the same 404 as any hidden photo.
func (s *Server) getSpaceProfilePhoto(q *rq) {
	sp, _, ok := s.gate(q)
	if !ok {
		return
	}
	owner := q.r.PathValue("owner")
	var a share.AvatarOf
	err := q.st.View(q.r.Context(), func(tx *sql.Tx) (err error) {
		a, err = share.OpenProfilePhoto(tx, sp.ID, q.id.user.ID, spaces.ConsentVersion, owner)
		return
	})
	if s.avatarGateFailed(q, err) {
		return
	}
	s.sendAvatar(q, owner, a, owner != q.id.user.ID)
}

func (s *Server) getSpacePartnerPhoto(q *rq) {
	sp, _, ok := s.gate(q)
	if !ok {
		return
	}
	owner, partner := q.r.PathValue("owner"), q.r.PathValue("partner")
	var a share.AvatarOf
	err := q.st.View(q.r.Context(), func(tx *sql.Tx) (err error) {
		a, err = share.OpenPartnerPhoto(tx, sp.ID, q.id.user.ID, spaces.ConsentVersion, owner, partner)
		return
	})
	if s.avatarGateFailed(q, err) {
		return
	}
	s.sendAvatar(q, owner, a, owner != q.id.user.ID)
}

// ---- the administrator (D-52): every photo and picture of a person, through the person pages ----

// getPersonPhoto sends one of a person's entry photos to the administrator, cleaned of metadata like
// every picture that is not the owner's own. Opening it is written to the admin log (a look at a
// photo, never the photo).
func (s *Server) getPersonPhoto(q *rq, w who) {
	id := q.r.PathValue("media")
	var kind photo.Kind
	err := q.st.View(q.r.Context(), func(tx *sql.Tx) (err error) {
		kind, err = share.OwnPhoto(tx, w.owner, id)
		return
	})
	if s.avatarGateFailed(q, err) {
		return
	}
	if !viewableKind(kind) {
		q.notFound()
		return
	}
	if !s.auditView(q, w, "photo", "media", id) {
		return
	}
	rc, ok := s.openPhoto(q, w.owner, id)
	if !ok {
		return
	}
	defer rc.Close()
	var clean bytes.Buffer
	got, err := photo.Strip(io.LimitReader(rc, maxServedPhoto+1), &clean)
	if err != nil || got != kind || clean.Len() > maxServedPhoto {
		s.log.Warn("a photo could not be cleaned for the administrator", "owner", w.owner)
		q.notFound()
		return
	}
	h := q.w.Header()
	setPhotoHeaders(h)
	h.Set("Content-Type", kind.MIME())
	h.Set("Content-Length", strconv.Itoa(clean.Len()))
	_, _ = q.w.Write(clean.Bytes())
}

// getPersonAvatar sends a person's profile picture, or one partner's, to the administrator.
func (s *Server) getPersonAvatar(q *rq, w who) {
	subject, what := "profile", "profile-photo"
	if p := q.r.PathValue("partner"); p != "" {
		subject, what = "partner:"+p, "partner-photo"
	}
	var a share.AvatarOf
	err := q.st.View(q.r.Context(), func(tx *sql.Tx) (err error) {
		a, err = share.OwnAvatar(tx, w.owner, subject)
		return
	})
	if s.avatarGateFailed(q, err) {
		return
	}
	if !viewableKind(a.Kind) {
		q.notFound()
		return
	}
	if !s.auditView(q, w, what, "avatar", subject) {
		return
	}
	s.sendAvatar(q, w.owner, a, true)
}
