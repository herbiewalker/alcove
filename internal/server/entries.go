package server

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/herbiewalker/alcove/internal/share"
	"github.com/herbiewalker/alcove/internal/spaces"
)

// entriesPerPage is how many entries a list page shows.
const entriesPerPage = 50

type entryRow struct {
	ID, OwnerID, Who, When, Duration, Rating, Note string
	Mine, Private                                  bool
}

func fmtEntry(e share.Entry) entryRow {
	r := entryRow{ID: e.ID, OwnerID: e.OwnerID, Who: e.OwnerName, When: e.Local.Format(entryFmt), Note: e.Note, Mine: e.Mine}
	if e.Duration != nil {
		r.Duration = fmt.Sprintf("%d min", *e.Duration)
	}
	if e.Rating != nil {
		r.Rating = fmt.Sprintf("%d of 5", *e.Rating)
	}
	return r
}

func pageNumber(q *rq) int {
	n, _ := strconv.Atoi(q.r.URL.Query().Get("page"))
	if n < 1 {
		n = 1
	}
	return n
}

func setPager(p *page, n, total int) {
	if n > 1 {
		p.Newer = n - 1
	}
	if n*entriesPerPage < total {
		p.Older = n + 1
	}
}

// getSpaceEntries lists what the space shows, newest first. The only data path is share.Entries.
func (s *Server) getSpaceEntries(q *rq) {
	sp, _, ok := s.gate(q)
	if !ok {
		return
	}
	n := pageNumber(q)
	var es []share.Entry
	var total int
	err := q.st.View(q.r.Context(), func(tx *sql.Tx) (err error) {
		es, total, err = share.Entries(tx, sp.ID, q.id.user.ID, spaces.ConsentVersion, share.Query{Limit: entriesPerPage, Offset: (n - 1) * entriesPerPage})
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
		q.serverError("list shared entries", err)
		return
	}
	p := &page{Title: sp.Name + " entries", Active: "spaces", EntryTotal: total, SpaceID: sp.ID, SpaceName: sp.Name}
	for _, e := range es {
		p.Shared = append(p.Shared, fmtEntry(e))
	}
	setPager(p, n, total)
	q.render(http.StatusOK, "spaceentries", p)
}

func (s *Server) myEntriesPage(q *rq) (*page, error) {
	n := pageNumber(q)
	var mine []share.Mine
	var total int
	err := q.st.View(q.r.Context(), func(tx *sql.Tx) (err error) {
		mine, total, err = share.Own(tx, q.id.user.ID, entriesPerPage, (n-1)*entriesPerPage)
		return
	})
	if err != nil {
		return nil, err
	}
	p := &page{Title: "My entries", Active: "uploads", EntryTotal: total, PageNo: n}
	for _, m := range mine {
		r := fmtEntry(m.Entry)
		r.Private = m.Private
		p.Shared = append(p.Shared, r)
	}
	setPager(p, n, total)
	return p, nil
}

func (s *Server) getMyEntries(q *rq) {
	p, err := s.myEntriesPage(q)
	if err != nil {
		q.serverError("list own entries", err)
		return
	}
	q.render(http.StatusOK, "myentries", p)
}

// postEntryPrivate marks one of the caller's own entries private or shared again. SetPrivate
// refuses an id that is not in the caller's own data, so this cannot touch anyone else's entry.
func (s *Server) postEntryPrivate(q *rq) {
	id, private := q.r.PostForm.Get("id"), q.r.PostForm.Get("private") == "1"
	err := q.st.Update(q.r.Context(), true, func(tx *sql.Tx) error {
		return share.SetPrivate(tx, s.now(), q.id.user.ID, id, private)
	})
	switch {
	case errors.Is(err, share.ErrNoEntry):
		q.message(http.StatusNotFound, "Not found", "That entry was not found.", "/entries")
		return
	case err != nil:
		q.serverError("mark entry", err)
		return
	}
	next := "/entries"
	if n, _ := strconv.Atoi(q.r.PostForm.Get("page")); n > 1 {
		next += "?page=" + strconv.Itoa(n)
	}
	q.redirect(next)
}

// getMyEntry opens one of the signed-in person's own entries in full. Only their own rows are read.
func (s *Server) getMyEntry(q *rq) {
	var d share.Detail
	err := q.st.View(q.r.Context(), func(tx *sql.Tx) (err error) {
		d, err = share.OwnDetail(tx, q.id.user.ID, q.r.PathValue("id"))
		return
	})
	switch {
	case errors.Is(err, share.ErrNoEntry):
		q.notFound()
		return
	case err != nil:
		q.serverError("read entry", err)
		return
	}
	q.render(http.StatusOK, "entry", &page{Title: "Entry", Active: "uploads", Detail: &d, DetailBack: "/entries", DetailBackLabel: "My entries"})
}

// getSpaceEntry opens one entry of a member of a space, only if the space's list would show it to
// this person. A missing space, a missing entry, a hidden entry and an outsider all get the same page.
func (s *Server) getSpaceEntry(q *rq) {
	sp, _, ok := s.gate(q)
	if !ok {
		return
	}
	var d share.Detail
	err := q.st.View(q.r.Context(), func(tx *sql.Tx) (err error) {
		d, err = share.EntryDetail(tx, sp.ID, q.id.user.ID, spaces.ConsentVersion, q.r.PathValue("owner"), q.r.PathValue("entry"))
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
		q.serverError("read shared entry", err)
		return
	}
	q.render(http.StatusOK, "entry", &page{Title: "Entry", Active: "spaces", Detail: &d, SpaceID: sp.ID, SpaceName: sp.Name,
		DetailBack: "/spaces/" + sp.ID + "/entries", DetailBackLabel: sp.Name + ": entries"})
}

// urlSeg escapes one path segment for a link built in Go.
func urlSeg(s string) string { return url.PathEscape(s) }
