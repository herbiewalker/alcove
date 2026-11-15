package server

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/herbiewalker/alcove/internal/edit"
	"github.com/herbiewalker/alcove/internal/share"
)

// deletedRow is one line of Recently deleted entries.
type deletedRow struct {
	ID, Started, Deleted, Until string
	Restorable                  bool
}

// userWindow is how long a person may restore their own deletion (the retention setting).
func (s *Server) userWindow() time.Duration {
	d, _ := s.days()
	return time.Duration(d) * 24 * time.Hour
}

// entryMessage says why a whole-entry action was refused, without any content.
func entryMessage(err error, days int) string {
	switch {
	case errors.Is(err, edit.ErrNoTemplate):
		return "A new entry copies the layout of the ones already in your backup, so upload a backup that has at least one entry first."
	case errors.Is(err, edit.ErrExpired):
		return "That was deleted more than " + strconv.Itoa(days) + " days ago, so it can no longer be restored here."
	case errors.Is(err, edit.ErrExists):
		return "That entry is already back."
	}
	return ""
}

// getNewEntry shows an empty entry form, with the time set to now in the person's zone.
func (s *Server) getNewEntry(q *rq) {
	var rev string
	var loc *time.Location
	var groups []setGroup
	err := q.st.View(q.r.Context(), func(tx *sql.Tx) (err error) {
		if rev, _, err = edit.Head(tx, q.id.user.ID); err != nil {
			return err
		}
		loc = share.LocationOf(tx, q.id.user.ID)
		groups, err = groupsFor(tx, q.id.user.ID, func(string) map[string]bool { return nil })
		return
	})
	switch {
	case errors.Is(err, edit.ErrNoHead):
		q.message(http.StatusNotFound, "No backup yet", "Upload a Tryst backup first, then you can add entries to it.", "/uploads")
		return
	case err != nil:
		q.serverError("open new entry", err)
		return
	}
	form := formOf("", rev, nil, loc, groups)
	form.New = true
	form.When = s.now().In(loc).Format(whenLayout)
	q.render(http.StatusOK, "editentry", &page{Title: "Add entry", Active: "uploads", EditForm: form})
}

// postNewEntry adds an entry to the signed-in person's own data. Every value is checked first.
func (s *Server) postNewEntry(q *rq) {
	f := q.r.PostForm
	var loc *time.Location
	_ = q.st.View(q.r.Context(), func(tx *sql.Tx) error { loc = share.LocationOf(tx, q.id.user.ID); return nil })
	in, shown := entryInput(f, loc, 0)
	baseRev := f.Get("rev")
	var id string
	err := q.st.Update(q.r.Context(), true, func(tx *sql.Tx) (e error) {
		_, id, e = edit.Add(tx, s.now(), q.id.user.ID, q.id.user.ID, baseRev, in)
		return
	})
	if errors.Is(err, edit.ErrNoHead) {
		q.notFound()
		return
	}
	days, _ := s.days()
	msg := formMessage(edit.Fields, err)
	if msg == "" {
		msg = entryMessage(err, days)
	}
	if msg != "" {
		form := &editForm{New: true, Rev: baseRev, When: f.Get("when"), Duration: f.Get("duration"), Rating: f.Get("rating"),
			Orgasms: f.Get("orgasms"), Note: f.Get("note"), Zone: loc.String()}
		form.fill(f.Get("mood"), f.Get("initiator"), strings.Join(f["protect"], ","))
		_ = q.st.View(q.r.Context(), func(tx *sql.Tx) (e error) {
			form.Groups, e = groupsFor(tx, q.id.user.ID, func(name string) map[string]bool {
				on := map[string]bool{}
				if shown[name] {
					for _, t := range f["set_"+name] {
						on[t] = true
					}
				}
				return on
			})
			return
		})
		q.render(http.StatusBadRequest, "editentry", &page{Title: "Add entry", Active: "uploads", Error: msg, EditForm: form})
		return
	}
	if err != nil {
		q.serverError("add entry", err)
		return
	}
	q.setFlash("entry-added")
	q.redirect("/entries/" + url.PathEscape(id))
}

