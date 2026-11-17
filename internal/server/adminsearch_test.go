package server

import (
	"net/url"
	"strings"
	"testing"
)

func (s *sharing) adminSearch(term string) (int, string) {
	w := s.admin.post("/admin/search", "/admin/search", url.Values{"q": {term}})
	return w.Code, w.Body.String()
}

// The administrator finds notes whatever the owner shares, and the search is logged without its words.
func TestAdminSearchFindsEverythingAndLogsOnlyTheShape(t *testing.T) {
	s := newSharing(t)
	sam, kim := firstEntry(t, s.sam), firstEntry(t, s.kim)
	s.setNote("sam", sam, "unicorn <i>festival</i> at the quarry")
	s.setNote("kim", kim, "quarry picnic")

	code, body := s.adminSearch("quarry")
	if code != 200 || !strings.Contains(body, "2 entries found") {
		t.Fatalf("admin search: %d %.400s", code, body)
	}
	for _, want := range []string{
		`href="/admin/people/` + s.e.idOf("sam") + `/entries/` + sam + `"`,
		`href="/admin/people/` + s.e.idOf("kim") + `/entries/` + kim + `"`,
		"<mark>quarry</mark>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if strings.Contains(body, "<i>festival</i>") {
		t.Error("a note's markup reached the page")
	}

	// Sharing choices do not hide anything from the administrator: private, paused, notes left out.
	s.sam.post("/entries", "/entries/private", url.Values{"id": {sam}, "private": {"1"}})
	s.sam.post("/spaces/"+s.space, "/spaces/"+s.space+"/settings", url.Values{"partners": {"1"}, "paused": {"1"}})
	if _, b := s.search(s.kim, s.space, "unicorn"); !strings.Contains(b, "Nothing found") {
		t.Fatal("setup: the member can still see the note")
	}
	if _, b := s.adminSearch("unicorn"); !strings.Contains(b, "1 entry found") {
		t.Error("the administrator cannot find a private, paused note")
	}

	// A person who deleted the entry is not found here (the delete removes it from the index).
	s.sam.post("/entries/"+sam+"/delete", "/entries/"+sam+"/delete", url.Values{"confirm": {"1"}})
	del := "/entries/" + sam + "/delete"
	page := s.sam.do("GET", del, nil)
	if w := s.sam.post(del, del, url.Values{"rev": {formRev(t, page.Body.String())}}); w.Code != 303 {
		t.Fatalf("setup: delete: %d", w.Code)
	}
	if _, b := s.adminSearch("unicorn"); !strings.Contains(b, "Nothing found") {
		t.Error("a deleted entry is still found (the delete should have removed it from the index)")
	}

	// The log holds the shape of the search and never the words (constraint 8).
	if n := logCount(t, s, "admin.search"); n < 3 {
		t.Fatalf("searches logged: %d", n)
	}
	for _, e := range logRows(t, s) {
		if e.Action != "admin.search" {
			continue
		}
		if !strings.HasPrefix(e.Fields, "len:") || !strings.Contains(e.Fields, ",hits:") {
			t.Errorf("search log fields: %q", e.Fields)
		}
		if strings.Contains(e.Fields+e.TargetID+e.TargetType, "quarry") || strings.Contains(e.Fields+e.TargetID, "unicorn") {
			t.Errorf("the log carries the search words: %+v", e)
		}
	}
	if strings.Contains(s.e.logs.String(), "unicorn") || strings.Contains(s.e.logs.String(), "quarry") {
		t.Error("the server log carries search words")
	}
}

func TestAdminSearchIsAdminOnlyPostOnlyAndRefusesBadTerms(t *testing.T) {
	s := newSharing(t)
	s.setNote("sam", firstEntry(t, s.sam), "secret lighthouse")
	if w := s.sam.post("/search", "/admin/search", url.Values{"q": {"lighthouse"}}); w.Code == 200 || strings.Contains(w.Body.String(), "lighthouse") {
		t.Errorf("a member ran the admin search: %d", w.Code)
	}
	out := s.e.client("192.168.1.99:1")
	for name, w := range map[string]int{"member page": s.sam.do("GET", "/admin/search", nil).Code, "signed-out page": out.do("GET", "/admin/search", nil).Code, "signed-out post": out.do("POST", "/admin/search", url.Values{"q": {"lighthouse"}}).Code} {
		if w == 200 {
			t.Errorf("%s was allowed", name)
		}
	}
	// The words are never read from the address.
	if b := s.admin.do("GET", "/admin/search?q=lighthouse", nil).Body.String(); strings.Contains(b, "found") || strings.Contains(b, "secret lighthouse") {
		t.Error("a search ran from the address")
	}
	before := logCount(t, s, "admin.search")
	for _, term := range []string{"a", "", strings.Repeat("x", 101)} {
		if code, _ := s.adminSearch(term); code != 400 {
			t.Errorf("term of %d characters: %d", len(term), code)
		}
	}
	if logCount(t, s, "admin.search") != before {
		t.Error("a refused term was logged as a search")
	}
	// Hostile input never reaches the engine as syntax.
	for _, term := range []string{`" OR 1=1 --`, `body : *`, `NEAR(a b)`, `x'; DROP TABLE search;--`, "a\x00b", `"unterminated`} {
		if code, _ := s.adminSearch(term); code != 200 && code != 400 {
			t.Errorf("hostile term %q: %d", term, code)
		}
	}
	if _, b := s.adminSearch("lighthouse"); !strings.Contains(b, "1 entry found") {
		t.Error("the index was damaged by hostile input")
	}
}
