package server

import (
	"net/url"
	"strings"
	"testing"
)

// setNote gives one of a person's entries a known note, in the cache and in the search index.
func (s *sharing) setNote(user, rid, text string) {
	s.e.t.Helper()
	uid := s.e.idOf(user)
	s.e.exec(`UPDATE cache_row SET data = json_set(data, '$.note', ?) WHERE owner_id = ? AND tbl = 'encounters' AND rid = ?`, text, uid, rid)
	s.e.exec(`DELETE FROM search WHERE owner_id = ? AND kind = 'encounter' AND ref = ?`, uid, rid)
	s.e.exec(`INSERT INTO search(owner_id, kind, ref, body) VALUES (?,?,?,?)`, uid, "encounter", rid, text)
}

func (s *sharing) search(c *client, scope, term string) (int, string) {
	w := c.post("/search", "/search", url.Values{"q": {term}, "scope": {scope}})
	return w.Code, w.Body.String()
}

func TestSearchOwnEntries(t *testing.T) {
	s := newSharing(t)
	id := firstEntry(t, s.sam)
	s.setNote("sam", id, "lighthouse dinner <b>loud</b>")
	code, body := s.search(s.sam, "me", "lighthouse")
	if code != 200 || !strings.Contains(body, "1 entry found") || !strings.Contains(body, `href="/entries/`+id+`"`) {
		t.Fatalf("own search: %d %.300s", code, body)
	}
	if !strings.Contains(body, "<mark>lighthouse</mark>") {
		t.Error("the match is not highlighted")
	}
	// Text from a note is escaped, never markup.
	if strings.Contains(body, "<b>loud</b>") || !strings.Contains(body, "&lt;b&gt;loud&lt;/b&gt;") {
		t.Error("a note's markup reached the page")
	}
	// Kim does not find Sam's note in her own data; an empty result says so.
	if _, b := s.search(s.kim, "me", "lighthouse"); !strings.Contains(b, "Nothing found") {
		t.Error("another person's note was found in their own search")
	}
	// The search page lists where one can search, and the term is never read from an address.
	page := s.sam.do("GET", "/search", nil).Body.String()
	if !strings.Contains(page, "My own entries") || !strings.Contains(page, "Shared in Ours") {
		t.Error("the scopes are not listed")
	}
	if b := s.sam.do("GET", "/search?q=lighthouse&scope=me", nil).Body.String(); strings.Contains(b, "entry found") || strings.Contains(b, "lighthouse dinner") {
		t.Error("a search ran from the address")
	}
}

func TestSearchASpaceFindsOnlyWhatItShows(t *testing.T) {
	s := newSharing(t)
	id := firstEntry(t, s.sam)
	s.setNote("sam", id, "picnic by the quarry")
	link := `href="/spaces/` + s.space + `/entry/` + s.e.idOf("sam") + `/` + id + `"`
	found := func() bool {
		_, b := s.search(s.kim, s.space, "quarry")
		return strings.Contains(b, link)
	}
	if !found() {
		t.Fatal("a shared note is not found by a member")
	}
	// Notes left out, private, paused: not found, and no hint it exists.
	s.sam.post("/spaces/"+s.space, "/spaces/"+s.space+"/settings", url.Values{"partners": {"1"}})
	if found() {
		t.Error("a note is found although its owner keeps notes out")
	}
	s.sam.post("/spaces/"+s.space, "/spaces/"+s.space+"/settings", url.Values{"notes": {"1"}})
	if !found() {
		t.Fatal("notes back on, still not found")
	}
	s.sam.post("/entries", "/entries/private", url.Values{"id": {id}, "private": {"1"}})
	if found() {
		t.Error("a private entry is found")
	}
	s.sam.post("/entries", "/entries/private", url.Values{"id": {id}, "private": {"0"}})
	s.sam.post("/spaces/"+s.space, "/spaces/"+s.space+"/settings", url.Values{"notes": {"1"}, "paused": {"1"}})
	if found() {
		t.Error("a paused member's note is found")
	}
	s.sam.post("/spaces/"+s.space, "/spaces/"+s.space+"/settings", url.Values{"notes": {"1"}})
	// The owner can find their own note in the space too.
	if _, b := s.search(s.sam, s.space, "quarry"); !strings.Contains(b, link) {
		t.Error("the owner does not find their own shared note")
	}
	// Leaving closes it.
	s.kim.post("/spaces/"+s.space, "/spaces/"+s.space+"/leave", url.Values{})
	if code, _ := s.search(s.kim, s.space, "quarry"); code != 404 {
		t.Errorf("a member who left searched the space: %d", code)
	}
}

func TestSearchRefusesAScopeThatIsNotYours(t *testing.T) {
	s := newSharing(t)
	id := firstEntry(t, s.sam)
	s.setNote("sam", id, "secret picnic")
	c1, b1 := s.search(s.lou, s.space, "picnic") // a real space, not Lou's
	c2, b2 := s.search(s.lou, "sp00000000000000000000000000000000", "picnic")
	c3, b3 := s.search(s.lou, s.otherSp, "picnic") // his own space: allowed, finds nothing
	if c1 != 404 || c2 != 404 || strings.Contains(b1, "picnic by") || tokenValueRE.ReplaceAllString(b1, "") != tokenValueRE.ReplaceAllString(b2, "") {
		t.Errorf("a foreign space looks different from a missing one: %d %d", c1, c2)
	}
	if c3 != 200 || strings.Contains(b3, "secret picnic") {
		t.Errorf("own space: %d", c3)
	}
	if w := s.e.client("192.168.1.99:1").do("GET", "/search", nil); w.Code == 200 {
		t.Error("a signed-out visitor opened the search page")
	}
}

func TestSearchRefusesTooShortAndTooLongAndKeepsTermsOutOfTheLog(t *testing.T) {
	s := newSharing(t)
	if code, b := s.search(s.sam, "me", "a"); code != 400 || !strings.Contains(b, "at least two") {
		t.Errorf("one character: %d", code)
	}
	if code, _ := s.search(s.sam, "me", strings.Repeat("x", 200)); code != 400 {
		t.Errorf("too long: %d", code)
	}
	for _, hostile := range []string{`"`, `a" OR "b`, `body:x`, "\x00ab", `NEAR(`, `*`} {
		if code, _ := s.search(s.sam, "me", hostile); code != 200 && code != 400 {
			t.Errorf("%q: %d", hostile, code)
		}
	}
	s.search(s.sam, "me", "uniquetermforthelog")
	s.search(s.sam, s.space, "uniquetermforthelog")
	if strings.Contains(s.e.logs.String(), "uniquetermforthelog") {
		t.Error("a search term reached the log")
	}
}
