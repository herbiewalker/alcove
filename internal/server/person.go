package server

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/herbiewalker/alcove/internal/adminlog"
	"github.com/herbiewalker/alcove/internal/auth"
	"github.com/herbiewalker/alcove/internal/share"
)

// who says whose data a handler works on. For a person's own pages that is the signed-in person.
// For the administrator's pages (D-42 slice 4) it is the person named in the path, and every look
// and every change is written to the admin log (constraint 6).
type who struct {
	owner  string // the person whose data it is
	prefix string // "" for their own pages, "/admin/people/<id>" for the administrator's
	name   string // their display name, shown on the administrator's pages
	admin  bool   // the administrator is acting on someone's data
}

// pg stamps a page with the scope, so its links stay inside it.
func (w who) pg(p *page) *page {
	p.Prefix, p.PersonName = w.prefix, w.name
	return p
}

// home is the list of entries a page goes back to.
func (w who) home() string {
	if w.admin {
		return w.prefix
	}
	return "/entries"
}

// forOwn runs a handler on the signed-in person's own data.
func forOwn(h func(*rq, who)) func(*rq) {
	return func(q *rq) { h(q, who{owner: q.id.user.ID}) }
}

// forPerson runs a handler, for the administrator, on one person's data. A person who does not
// exist is a plain not-found page. A change asks for the password again, like every other change
// the administrator makes.
func forPerson(h func(*rq, who)) func(*rq) {
	return func(q *rq) {
		id := q.r.PathValue("user")
		var u auth.User
		err := q.st.View(q.r.Context(), func(tx *sql.Tx) (err error) {
			u, err = auth.ByID(tx, id)
			return
		})
		switch {
		case errors.Is(err, auth.ErrNoUser):
			q.notFound()
			return
		case err != nil:
			q.serverError("find person", err)
			return
		}
		if q.r.Method == http.MethodPost && q.needStepUp("/admin/users") {
			return
		}
		h(q, who{owner: u.ID, prefix: "/admin/people/" + u.ID, name: u.DisplayName, admin: true})
	}
}

// audit writes the administrator's change to the log in the same transaction as the change, naming
// the table, the row and the fields, never a value (constraints 6 and 8). It does nothing for a
// person working on their own data.
func (s *Server) audit(tx *sql.Tx, q *rq, w who, action, table, rid string, fields []string) error {
	if !w.admin {
		return nil
	}
	target := w.owner + ":" + rid
	if table == "user" {
		table, target = "user", w.owner
	}
	_, err := adminlog.Append(tx, s.now(), q.id.user.ID, action, table, target, strings.Join(fields, ","))
	return err
}

// auditView logs that the administrator opened a person's data, before it is shown. It reports
// whether the page may go on; on a failure it has already answered.
func (s *Server) auditView(q *rq, w who, what, table, rid string) bool {
	if !w.admin {
		return true
	}
	if err := q.st.Update(q.r.Context(), false, func(tx *sql.Tx) error {
		return s.audit(tx, q, w, "data.view", table, rid, []string{what})
	}); err != nil {
		q.serverError("write admin log", err)
		return false
	}
	return true
}

// getPerson is the administrator's list of one person's entries, with the way into their partners
// and what they deleted.
func (s *Server) getPerson(q *rq, w who) {
	n := pageNumber(q)
	var mine []share.Mine
	var total int
	err := q.st.View(q.r.Context(), func(tx *sql.Tx) (err error) {
		mine, total, err = share.Own(tx, w.owner, entriesPerPage, (n-1)*entriesPerPage)
		return
	})
	if err != nil {
		q.serverError("list a person's entries", err)
		return
	}
	if !s.auditView(q, w, "entries", "user", "") {
		return
	}
	p := &page{Title: w.name + ": data", Active: "admin", EntryTotal: total, PageNo: n}
	for _, m := range mine {
		r := fmtEntry(m.Entry)
		r.Private = m.Private
		p.Shared = append(p.Shared, r)
	}
	setPager(p, n, total)
	q.render(http.StatusOK, "persondata", w.pg(p))
}

// getPersonEntry opens one entry of a person for the administrator. Photos are not shown here.
func (s *Server) getPersonEntry(q *rq, w who) {
	var d share.Detail
	err := q.st.View(q.r.Context(), func(tx *sql.Tx) (err error) {
		d, err = share.OwnDetail(tx, w.owner, pathID(q))
		return
	})
	switch {
	case errors.Is(err, share.ErrNoEntry):
		q.notFound()
		return
	case err != nil:
		q.serverError("read a person's entry", err)
		return
	}
	if !s.auditView(q, w, "entry", "encounters", pathID(q)) {
		return
	}
	d.Photos, d.PhotosHidden = nil, 0
	q.render(http.StatusOK, "entry", w.pg(&page{Title: "Entry", Active: "admin", Detail: &d,
		DetailBack: w.prefix, DetailBackLabel: w.name + "'s data"}))
}
