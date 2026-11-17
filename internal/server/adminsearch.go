package server

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/herbiewalker/alcove/internal/adminlog"
	"github.com/herbiewalker/alcove/internal/adminsearch"
	"github.com/herbiewalker/alcove/internal/share"
)

func (s *Server) getAdminSearch(q *rq) {
	q.render(http.StatusOK, "adminsearch", &page{Title: "Search everything", Active: "admin"})
}

// postAdminSearch searches every person's current notes and partner names (D-46). It is a POST so
// the words never reach an address or a log. The search and its log line commit together; the line
// records how long the term was and how many entries matched, never the term (constraint 8, D-17).
func (s *Server) postAdminSearch(q *rq) {
	term := q.r.PostForm.Get("q")
	p := &page{Title: "Search everything", Active: "admin", SearchQ: term, Searched: true}
	var hits []adminsearch.Hit
	var serr error
	err := q.st.Update(q.r.Context(), true, func(tx *sql.Tx) error {
		hits, serr = adminsearch.Search(tx, term, searchLimit)
		if serr != nil {
			return nil // a refused term is not a search: nothing is read, so nothing is logged
		}
		_, err := adminlog.Append(tx, s.now(), q.id.user.ID, "admin.search", "search", "all",
			"len:"+strconv.Itoa(utf8.RuneCountInString(term))+",hits:"+strconv.Itoa(len(hits)))
		return err
	})
	switch {
	case err != nil:
		q.serverError("admin search", err)
		return
	case errors.Is(serr, share.ErrQueryShort):
		p.Searched, p.Error = false, "Type at least two characters."
		q.render(http.StatusBadRequest, "adminsearch", p)
		return
	case errors.Is(serr, share.ErrQueryLong):
		p.Searched, p.Error = false, "That search is too long. Use fewer words."
		q.render(http.StatusBadRequest, "adminsearch", p)
		return
	case serr != nil:
		q.serverError("admin search", serr)
		return
	}
	for _, h := range hits {
		p.Results = append(p.Results, resultRow{Who: h.OwnerName, When: h.When.Format(entryFmt), Segs: h.Segs, Partner: h.Partner,
			Link: "/admin/people/" + urlSeg(h.OwnerID) + "/entries/" + urlSeg(h.EntryID)})
	}
	p.SearchFull = len(hits) >= searchLimit
	q.render(http.StatusOK, "adminsearch", p)
}
