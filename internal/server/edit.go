package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/herbiewalker/alcove/internal/edit"
	"github.com/herbiewalker/alcove/internal/share"
)

const whenLayout = "2006-01-02T15:04" // the value of a datetime-local input

// choice is one option of a select or a checkbox group.
type choice struct {
	Value, Label string
	On           bool
}

// editForm is what the edit page shows: the current values, as text.
type editForm struct {
	ID, Rev, When, Duration, Rating, Orgasms, Note string
	Moods, Initiators, Protections                 []choice
	Zone                                           string
}

// histChange is one field of one edit, ready to read.
type histChange struct{ Label, Old, New string }

type histRow struct {
	ID, When, By string
	Changes      []histChange
}

// label turns Tryst's SCREAMING_CASE token into words.
func label(t string) string {
	t = strings.ToLower(strings.ReplaceAll(t, "_", " "))
	if t == "" {
		return t
	}
	return strings.ToUpper(t[:1]) + t[1:]
}

func choices(list []string, on map[string]bool) []choice {
	out := make([]choice, 0, len(list))
	for _, v := range list {
		out = append(out, choice{Value: v, Label: label(v), On: on[v]})
	}
	return out
}

func fieldLabel(name string) string {
	for _, f := range edit.Fields {
		if f.Name == name {
			return f.Label
		}
	}
	return name
}

// valueText shows a stored value to a person. A start time is shown in their zone.
func valueText(field string, raw json.RawMessage, loc *time.Location) string {
	s := edit.Describe(raw)
	switch field {
	case "startAt":
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return time.UnixMilli(n).In(loc).Format(entryFmt)
		}
	case "mood", "initiator":
		return label(s)
	case "protectionUsed":
		var out []string
		for _, t := range strings.Split(s, ",") {
			if t = strings.TrimSpace(t); t != "" {
				out = append(out, label(t))
			}
		}
		return strings.Join(out, ", ")
	}
	return s
}

// formOf builds the form's current values from an entry's raw editable values.
func formOf(id, rev string, cur map[string]json.RawMessage, loc *time.Location) *editForm {
	f := &editForm{ID: id, Rev: rev, Zone: loc.String()}
	if n, err := strconv.ParseInt(edit.Describe(cur["startAt"]), 10, 64); err == nil {
		f.When = time.UnixMilli(n).In(loc).Format(whenLayout)
	}
	f.Duration = edit.Describe(cur["durationMin"])
	f.Rating = edit.Describe(cur["satisfactionRating"])
	f.Orgasms = edit.Describe(cur["orgasmCountSelf"])
	f.Note = edit.Describe(cur["note"])
	f.fill(edit.Describe(cur["mood"]), edit.Describe(cur["initiator"]), edit.Describe(cur["protectionUsed"]))
	return f
}

func (f *editForm) fill(mood, initiator, protect string) {
	on := map[string]bool{}
	for _, t := range strings.Split(protect, ",") {
		on[strings.TrimSpace(t)] = true
	}
	f.Moods = choices(edit.Moods, map[string]bool{mood: true})
	f.Initiators = choices(edit.Initiators, map[string]bool{initiator: true})
	f.Protections = choices(edit.Protections, on)
}

func entryID(q *rq) string { return q.r.PathValue("id") }

// getEditEntry shows the edit form for one of the signed-in person's own entries.
func (s *Server) getEditEntry(q *rq) {
	id := entryID(q)
	var cur map[string]json.RawMessage
	var rev string // the revision the form was opened on, read again only to confirm the entry exists
	var loc *time.Location
	err := q.st.View(q.r.Context(), func(tx *sql.Tx) (err error) {
		cur, rev, err = edit.Current(tx, q.id.user.ID, id)
		loc = share.LocationOf(tx, q.id.user.ID)
		return
	})
	switch {
	case errors.Is(err, edit.ErrNoEntry), errors.Is(err, edit.ErrNoHead):
		q.notFound()
		return
	case err != nil:
		q.serverError("read entry for edit", err)
		return
	}
	q.render(http.StatusOK, "editentry", &page{Title: "Edit entry", Active: "uploads", EditForm: formOf(id, rev, cur, loc)})
}

// formMessage says what was wrong, without repeating the value (constraint 8).
func formMessage(err error) string {
	var bad *edit.BadField
	switch {
	case errors.As(err, &bad):
		return fieldLabel(bad.Field) + " " + bad.Rule + "."
	case errors.Is(err, edit.ErrStale):
		return "This entry was changed after you opened it. Nothing was saved. Open it again to see the current values."
	case errors.Is(err, edit.ErrNoChange):
		return "Nothing was changed."
	}
	return ""
}

