package server

import (
	"net/url"
	"strings"
	"testing"
)

func (s *sharing) compare(c *client, query string) (int, string) {
	w := c.do("GET", "/spaces/"+s.space+"/compare"+query, nil)
	return w.Code, w.Body.String()
}

// cell returns the table cells of the row with this label on the compare page, as plain text.
func cells(body, label string) []string {
	i := strings.Index(body, `<th scope="row">`+label+`</th>`)
	if i < 0 {
		return nil
	}
	rest := body[i:]
	if j := strings.Index(rest, "</tr>"); j >= 0 {
		rest = rest[:j]
	}
	var out []string
	for _, part := range strings.Split(rest, "<td>")[1:] {
		part = part[:strings.Index(part, "</td>")]
		if k := strings.Index(part, "<meter"); k >= 0 {
			part = part[:k]
		}
		out = append(out, strings.TrimSpace(part))
	}
	return out
}

func TestCompareShowsEachMembersNumbersAndNothingElse(t *testing.T) {
	s := calSetup(t)
	code, body := s.compare(s.kim, "?period=all")
	if code != 200 {
		t.Fatalf("%d", code)
	}
	for _, want := range []string{"At a glance", "Rhythm", "Trend", "Variety", "Person sam", "Person kim (you)"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if got := cells(body, "Entries"); len(got) != 2 || got[0] != "2" || got[1] != "2" {
		t.Errorf("entries row: %v", got)
	}
	// Sam: the 10th and 15th (Tuesday, Sunday); kim: the 10th and 20th. Both in March.
	if got := cells(body, "Busiest month"); len(got) != 2 || got[0] != "March" || got[1] != "March" {
		t.Errorf("busiest month: %v", got)
	}
	if got := cells(body, "2026"); len(got) != 2 || got[0] != "2" {
		t.Errorf("year trend: %v", got)
	}
	for _, leak := range []string{"note-s10", "note-k10", "note-s15"} {
		if strings.Contains(body, leak) {
			t.Errorf("a note leaked into the compare page: %s", leak)
		}
	}
	// A strange period falls back to the default and still renders.
	if code, _ := s.compare(s.kim, "?period=%00zzz"); code != 200 {
		t.Errorf("odd period: %d", code)
	}
}

func TestCompareFollowsTheSameRulesAsEntries(t *testing.T) {
	s := calSetup(t)
	// A private entry is not counted.
	s.sam.post("/entries", "/entries/private", url.Values{"id": {"s15"}, "private": {"1"}})
	_, body := s.compare(s.kim, "?period=all")
	if got := cells(body, "Entries"); got[0] != "1" || got[1] != "2" {
		t.Errorf("a private entry was counted: %v", got)
	}
	s.sam.post("/entries", "/entries/private", url.Values{"id": {"s15"}, "private": {"0"}})
	// A paused member leaves the table altogether.
	s.kim.post("/spaces/"+s.space, "/spaces/"+s.space+"/settings", url.Values{"paused": {"1"}})
	_, body = s.compare(s.sam, "?period=all")
	if strings.Contains(body, "Person kim") {
		t.Error("a paused member is still compared")
	}
	if got := cells(body, "Entries"); len(got) != 1 || got[0] != "2" {
		t.Errorf("entries with kim paused: %v", got)
	}
	s.kim.post("/spaces/"+s.space, "/spaces/"+s.space+"/settings", url.Values{})
	// A member whose backup is from a newer Tryst is named and left out, not shown wrongly.
	s.e.exec(`UPDATE cache_head SET tryst_schema = 99 WHERE owner_id = ?`, s.e.idOf("kim"))
	_, body = s.compare(s.sam, "?period=all")
	if !strings.Contains(body, "newer Tryst version") || got(cells(body, "Entries")) != "2" {
		t.Errorf("newer backup: %v", cells(body, "Entries"))
	}
	s.e.exec(`UPDATE cache_head SET tryst_schema = 19 WHERE owner_id = ?`, s.e.idOf("kim"))
	// Leaving removes the person at once.
	s.kim.post("/spaces/"+s.space, "/spaces/"+s.space+"/leave", url.Values{})
	_, body = s.compare(s.sam, "?period=all")
	if strings.Contains(body, "Person kim") {
		t.Error("a member who left is still compared")
	}
}

func got(c []string) string { return strings.Join(c, ",") }

func TestCompareIsClosedToOutsidersAndStaleConsent(t *testing.T) {
	s := calSetup(t)
	code, body := s.compare(s.lou, "?period=all")
	if code != 404 || strings.Contains(body, "Person sam") {
		t.Errorf("outsider: %d", code)
	}
	w1 := s.lou.do("GET", "/spaces/"+s.space+"/compare", nil)
	w2 := s.lou.do("GET", "/spaces/sp00000000000000000000000000000000/compare", nil)
	if w1.Code != w2.Code || w1.Body.String() != w2.Body.String() {
		t.Error("an outsider can tell a real space from a missing one")
	}
	if w := s.e.client("192.168.1.99:1").do("GET", "/spaces/"+s.space+"/compare", nil); w.Code == 200 {
		t.Error("a signed-out visitor got the page")
	}
	s.e.exec(`UPDATE membership SET consent_version = 1 WHERE space_id = ? AND user_id = ?`, s.space, s.e.idOf("kim"))
	if w := s.kim.do("GET", "/spaces/"+s.space+"/compare", nil); w.Code != 303 {
		t.Errorf("older wording: %d", w.Code)
	}
	_, body = s.compare(s.sam, "?period=all")
	if strings.Contains(body, "Person kim") {
		t.Error("a member on older wording is compared")
	}
}

func TestTimeZoneMovesAnEntryToItsOwnersDay(t *testing.T) {
	s := calSetup(t)
	// Sam's entry is 12:00 UTC on 10 March: it is 01:00 on the 11th in Auckland (UTC+13 in March).
	_, body := s.cal(s.kim, "?month=2026-03")
	if n := len(bothCellRE.FindAllString(body, -1)); n != 1 {
		t.Fatalf("setup: %d highlighted days", n)
	}
	if w := s.sam.post("/profile", "/profile", url.Values{"name": {"Person sam"}, "tz": {"Pacific/Auckland"}}); w.Code != 303 {
		t.Fatalf("set the zone: %d", w.Code)
	}
	_, body = s.cal(s.kim, "?month=2026-03")
	if n := len(bothCellRE.FindAllString(body, -1)); n != 0 {
		t.Errorf("the overlap should be gone once sam's entry moves to the 11th (%d highlighted)", n)
	}
	for _, want := range []string{`aria-label="Wednesday 11 March, Person sam 1`} {
		if !strings.Contains(body, want) {
			t.Errorf("the 11th lacks sam: %q", want)
		}
	}
	_, body = s.cal(s.kim, "?month=2026-03&day=2026-03-11")
	if !strings.Contains(body, "Mar 11, 2026, 1:00 AM") || !strings.Contains(body, "note-s10") {
		t.Error("the 11th should list sam's entry at his local time")
	}
	_, body = s.cal(s.kim, "?month=2026-03&day=2026-03-10")
	if strings.Contains(body, "note-s10") || !strings.Contains(body, "note-k10") {
		t.Error("the 10th should list only kim's entry now")
	}
	// His own list shows his local time too.
	if !strings.Contains(s.sam.do("GET", "/entries", nil).Body.String(), "Mar 11, 2026, 1:00 AM") {
		t.Error("My entries does not use the person's own zone")
	}
	// A bad zone is refused and nothing changes; an empty one goes back to the default.
	for _, bad := range []string{"Mars/Olympus", "../../etc/passwd", "UTC+5", strings.Repeat("a", 80)} {
		if w := s.sam.post("/profile", "/profile", url.Values{"name": {"Person sam"}, "tz": {bad}}); w.Code != 400 {
			t.Errorf("%q: %d, want 400", bad, w.Code)
		}
	}
	if n := s.e.count(`SELECT COUNT(*) FROM user_tz WHERE tz = 'Pacific/Auckland'`); n != 1 {
		t.Errorf("a refused save changed the zone (%d)", n)
	}
	s.sam.post("/profile", "/profile", url.Values{"name": {"Person sam"}})
	if n := s.e.count(`SELECT COUNT(*) FROM user_tz`); n != 0 {
		t.Errorf("an empty zone should remove the choice (%d rows)", n)
	}
}
