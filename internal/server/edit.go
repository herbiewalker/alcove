package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
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

// setGroup is one set field of an entry as a group of checkboxes.
type setGroup struct {
	Name, Label string
	Items       []choice
}

// editForm is what the edit page shows: the current values, as text.
type editForm struct {
	ID, Rev, When, Duration, Rating, Orgasms, Note string
	Moods, Initiators, Protections                 []choice
	Groups                                         []setGroup
	Zone                                           string
}

// partnerForm is the partner edit page.
type partnerForm struct{ ID, Rev, Name, Note string }

// partnerRow is one line of the partner list.
type partnerRow struct{ ID, Name string }

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

func fieldLabel(fields []edit.Field, name string) string {
	for _, f := range fields {
		if f.Name == name {
			return f.Label
		}
	}
	return name
}

const removedItem = "(a removed item)"

// valueText shows a stored value to a person. A start time is shown in their zone, a catalog item
// by its label.
func valueText(field string, raw json.RawMessage, loc *time.Location, labels map[string]string) string {
	s := edit.Describe(raw)
	f, _ := lookupField(field)
	switch {
	case field == "startAt":
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return time.UnixMilli(n).In(loc).Format(entryFmt)
		}
	case field == "mood" || field == "initiator":
		return label(s)
	case f.Kind == "tokens" || f.Kind == "places" || edit.CatalogTable(f) != "":
		var out []string
		for _, t := range strings.Split(s, ",") {
			if t = strings.TrimSpace(t); t == "" {
				continue
			}
			if strings.HasPrefix(t, "custom:") {
				l := labels[t]
				if l == "" {
					l = removedItem
				}
				out = append(out, l)
			} else {
				out = append(out, label(t))
			}
		}
		sort.Strings(out)
		return strings.Join(out, ", ")
	}
	return s
}

func lookupField(name string) (edit.Field, bool) {
	for _, f := range edit.Fields {
		if f.Name == name {
			return f, true
		}
	}
	return edit.Field{}, false
}

// tokenSet splits a stored comma list into a set.
func tokenSet(raw json.RawMessage) map[string]bool {
	on := map[string]bool{}
	for _, t := range strings.Split(edit.Describe(raw), ",") {
		if t = strings.TrimSpace(t); t != "" {
			on[t] = true
		}
	}
	return on
}

// groupsFor builds the checkbox groups for the set fields. selected gives what is ticked for each
// field. An item ticked but no longer in the catalog stays, so it is kept unless the person unticks it.
func groupsFor(tx *sql.Tx, owner string, selected func(field string) map[string]bool) ([]setGroup, error) {
	var out []setGroup
	for _, f := range edit.Fields {
		on := selected(f.Name)
		var items []choice
		switch {
		case f.Kind == "places":
			items = choices(edit.Places, on)
		case edit.CatalogTable(f) != "":
			cat, err := edit.Catalog(tx, owner, edit.CatalogTable(f))
			if err != nil {
				return nil, err
			}
			known := map[string]bool{}
			for _, it := range cat {
				tok := "custom:" + it.ID
				known[tok] = true
				items = append(items, choice{Value: tok, Label: it.Label, On: on[tok]})
			}
			var gone []string
			for t := range on {
				if !known[t] {
					gone = append(gone, t)
				}
			}
			sort.Strings(gone)
			for _, t := range gone {
				items = append(items, choice{Value: t, Label: removedItem, On: true})
			}
		default:
			continue
		}
		if len(items) > 0 {
			out = append(out, setGroup{Name: f.Name, Label: f.Label, Items: items})
		}
	}
	return out, nil
}

