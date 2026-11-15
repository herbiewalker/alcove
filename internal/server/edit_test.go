package server

import (
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// samEntry returns one of sam's own entry ids and its current start time in milliseconds.
func samEntry(t *testing.T, s *sharing) (id, startAt string) {
	t.Helper()
	var ids, starts []string
	if err := queryStrings(s.e, `SELECT rid, CAST(json_extract(data, '$.startAt') AS TEXT) FROM cache_row WHERE owner_id = ? AND tbl = 'encounters' ORDER BY pos LIMIT 1`, s.e.idOf("sam"), &ids, &starts); err != nil || len(ids) != 1 {
		t.Fatalf("sam's entry: %v %v", ids, err)
	}
	return ids[0], starts[0]
}

// cacheValue reads one JSON path of sam's cached entry ("NULL" when it is null or absent).
func cacheValue(t *testing.T, s *sharing, id, path string) string {
	t.Helper()
	var a, b []string
	if err := queryStrings(s.e, `SELECT CAST(COALESCE(json_extract(data, '`+path+`'), 'NULL') AS TEXT), rid FROM cache_row WHERE owner_id = ? AND tbl = 'encounters' AND rid = '`+id+`'`, s.e.idOf("sam"), &a, &b); err != nil {
		t.Fatal(err)
	}
	if len(a) == 0 {
		return ""
	}
	return a[0]
}

func changeCount(t *testing.T, s *sharing) int {
	t.Helper()
	var a, b []string
	if err := queryStrings(s.e, `SELECT CAST(COUNT(*) AS TEXT), ? FROM change`, "x", &a, &b); err != nil || len(a) != 1 {
		t.Fatal(err)
	}
	n, _ := strconv.Atoi(a[0])
	return n
}

func between(t *testing.T, body, start string) string {
	t.Helper()
	i := strings.Index(body, start)
	if i < 0 {
		t.Fatalf("%q not found in %.400s", start, body)
	}
	rest := body[i+len(start):]
	return rest[:strings.Index(rest, `"`)]
}

func formRev(t *testing.T, body string) string { return between(t, body, `name="rev" value="`) }
func extractWhen(t *testing.T, body string) string {
	return between(t, body, `name="when" type="datetime-local" value="`)
}

func TestAPersonEditsTheirOwnEntryAndTheSpaceSeesIt(t *testing.T) {
	s := newSharing(t)
	id, start := samEntry(t, s)
	edit := "/entries/" + url.PathEscape(id) + "/edit"
	form := s.sam.do("GET", edit, nil)
	if form.Code != 200 || !strings.Contains(form.Body.String(), `action="/entries/`+id+`/edit"`) {
		t.Fatalf("form: %d %.300s", form.Code, form.Body.String())
	}
	rev := formRev(t, form.Body.String())
	// Saving without touching the time keeps the exact stored start (seconds included).
	w := s.sam.post(edit, edit, url.Values{"rev": {rev}, "when": {extractWhen(t, form.Body.String())}, "rating": {"5"}, "duration": {"61"},
		"mood": {"TENDER"}, "initiator": {"MUTUAL"}, "protect": {"CONDOM", "PILL"}, "orgasms": {"2"}, "note": {"A brand new note\r\nsecond line"}})
	if w.Code != 303 || w.Header().Get("Location") != "/entries/"+id {
		t.Fatalf("save: %d %s %.300s", w.Code, w.Header().Get("Location"), w.Body.String())
	}
	if got := cacheValue(t, s, id, "$.startAt"); got != start {
		t.Errorf("an untouched time changed: %s -> %s", start, got)
	}
	page := s.sam.do("GET", "/entries/"+url.PathEscape(id), nil).Body.String()
	for _, want := range []string{"A brand new note", "5 of 5", "61 min", "Tender", "Condom", "Edit", "History"} {
		if !strings.Contains(page, want) {
			t.Errorf("the entry page lacks %q", want)
		}
	}
	// The space shows the edit at once, through the same gate.
	shared := s.kim.do("GET", "/spaces/"+s.space+"/entry/"+s.e.idOf("sam")+"/"+url.PathEscape(id), nil).Body.String()
	if !strings.Contains(shared, "A brand new note") || !strings.Contains(shared, "5 of 5") {
		t.Errorf("the space does not show the edit: %.600s", shared)
	}
	// The history shows what changed.
	hist := s.sam.do("GET", "/entries/"+url.PathEscape(id)+"/history", nil).Body.String()
	for _, want := range []string{"Rating", "Note", "A brand new note", "Undo this change"} {
		if !strings.Contains(hist, want) {
			t.Errorf("history lacks %q: %.800s", want, hist)
		}
	}
}

func TestOnlyTheOwnerCanOpenOrSaveAnEdit(t *testing.T) {
	s := newSharing(t)
	id, _ := samEntry(t, s)
	edit := "/entries/" + url.PathEscape(id) + "/edit"
	rev := formRev(t, s.sam.do("GET", edit, nil).Body.String())
	before := cacheValue(t, s, id, "$.note")
	for name, c := range map[string]*client{"another member": s.kim, "an outsider": s.lou} {
		if w := c.do("GET", edit, nil); w.Code != 404 {
			t.Errorf("%s opened the form: %d", name, w.Code)
		}
		if w := c.post("/entries", edit, url.Values{"rev": {rev}, "when": {"2026-01-01T10:00"}, "note": {"HIJACKED"}}); w.Code != 404 {
			t.Errorf("%s saved: %d", name, w.Code)
		}
		if w := c.do("GET", "/entries/"+url.PathEscape(id)+"/history", nil); w.Code != 404 {
			t.Errorf("%s read the history: %d", name, w.Code)
		}
		if w := c.post("/entries", "/entries/"+url.PathEscape(id)+"/revert", url.Values{"rev": {rev}, "revision": {"x"}}); w.Code != 404 {
			t.Errorf("%s reverted: %d", name, w.Code)
		}
	}
	if cacheValue(t, s, id, "$.note") != before {
		t.Error("another person changed the note")
	}
	// A save without the token is refused, and a signed-out visitor gets nothing.
	if w := s.sam.do("POST", edit, url.Values{"rev": {rev}, "note": {"NO-TOKEN"}}); w.Code == 303 || w.Code == 200 {
		t.Errorf("a save without the CSRF token: %d", w.Code)
	}
	if w := s.e.client("192.168.1.99:1").do("GET", edit, nil); w.Code == 200 {
		t.Error("a signed-out visitor opened the form")
	}
	if n := changeCount(t, s); n != 0 {
		t.Errorf("%d changes recorded by refused requests", n)
	}
}

func TestABadOrStaleSaveChangesNothingAndSaysWhy(t *testing.T) {
	s := newSharing(t)
	id, _ := samEntry(t, s)
	edit := "/entries/" + url.PathEscape(id) + "/edit"
	form := s.sam.do("GET", edit, nil).Body.String()
	rev, when := formRev(t, form), extractWhen(t, form)
	bad := s.sam.post(edit, edit, url.Values{"rev": {rev}, "when": {when}, "rating": {"9"}, "note": {"MY-TYPED-NOTE"}})
	body := bad.Body.String()
	if bad.Code != 400 || !strings.Contains(body, "Rating must be 1 to 5.") || !strings.Contains(body, "MY-TYPED-NOTE") {
		t.Fatalf("bad rating: %d %.500s", bad.Code, body)
	}
	if w := s.sam.post(edit, edit, url.Values{"rev": {rev}, "when": {"not a time"}, "note": {"x"}}); w.Code != 400 || !strings.Contains(w.Body.String(), "When must be a real date and time.") {
		t.Errorf("bad time: %d", w.Code)
	}
	if w := s.sam.post(edit, edit, url.Values{"rev": {rev}, "when": {when}, "mood": {"SPLENDID"}}); w.Code != 400 || strings.Contains(w.Body.String(), "SPLENDID is") {
		t.Errorf("bad mood: %d", w.Code)
	}
	if changeCount(t, s) != 0 {
		t.Fatal("a refused save recorded a change")
	}
	// A save from a form opened before another save is refused.
	if w := s.sam.post(edit, edit, url.Values{"rev": {rev}, "when": {when}, "note": {"FIRST"}}); w.Code != 303 {
		t.Fatalf("first save: %d %.300s", w.Code, w.Body.String())
	}
	w := s.sam.post(edit, edit, url.Values{"rev": {rev}, "when": {when}, "note": {"SECOND"}})
	if w.Code != 400 || !strings.Contains(w.Body.String(), "changed after you opened it") {
		t.Fatalf("stale save: %d %.400s", w.Code, w.Body.String())
	}
	if got := cacheValue(t, s, id, "$.note"); got != "FIRST" {
		t.Errorf("the stale save changed the note to %q", got)
	}
}

func TestUndoPutsAnEditBackAndKeepsTheHistory(t *testing.T) {
	s := newSharing(t)
	id, _ := samEntry(t, s)
	edit := "/entries/" + url.PathEscape(id) + "/edit"
	before := cacheValue(t, s, id, "$.durationMin")
	form := s.sam.do("GET", edit, nil).Body.String()
	if w := s.sam.post(edit, edit, url.Values{"rev": {formRev(t, form)}, "when": {extractWhen(t, form)}, "duration": {"123"}}); w.Code != 303 {
		t.Fatalf("save: %d", w.Code)
	}
	if cacheValue(t, s, id, "$.durationMin") != "123" {
		t.Fatal("the edit did not apply")
	}
	hist := "/entries/" + url.PathEscape(id) + "/history"
	page := s.sam.do("GET", hist, nil).Body.String()
	revision := between(t, page, `name="revision" value="`)
	if w := s.sam.post(hist, "/entries/"+url.PathEscape(id)+"/revert", url.Values{"rev": {between(t, page, `name="rev" value="`)}, "revision": {revision}}); w.Code != 303 {
		t.Fatalf("undo: %d %.300s", w.Code, w.Body.String())
	}
	if after := cacheValue(t, s, id, "$.durationMin"); after != before {
		t.Errorf("duration after undo: %s, want %s", after, before)
	}
	if n := strings.Count(s.sam.do("GET", hist, nil).Body.String(), "Undo this change"); n != 2 {
		t.Errorf("the history lists %d edits, want 2", n)
	}
}

func TestTheUploadPageWarnsOnlyWhenThereAreEdits(t *testing.T) {
	s := newSharing(t)
	if strings.Contains(s.sam.do("GET", "/uploads", nil).Body.String(), "does not carry those edits over") {
		t.Error("a warning with nothing edited")
	}
	id, _ := samEntry(t, s)
	edit := "/entries/" + url.PathEscape(id) + "/edit"
	form := s.sam.do("GET", edit, nil).Body.String()
	if w := s.sam.post(edit, edit, url.Values{"rev": {formRev(t, form)}, "when": {extractWhen(t, form)}, "note": {"edited"}}); w.Code != 303 {
		t.Fatalf("save: %d", w.Code)
	}
	if !strings.Contains(s.sam.do("GET", "/uploads", nil).Body.String(), "does not carry those edits over") {
		t.Error("no warning after an edit")
	}
	if strings.Contains(s.kim.do("GET", "/uploads", nil).Body.String(), "does not carry those edits over") {
		t.Error("another person's edit warns someone else")
	}
}
