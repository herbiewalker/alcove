package server

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/herbiewalker/alcove/internal/share"
	"github.com/herbiewalker/alcove/internal/spaces"
)

// searchLimit is how many results one search shows.
const searchLimit = 50

type scopeOption struct {
	Value, Label string
	Selected     bool
}

type resultRow struct {
	Link, Who, When string
	Segs            []share.Seg
	Partner, Mine   bool
}

// searchScopes lists where a person may search: their own data and each space they are active in.
func (s *Server) searchScopes(q *rq, chosen string) ([]scopeOption, error) {
	opts := []scopeOption{{Value: "me", Label: "My own entries", Selected: chosen == "me" || chosen == ""}}
	err := q.st.View(q.r.Context(), func(tx *sql.Tx) error {
		ms, err := spaces.ForUser(tx, q.id.user.ID)
		if err != nil {
			return err
		}
		for _, m := range ms {
			if m.State == spaces.StateActive {
				opts = append(opts, scopeOption{Value: m.ID, Label: "Shared in " + m.Name, Selected: chosen == m.ID})
			}
		}
		return nil
	})
	return opts, err
}

func (s *Server) getSearch(q *rq) {
	scopes, err := s.searchScopes(q, q.r.URL.Query().Get("in"))
	if err != nil {
		q.serverError("list search scopes", err)
		return
	}
	q.render(http.StatusOK, "search", &page{Title: "Search", Active: "search", SearchScopes: scopes})
}

// postSearch runs a search. It is a POST on purpose: what a person searches for never goes into
// an address, the browser's history, a proxy's log or a Referer.
func (s *Server) postSearch(q *rq) {
	term, scope := q.r.PostForm.Get("q"), q.r.PostForm.Get("scope")
	if scope == "" {
		scope = "me"
	}
	scopes, err := s.searchScopes(q, scope)
	if err != nil {
		q.serverError("list search scopes", err)
		return
	}
	p := &page{Title: "Search", Active: "search", SearchScopes: scopes, SearchQ: term, Searched: true}
	var found []share.Found
	err = q.st.View(q.r.Context(), func(tx *sql.Tx) (err error) {
		if scope == "me" {
			found, err = share.SearchOwn(tx, q.id.user.ID, term, searchLimit)
		} else {
			found, err = share.SearchSpace(tx, scope, q.id.user.ID, spaces.ConsentVersion, term, searchLimit)
		}
		return
	})
	switch {
	case errors.Is(err, share.ErrQueryShort):
		p.Searched, p.Error = false, "Type at least two characters."
		q.render(http.StatusBadRequest, "search", p)
		return
	case errors.Is(err, share.ErrQueryLong):
		p.Searched, p.Error = false, "That search is too long. Use fewer words."
		q.render(http.StatusBadRequest, "search", p)
		return
	case errors.Is(err, share.ErrNotMember):
		q.notFound() // a space that is not yours looks exactly like one that does not exist
		return
	case errors.Is(err, share.ErrNeedConsent):
		q.redirect("/spaces/" + scope)
		return
	case err != nil:
		q.serverError("search", err)
		return
	}
	for _, f := range found {
		r := resultRow{Who: f.OwnerName, When: f.Local.Format(timeFmt), Segs: f.Segs, Partner: f.Partner, Mine: f.Mine}
		if scope == "me" {
			r.Link = "/entries/" + urlSeg(f.EntryID)
		} else {
			r.Link = "/spaces/" + scope + "/entry/" + urlSeg(f.OwnerID) + "/" + urlSeg(f.EntryID)
		}
		p.Results = append(p.Results, r)
	}
	p.SearchFull = len(found) >= searchLimit
	q.render(http.StatusOK, "search", p)
}