// formOf builds the form's current values from an entry's raw editable values.
func formOf(id, rev string, cur map[string]json.RawMessage, loc *time.Location, groups []setGroup) *editForm {
	f := &editForm{ID: id, Rev: rev, Zone: loc.String(), Groups: groups}
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

func pathID(q *rq) string { return q.r.PathValue("id") }

// getEditEntry shows the edit form for one of the signed-in person's own entries.
func (s *Server) getEditEntry(q *rq) {
	id := pathID(q)
	var cur map[string]json.RawMessage
	var rev string
	var loc *time.Location
	var groups []setGroup
	err := q.st.View(q.r.Context(), func(tx *sql.Tx) (err error) {
		if cur, rev, err = edit.Current(tx, q.id.user.ID, id); err != nil {
			return err
		}
		loc = share.LocationOf(tx, q.id.user.ID)
		groups, err = groupsFor(tx, q.id.user.ID, func(f string) map[string]bool { return tokenSet(cur[f]) })
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
	q.render(http.StatusOK, "editentry", &page{Title: "Edit entry", Active: "uploads", EditForm: formOf(id, rev, cur, loc, groups)})
}

// formMessage says what was wrong, without repeating the value (constraint 8).
func formMessage(fields []edit.Field, err error) string {
	var bad *edit.BadField
	switch {
	case errors.As(err, &bad):
		return fieldLabel(fields, bad.Field) + " " + bad.Rule + "."
	case errors.Is(err, edit.ErrStale):
		return "This was changed after you opened it. Nothing was saved. Open it again to see the current values."
	case errors.Is(err, edit.ErrNoChange):
		return "Nothing was changed."
	}
	return ""
}

// postEditEntry saves the form as one edit of one of the signed-in person's own entries. Every
// value is checked first; a refused save changes nothing.
func (s *Server) postEditEntry(q *rq) {
	id, f := pathID(q), q.r.PostForm
	var cur map[string]json.RawMessage
	var loc *time.Location
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
	// A set is saved only if the form showed it, so a group with nothing to choose is never cleared.
	shown := map[string]bool{}
	for _, name := range f["has"] {
		shown[name] = true
	}
	for _, fd := range edit.Fields {
		if (fd.Kind == "places" || edit.CatalogTable(fd) != "") && shown[fd.Name] {
			in[fd.Name] = strings.Join(f["set_"+fd.Name], ",")
		}
	}
	// A time left as it was keeps its stored seconds.
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
	if msg := formMessage(edit.Fields, err); msg != "" {
		// Show what they typed, so nothing is lost; the revision stays the one they opened on.
		form := &editForm{ID: id, Rev: baseRev, When: f.Get("when"), Duration: f.Get("duration"), Rating: f.Get("rating"),
			Orgasms: f.Get("orgasms"), Note: f.Get("note"), Zone: loc.String()}
		form.fill(f.Get("mood"), f.Get("initiator"), strings.Join(f["protect"], ","))
		_ = q.st.View(q.r.Context(), func(tx *sql.Tx) (e error) {
			form.Groups, e = groupsFor(tx, q.id.user.ID, func(name string) map[string]bool {
				if shown[name] {
					on := map[string]bool{}
					for _, t := range f["set_"+name] {
						on[t] = true
					}
					return on
				}
				return tokenSet(cur[name])
			})
			return
		})
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

// ---- history and undo, for entries and partners ----

type histKind struct {
	fields  []edit.Field
	base    string // the path of the thing, without its id
	back    string
	backTo  string // where the back link goes; empty means the thing itself
	current func(tx *sql.Tx, owner, id string) (map[string]json.RawMessage, string, error)
	history func(tx *sql.Tx, owner, id string) ([]edit.Revision, error)
	revert  func(tx *sql.Tx, now time.Time, actor, owner, id, base, revision string) (string, []string, error)
}

var (
	entryKind = histKind{edit.Fields, "/entries/", "Back to the entry", "", edit.Current, edit.History, edit.Revert}
	partnKind = histKind{edit.PartnerFields, "/partners/", "Back to my partners", "/partners", edit.PartnerCurrent, edit.PartnerHistory, edit.PartnerRevert}
)

// catalogLabels maps every custom token of the owner's catalogs to its label.
func catalogLabels(tx *sql.Tx, owner string) (map[string]string, error) {
	out := map[string]string{}
	for _, t := range []string{"acts", "positions", "kinks", "toys", "occasions"} {
		items, err := edit.Catalog(tx, owner, t)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			out["custom:"+it.ID] = it.Label
		}
	}
	return out, nil
}

func (s *Server) historyPage(q *rq, k histKind, id, errMsg string) (*page, bool) {
	var hist []edit.Revision
	var head string
	var loc *time.Location
	var labels map[string]string
	err := q.st.View(q.r.Context(), func(tx *sql.Tx) (err error) {
		if _, head, err = k.current(tx, q.id.user.ID, id); err != nil {
			return err
		}
		loc = share.LocationOf(tx, q.id.user.ID)
		if labels, err = catalogLabels(tx, q.id.user.ID); err != nil {
			return err
		}
		hist, err = k.history(tx, q.id.user.ID, id)
		return
	})
	if errors.Is(err, edit.ErrNoEntry) || errors.Is(err, edit.ErrNoHead) {
		q.notFound()
		return nil, false
	}
	if err != nil {
		q.serverError("read entry history", err)
		return nil, false
	}
	p := &page{Title: "History", Active: "uploads", Error: errMsg, HistID: id, HistRev: head, HistBase: k.base + url.PathEscape(id), HistBack: k.back, HistBackHref: k.backTo}
	if p.HistBackHref == "" {
		p.HistBackHref = p.HistBase
	}
	for _, r := range hist {
		row := histRow{ID: r.ID, When: r.At.In(loc).Format(entryFmt), By: "You"}
		if !r.ByOwner {
			row.By = "The administrator"
		}
		for _, c := range r.Changes {
			row.Changes = append(row.Changes, histChange{fieldLabel(k.fields, c.Field), valueText(c.Field, c.Old, loc, labels), valueText(c.Field, c.New, loc, labels)})
		}
		p.History = append(p.History, row)
	}
	return p, true
}

func (s *Server) showHistory(q *rq, k histKind) {
	if p, ok := s.historyPage(q, k, pathID(q), ""); ok {
		q.render(http.StatusOK, "history", p)
	}
}

func (s *Server) revertEdit(q *rq, k histKind) {
	id, f := pathID(q), q.r.PostForm
	err := q.st.Update(q.r.Context(), true, func(tx *sql.Tx) error {
		_, _, e := k.revert(tx, s.now(), q.id.user.ID, q.id.user.ID, id, f.Get("rev"), f.Get("revision"))
		return e
	})
	if errors.Is(err, edit.ErrNoEntry) || errors.Is(err, edit.ErrNoHead) {
		q.notFound()
		return
	}
	if msg := formMessage(k.fields, err); msg != "" {
		if p, ok := s.historyPage(q, k, id, msg); ok {
			q.render(http.StatusBadRequest, "history", p)
		}
		return
	}
	if err != nil {
		q.serverError("revert edit", err)
		return
	}
	q.setFlash("entry-reverted")
	q.redirect(k.base + url.PathEscape(id) + "/history")
}

// getEntryHistory lists the edits of one of the person's own entries, newest first.
func (s *Server) getEntryHistory(q *rq) { s.showHistory(q, entryKind) }

// postRevertEntry sets back what one earlier edit changed, as a new edit.
func (s *Server) postRevertEntry(q *rq) { s.revertEdit(q, entryKind) }

// ---- partners ----

// getPartners lists the signed-in person's own partners, so they can be renamed. Only their rows.
func (s *Server) getPartners(q *rq) {
	var items []edit.Item
	err := q.st.View(q.r.Context(), func(tx *sql.Tx) (err error) {
		items, err = edit.Partners(tx, q.id.user.ID)
		return
	})
	if err != nil {
		q.serverError("list partners", err)
		return
	}
	p := &page{Title: "My partners", Active: "uploads"}
	n := 0
	for _, it := range items {
		name := it.Label
		if name == "" {
			n++
			name = "Anonymous partner " + strconv.Itoa(n)
		}
		p.PartnerRows = append(p.PartnerRows, partnerRow{ID: it.ID, Name: name})
	}
	q.render(http.StatusOK, "partners", p)
}

func (s *Server) getEditPartner(q *rq) {
	id := pathID(q)
	var cur map[string]json.RawMessage
	var rev string
	err := q.st.View(q.r.Context(), func(tx *sql.Tx) (err error) {
		cur, rev, err = edit.PartnerCurrent(tx, q.id.user.ID, id)
		return
	})
	switch {
	case errors.Is(err, edit.ErrNoEntry), errors.Is(err, edit.ErrNoHead):
		q.notFound()
		return
	case err != nil:
		q.serverError("read partner for edit", err)
		return
	}
	q.render(http.StatusOK, "editpartner", &page{Title: "Edit partner", Active: "uploads",
		PartnerForm: &partnerForm{ID: id, Rev: rev, Name: edit.Describe(cur["displayName"]), Note: edit.Describe(cur["note"])}})
}

func (s *Server) postEditPartner(q *rq) {
	id, f := pathID(q), q.r.PostForm
	in := map[string]string{"displayName": f.Get("name"), "note": strings.ReplaceAll(f.Get("note"), "\r\n", "\n")}
	err := q.st.Update(q.r.Context(), true, func(tx *sql.Tx) error {
		_, _, e := edit.PartnerApply(tx, s.now(), q.id.user.ID, q.id.user.ID, id, f.Get("rev"), in)
		return e
	})
	if errors.Is(err, edit.ErrNoEntry) || errors.Is(err, edit.ErrNoHead) {
		q.notFound()
		return
	}
	if msg := formMessage(edit.PartnerFields, err); msg != "" {
		q.render(http.StatusBadRequest, "editpartner", &page{Title: "Edit partner", Active: "uploads", Error: msg,
			PartnerForm: &partnerForm{ID: id, Rev: f.Get("rev"), Name: f.Get("name"), Note: f.Get("note")}})
		return
	}
	if err != nil {
		q.serverError("save partner edit", err)
		return
	}
	q.setFlash("partner-saved")
	q.redirect("/partners")
}

func (s *Server) getPartnerHistory(q *rq) { s.showHistory(q, partnKind) }
func (s *Server) postRevertPartner(q *rq) { s.revertEdit(q, partnKind) }