// getDeleteEntry asks before deleting one of the person's own entries.
func (s *Server) getDeleteEntry(q *rq) {
	id := pathID(q)
	var rev string
	var startAt int64
	var loc *time.Location
	err := q.st.View(q.r.Context(), func(tx *sql.Tx) (err error) {
		cur, head, err := edit.Current(tx, q.id.user.ID, id)
		if err != nil {
			return err
		}
		rev, loc = head, share.LocationOf(tx, q.id.user.ID)
		startAt, _ = strconv.ParseInt(edit.Describe(cur["startAt"]), 10, 64)
		return nil
	})
	switch {
	case errors.Is(err, edit.ErrNoEntry), errors.Is(err, edit.ErrNoHead):
		q.notFound()
		return
	case err != nil:
		q.serverError("open delete page", err)
		return
	}
	days, _ := s.days()
	q.render(http.StatusOK, "deleteentry", &page{Title: "Delete entry", Active: "uploads", DeleteID: id, HistRev: rev,
		DeleteWhen: time.UnixMilli(startAt).In(loc).Format(entryFmt), UserDays: days})
}

// postDeleteEntry deletes one of the person's own entries. It can be restored for the retention window.
func (s *Server) postDeleteEntry(q *rq) {
	id := pathID(q)
	err := q.st.Update(q.r.Context(), true, func(tx *sql.Tx) error {
		_, e := edit.Delete(tx, s.now(), q.id.user.ID, q.id.user.ID, id, q.r.PostForm.Get("rev"))
		return e
	})
	switch {
	case errors.Is(err, edit.ErrNoEntry), errors.Is(err, edit.ErrNoHead):
		q.notFound()
		return
	case errors.Is(err, edit.ErrStale):
		q.message(http.StatusConflict, "Nothing was deleted", formMessage(edit.Fields, err), "/entries")
		return
	case err != nil:
		q.serverError("delete entry", err)
		return
	}
	q.setFlash("entry-deleted")
	q.redirect("/entries/deleted")
}

// getDeletedEntries lists what the person deleted and may still restore.
func (s *Server) getDeletedEntries(q *rq) {
	var list []edit.DeletedEntry
	var rev string
	var loc *time.Location
	err := q.st.View(q.r.Context(), func(tx *sql.Tx) (err error) {
		if rev, _, err = edit.Head(tx, q.id.user.ID); errors.Is(err, edit.ErrNoHead) {
			return nil
		} else if err != nil {
			return err
		}
		loc = share.LocationOf(tx, q.id.user.ID)
		list, err = edit.Deleted(tx, q.id.user.ID, s.now(), s.userWindow())
		return
	})
	if err != nil {
		q.serverError("list deleted entries", err)
		return
	}
	if loc == nil {
		loc = time.UTC
	}
	days, _ := s.days()
	p := &page{Title: "Recently deleted entries", Active: "uploads", HistRev: rev, UserDays: days}
	for _, d := range list {
		r := deletedRow{ID: d.ID, Deleted: d.DeletedAt.In(loc).Format(entryFmt), Restorable: d.Restorable}
		if d.StartMs != 0 {
			r.Started = time.UnixMilli(d.StartMs).In(loc).Format(entryFmt)
		}
		if d.RestoreByMs != 0 {
			r.Until = time.UnixMilli(d.RestoreByMs).In(loc).Format(entryFmt)
		}
		p.DeletedRows = append(p.DeletedRows, r)
	}
	q.render(http.StatusOK, "deletedentries", p)
}

// postRestoreEntry puts one deleted entry back, inside the person's window.
func (s *Server) postRestoreEntry(q *rq) {
	id := pathID(q)
	err := q.st.Update(q.r.Context(), true, func(tx *sql.Tx) error {
		_, e := edit.Restore(tx, s.now(), q.id.user.ID, q.id.user.ID, id, q.r.PostForm.Get("rev"), s.userWindow())
		return e
	})
	days, _ := s.days()
	switch {
	case errors.Is(err, edit.ErrNoEntry), errors.Is(err, edit.ErrNoHead):
		q.notFound()
		return
	case errors.Is(err, edit.ErrStale):
		q.message(http.StatusConflict, "Nothing was restored", formMessage(edit.Fields, err), "/entries/deleted")
		return
	case entryMessage(err, days) != "":
		q.message(http.StatusConflict, "Nothing was restored", entryMessage(err, days), "/entries/deleted")
		return
	case err != nil:
		q.serverError("restore entry", err)
		return
	}
	q.setFlash("entry-restored")
	q.redirect("/entries/" + url.PathEscape(id))
}