// postEditEntry saves the form as one edit of one of the signed-in person's own entries. Every
// value is checked first; a refused save changes nothing.
func (s *Server) postEditEntry(q *rq) {
	id, f := entryID(q), q.r.PostForm
	var cur map[string]json.RawMessage
	var loc *time.Location
	// What the person may be keeping as it was (a time typed to the minute must not lose its seconds).
	if err := q.st.View(q.r.Context(), func(tx *sql.Tx) (err error) {
		cur, _, err = edit.Current(tx, q.id.user.ID, id)
		loc = share.LocationOf(tx, q.id.user.ID)
		return
	}); errors.Is(err, edit.ErrNoEntry) || errors.Is(err, edit.ErrNoHead) {
		q.notFound()
		return
	} else if err != nil {
		q.serverError("read entry for edit", err)
		return
	}
	in := map[string]string{
		"durationMin":        f.Get("duration"),
		"satisfactionRating": f.Get("rating"),
		"mood":               f.Get("mood"),
		"initiator":          f.Get("initiator"),
		"protectionUsed":     strings.Join(f["protect"], ","),
		"orgasmCountSelf":    f.Get("orgasms"),
		"note":               strings.ReplaceAll(f.Get("note"), "\r\n", "\n"),
	}
	oldMs, _ := strconv.ParseInt(edit.Describe(cur["startAt"]), 10, 64)
	if w := strings.TrimSpace(f.Get("when")); w == time.UnixMilli(oldMs).In(loc).Format(whenLayout) {
		in["startAt"] = strconv.FormatInt(oldMs, 10)
	} else if t, err := time.ParseInLocation(whenLayout, w, loc); err == nil {
		in["startAt"] = strconv.FormatInt(t.UnixMilli(), 10)
	} else {
		in["startAt"] = "" // refused below by the same rule as any other bad value
	}
	baseRev := f.Get("rev")
	err := q.st.Update(q.r.Context(), true, func(tx *sql.Tx) error {
		_, _, e := edit.Apply(tx, s.now(), q.id.user.ID, q.id.user.ID, id, baseRev, in)
		return e
	})
	if errors.Is(err, edit.ErrNoEntry) || errors.Is(err, edit.ErrNoHead) {
		q.notFound()
		return
	}
	if msg := formMessage(err); msg != "" {
		// Show what they typed, so nothing is lost; the revision stays the one they opened on.
		form := &editForm{ID: id, Rev: baseRev, When: f.Get("when"), Duration: f.Get("duration"), Rating: f.Get("rating"),
			Orgasms: f.Get("orgasms"), Note: f.Get("note"), Zone: loc.String()}
		form.fill(f.Get("mood"), f.Get("initiator"), strings.Join(f["protect"], ","))
		q.render(http.StatusBadRequest, "editentry", &page{Title: "Edit entry", Active: "uploads", Error: msg, EditForm: form})
		return
	}
	if err != nil {
		q.serverError("save entry edit", err)
		return
	}
	q.setFlash("entry-saved")
	q.redirect("/entries/" + url.PathEscape(id))
}

func (s *Server) historyPage(q *rq, id, errMsg string) (*page, bool) {
	var hist []edit.Revision
	var head string
	var loc *time.Location
	var exists bool
	err := q.st.View(q.r.Context(), func(tx *sql.Tx) (err error) {
		if _, head, err = edit.Current(tx, q.id.user.ID, id); err != nil {
			return err
		}
		exists = true
		loc = share.LocationOf(tx, q.id.user.ID)
		hist, err = edit.History(tx, q.id.user.ID, id)
		return
	})
	if errors.Is(err, edit.ErrNoEntry) || errors.Is(err, edit.ErrNoHead) || !exists && err == nil {
		q.notFound()
		return nil, false
	}
	if err != nil {
		q.serverError("read entry history", err)
		return nil, false
	}
	p := &page{Title: "Entry history", Active: "uploads", Error: errMsg, HistID: id, HistRev: head}
	for _, r := range hist {
		row := histRow{ID: r.ID, When: r.At.In(loc).Format(entryFmt), By: "You"}
		if !r.ByOwner {
			row.By = "The administrator"
		}
		for _, c := range r.Changes {
			row.Changes = append(row.Changes, histChange{fieldLabel(c.Field), valueText(c.Field, c.Old, loc), valueText(c.Field, c.New, loc)})
		}
		p.History = append(p.History, row)
	}
	return p, true
}

// getEntryHistory lists the edits of one of the person's own entries, newest first.
func (s *Server) getEntryHistory(q *rq) {
	if p, ok := s.historyPage(q, entryID(q), ""); ok {
		q.render(http.StatusOK, "history", p)
	}
}

// postRevertEntry sets back what one earlier edit changed, as a new edit.
func (s *Server) postRevertEntry(q *rq) {
	id, f := entryID(q), q.r.PostForm
	err := q.st.Update(q.r.Context(), true, func(tx *sql.Tx) error {
		_, _, e := edit.Revert(tx, s.now(), q.id.user.ID, q.id.user.ID, id, f.Get("rev"), f.Get("revision"))
		return e
	})
	if errors.Is(err, edit.ErrNoEntry) || errors.Is(err, edit.ErrNoHead) {
		q.notFound()
		return
	}
	if msg := formMessage(err); msg != "" {
		if p, ok := s.historyPage(q, id, msg); ok {
			q.render(http.StatusBadRequest, "history", p)
		}
		return
	}
	if err != nil {
		q.serverError("revert entry edit", err)
		return
	}
	q.setFlash("entry-reverted")
	q.redirect("/entries/" + url.PathEscape(id) + "/history")
}
