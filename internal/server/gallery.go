package server

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/herbiewalker/alcove/internal/share"
	"github.com/herbiewalker/alcove/internal/spaces"
)

// photosPerPage is how many pictures a gallery page lists. Pictures load only when scrolled to.
const photosPerPage = 24

// tile is one picture in a gallery: where to load it, and where its entry opens.
type tile struct {
	Src, Thumb, Entry, Who, When, Caption string
	Mine                                  bool
}

func tileOf(spaceID string, it share.GalleryItem) tile {
	t := tile{Who: it.OwnerName, When: it.When.Format(entryFmt), Caption: it.Caption, Mine: it.Mine}
	if spaceID == "" {
		t.Src = "/photos/" + urlSeg(it.MediaID)
		t.Thumb = t.Src + "/thumb"
		t.Entry = "/entries/" + urlSeg(it.EntryID)
	} else {
		t.Src = "/spaces/" + spaceID + "/photo/" + urlSeg(it.OwnerID) + "/" + urlSeg(it.MediaID)
		t.Thumb = t.Src + "/thumb"
		t.Entry = "/spaces/" + spaceID + "/entry/" + urlSeg(it.OwnerID) + "/" + urlSeg(it.EntryID)
	}
	return t
}

func setPhotoPager(p *page, n, total int) {
	if n > 1 {
		p.Newer = n - 1
	}
	if n*photosPerPage < total {
		p.Older = n + 1
	}
}

// getMyGallery shows the signed-in person's own pictures. Only their rows are read.
func (s *Server) getMyGallery(q *rq) {
	n := pageNumber(q)
	var items []share.GalleryItem
	var total int
	err := q.st.View(q.r.Context(), func(tx *sql.Tx) (err error) {
		items, total, err = share.OwnGallery(tx, q.id.user.ID, photosPerPage, (n-1)*photosPerPage)
		return
	})
	if err != nil {
		q.serverError("list own photos", err)
		return
	}
	p := &page{Title: "My photos", Active: "uploads", EntryTotal: total, PageNo: n}
	for _, it := range items {
		p.Tiles = append(p.Tiles, tileOf("", it))
	}
	setPhotoPager(p, n, total)
	q.render(http.StatusOK, "gallery", p)
}

// getSpaceGallery shows the pictures a space shows this person. The only data path is share.Gallery.
func (s *Server) getSpaceGallery(q *rq) {
	sp, _, ok := s.gate(q)
	if !ok {
		return
	}
	n := pageNumber(q)
	var items []share.GalleryItem
	var total int
	err := q.st.View(q.r.Context(), func(tx *sql.Tx) (err error) {
		items, total, err = share.Gallery(tx, sp.ID, q.id.user.ID, spaces.ConsentVersion, photosPerPage, (n-1)*photosPerPage)
		return
	})
	switch {
	case errors.Is(err, share.ErrNotMember):
		q.notFound()
		return
	case errors.Is(err, share.ErrNeedConsent):
		q.redirect("/spaces/" + sp.ID)
		return
	case err != nil:
		q.serverError("list shared photos", err)
		return
	}
	p := &page{Title: sp.Name + " photos", Active: "spaces", EntryTotal: total, SpaceID: sp.ID, SpaceName: sp.Name, PageNo: n}
	for _, it := range items {
		p.Tiles = append(p.Tiles, tileOf(sp.ID, it))
	}
	setPhotoPager(p, n, total)
	q.render(http.StatusOK, "gallery", p)
}
