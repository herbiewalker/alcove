package server

import (
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// firstEntry returns the id of one entry on a person's own list.
func firstEntry(t *testing.T, c *client) string {
	t.Helper()
	m := entryIDRE.FindStringSubmatch(c.do("GET", "/entries", nil).Body.String())
	if m == nil {
		t.Fatal("no entry on the list")
	}
	return m[1]
}

func (s *sharing) entryPage(c *client, ownerUser, id string) (int, string) {
	w := c.do("GET", "/spaces/"+s.space+"/entry/"+s.e.idOf(ownerUser)+"/"+url.PathEscape(id), nil)
	return w.Code, w.Body.String()
}

func TestOpenYourOwnEntry(t *testing.T) {
	s := newSharing(t)
	id := firstEntry(t, s.sam)
	w := s.sam.do("GET", "/entries/"+url.PathEscape(id), nil)
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, "Your entry.") {
		t.Fatalf("own entry: %d %.200s", w.Code, body)
	}
	// The owner sees what is private to them: partner names and catalog labels.
	if !strings.Contains(body, "Synthetic Partner") && !strings.Contains(body, "anonymous partner") {
		t.Error("the owner's own partners are not shown")
	}
	// The lists link to it.
	if !strings.Contains(s.sam.do("GET", "/entries", nil).Body.String(), `href="/entries/`+id) {
		t.Error("My entries does not link to the entry")
	}
	// Nobody else's, nothing made up, and not when signed out.
	if w := s.kim.do("GET", "/entries/"+url.PathEscape(id), nil); w.Code != 404 {
		t.Errorf("another person opened it: %d", w.Code)
	}
	if w := s.sam.do("GET", "/entries/does-not-exist", nil); w.Code != 404 {
		t.Errorf("a made-up id: %d", w.Code)
	}
	if w := s.e.client("192.168.1.99:1").do("GET", "/entries/"+url.PathEscape(id), nil); w.Code == 200 {
		t.Error("a signed-out visitor opened it")
	}
}

func TestOpenSomeoneElsesSharedEntry(t *testing.T) {
	s := newSharing(t)
	id := firstEntry(t, s.sam)
	code, body := s.entryPage(s.kim, "sam", id)
	if code != 200 || !strings.Contains(body, "Shared by Person sam") {
		t.Fatalf("shared entry: %d %.200s", code, body)
	}
	// The space list links to it.
	if !strings.Contains(s.kim.do("GET", "/spaces/"+s.space+"/entries", nil).Body.String(), "/entry/"+s.e.idOf("sam")+"/"+id) {
		t.Error("the shared list does not link to the entry")
	}
	// Partner names are hidden unless the owner shows them.
	if strings.Contains(body, "Synthetic Partner") || strings.Contains(body, "An anonymous partner") {
		t.Error("a partner is named to another member")
	}
	s.sam.post("/spaces/"+s.space, "/spaces/"+s.space+"/settings", url.Values{"notes": {"1"}, "partners": {"1"}})
	_, body = s.entryPage(s.kim, "sam", id)
	if !strings.Contains(body, "Synthetic Partner") && !strings.Contains(body, "An anonymous partner") {
		t.Error("the owner allowed partner names but they are not shown")
	}
	// Notes off: the page says so instead of showing the note.
	s.sam.post("/spaces/"+s.space, "/spaces/"+s.space+"/settings", url.Values{"partners": {"1"}})
	_, body = s.entryPage(s.kim, "sam", id)
	if strings.Contains(body, "Synthetic entry") || strings.Contains(body, `<p class="note">`) {
		t.Error("a note is shown although the owner left notes out")
	}
}

func TestSharedEntryIsClosedWhereTheListIsClosed(t *testing.T) {
	s := newSharing(t)
	id := firstEntry(t, s.sam)
	open := func() int { c, _ := s.entryPage(s.kim, "sam", id); return c }
	if open() != 200 {
		t.Fatal("setup: not open")
	}
	// Private.
	s.sam.post("/entries", "/entries/private", url.Values{"id": {id}, "private": {"1"}})
	if c := open(); c != 404 {
		t.Errorf("a private entry opened: %d", c)
	}
	s.sam.post("/entries", "/entries/private", url.Values{"id": {id}, "private": {"0"}})
	if open() != 200 {
		t.Fatal("unflagging did not reopen it")
	}
	// Paused.
	s.sam.post("/spaces/"+s.space, "/spaces/"+s.space+"/settings", url.Values{"paused": {"1"}})
	if c := open(); c != 404 {
		t.Errorf("a paused member's entry opened: %d", c)
	}
	s.sam.post("/spaces/"+s.space, "/spaces/"+s.space+"/settings", url.Values{})
	// An outsider, and the outsider's view of a missing entry, are the same page.
	c1, b1 := s.entryPage(s.lou, "sam", id)
	c2, b2 := s.entryPage(s.kim, "sam", "does-not-exist")
	c3, b3 := s.entryPage(s.kim, "lou", id) // a real id, wrong owner for this space
	strip := func(b string) string { return tokenValueRE.ReplaceAllString(b, "") }
	if c1 != 404 || c2 != 404 || c3 != 404 || strip(b1) != strip(b2) || strip(b2) != strip(b3) {
		t.Errorf("the refusals differ: %d %d %d", c1, c2, c3)
	}
	// Older wording sends the member to read again.
	s.e.exec(`UPDATE membership SET consent_version = 1 WHERE space_id = ? AND user_id = ?`, s.space, s.e.idOf("kim"))
	if w := s.kim.do("GET", "/spaces/"+s.space+"/entry/"+s.e.idOf("sam")+"/"+url.PathEscape(id), nil); w.Code != 303 {
		t.Errorf("older wording: %d", w.Code)
	}
	s.e.exec(`UPDATE membership SET consent_version = 2 WHERE space_id = ? AND user_id = ?`, s.space, s.e.idOf("kim"))
	// Leaving closes it at once.
	s.sam.post("/spaces/"+s.space, "/spaces/"+s.space+"/settings", url.Values{})
	s.kim.post("/spaces/"+s.space, "/spaces/"+s.space+"/leave", url.Values{})
	if c, _ := s.entryPage(s.kim, "sam", id); c != 404 {
		t.Errorf("a member who left opened an entry: %d", c)
	}
}

func TestCalendarDayListLinksToTheEntry(t *testing.T) {
	s := calSetup(t)
	_, body := s.cal(s.kim, "?month=2026-03&day=2026-03-10")
	for _, want := range []string{"/entry/" + s.e.idOf("sam") + "/s10", "/entry/" + s.e.idOf("kim") + "/k10"} {
		if !strings.Contains(body, want) {
			t.Errorf("the day list lacks a link %q", want)
		}
	}
	code, page := s.entryPage(s.kim, "sam", "s10")
	if code != 200 || !strings.Contains(page, "note-s10") || !strings.Contains(page, "Length") {
		t.Errorf("the linked entry: %d", code)
	}
}

var tokenValueRE = regexp.MustCompile(`name="csrf" value="[^"]*"`)
