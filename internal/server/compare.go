package server

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/herbiewalker/alcove/internal/compare"
	"github.com/herbiewalker/alcove/internal/share"
	"github.com/herbiewalker/alcove/internal/spaces"
)

// compareCap bounds how many of one member's entries the compare page reads, newest first. A hostile or
// very large backup must not be able to make one page view use unbounded memory (audit 2026-11-21).
const compareCap = 10000

type periodLink struct {
	Label, Link string
	Current     bool
}

type compareView struct {
	Periods  []periodLink
	Period   string
	Sections []compare.Section
	Members  int
	Cut      []string // members with more entries than compareCap: only their newest are counted
}

// getSpaceCompare shows the compare metrics for a space. Its only data path is share.Entries and
// share.Roster, so a metric can never count an entry the list would not show.
func (s *Server) getSpaceCompare(q *rq) {
	sp, _, ok := s.gate(q)
	if !ok {
		return
	}
	now := s.now().UTC()
	period := compare.PeriodByID(q.r.URL.Query().Get("period"), now)
	var roster []share.Member
	var entries []share.Entry
	var cut []string
	err := q.st.View(q.r.Context(), func(tx *sql.Tx) (err error) {
		if roster, err = share.Roster(tx, sp.ID, q.id.user.ID, spaces.ConsentVersion); err != nil {
			return err
		}
		for _, m := range roster {
			es, total, err := share.Entries(tx, sp.ID, q.id.user.ID, spaces.ConsentVersion, share.Query{Owner: m.UserID, Limit: compareCap})
			if err != nil {
				return err
			}
			entries = append(entries, es...)
			if total > compareCap {
				cut = append(cut, m.Name)
			}
		}
		return nil
	})
	switch {
	case errors.Is(err, share.ErrNotMember):
		q.notFound()
		return
	case errors.Is(err, share.ErrNeedConsent):
		q.redirect("/spaces/" + sp.ID)
		return
	case err != nil:
		q.serverError("read compare", err)
		return
	}
	v := &compareView{Period: period.Label, Members: len(roster), Cut: cut}
	for _, p := range compare.Periods(now) {
		v.Periods = append(v.Periods, periodLink{Label: p.Label, Link: "/spaces/" + sp.ID + "/compare?period=" + p.ID, Current: p.ID == period.ID})
	}
	v.Sections = compare.Build(compare.Input{Members: roster, Entries: entries, Period: period, Now: now})
	q.render(http.StatusOK, "compare", &page{Title: sp.Name + " compare", Active: "spaces", SpaceID: sp.ID, SpaceName: sp.Name, Compare: v})
}
