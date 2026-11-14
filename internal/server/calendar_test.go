package server

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

var bothCellRE = regexp.MustCompile(`<td class="[^"]*both`)

// calSetup replaces the synthetic entries with known days: sam on the 10th and 15th, kim on the 10th and 20th.
func calSetup(t *testing.T) *sharing {
	t.Helper()
	s := newSharing(t)
	s.e.exec(`DELETE FROM cache_row WHERE tbl = 'encounters'`)
	add := func(user, id string, day, hour int) {
		at := time.Date(2026, 3, day, hour, 0, 0, 0, time.UTC).UnixMilli()
		s.e.exec(`INSERT INTO cache_row(owner_id, tbl, rid, pos, data) VALUES (?,?,?,0,?)`, s.e.idOf(user), "encounters", id,
			fmt.Sprintf(`{"id":%q,"startAt":%d,"durationMin":30,"satisfactionRating":4,"note":"note-%s"}`, id, at, id))
	}
	add("sam", "s10", 10, 12)
	add("sam", "s15", 15, 9)
	add("kim", "k10", 10, 20)
	add("kim", "k20", 20, 23)
	return s
}

func (s *sharing) cal(c *client, query string) (int, string) {
	w := c.do("GET", "/spaces/"+s.space+"/calendar"+query, nil)
	return w.Code, w.Body.String()
}

func TestCalendarShowsEveryMembersDaysAndHighlightsOverlap(t *testing.T) {
	s := calSetup(t)
	code, body := s.cal(s.kim, "?month=2026-03")
	if code != 200 || !strings.Contains(body, "March 2026") {
		t.Fatalf("%d", code)
	}
	if n := len(bothCellRE.FindAllString(body, -1)); n != 1 {
		t.Errorf("%d highlighted days, want 1 (the 10th)", n)
	}
	for _, want := range []string{"Person sam", "Person kim", `aria-label="Tuesday 10 March`, "several members active"} {
		if !strings.Contains(body, want) {
			t.Errorf("the calendar lacks %q", want)
		}
	}
	// 4 entries on 3 distinct days with marks (10th two members, 15th, 20th): 4 markers in all.
	if n := strings.Count(body, `class="mk m`) - 2; n != 4 { // minus the legend's two
		t.Errorf("%d markers, want 4", n)
	}
	// Choosing a day lists that day's entries from everyone, with notes.
	_, body = s.cal(s.sam, "?month=2026-03&day=2026-03-10")
	if strings.Count(body, "<tr><td>2026-03-10") != 2 || !strings.Contains(body, "note-s10") || !strings.Contains(body, "note-k10") {
		t.Errorf("the 10th should list both entries")
	}
	_, body = s.cal(s.sam, "?day=2026-03-15") // the month follows the day
	if !strings.Contains(body, "March 2026") || strings.Count(body, "<tr><td>2026-03-15") != 1 || strings.Contains(body, "note-k10") {
		t.Error("the 15th should list only sam's entry")
	}
	// An empty month and an empty day say so.
	_, body = s.cal(s.sam, "?month=2024-01")
	if !strings.Contains(body, "Nothing is shown for this month") {
		t.Error("an empty month should say so")
	}
	_, body = s.cal(s.sam, "?month=2026-03&day=2026-03-11")
	if !strings.Contains(body, "No shown entries on this day") {
		t.Error("an empty day should say so")
	}
}

func TestCalendarFollowsTheSameRulesAsEntries(t *testing.T) {
	s := calSetup(t)
	// A private entry leaves the calendar.
	if w := s.sam.post("/entries", "/entries/private", url.Values{"id": {"s15"}, "private": {"1"}}); w.Code != 303 {
		t.Fatalf("private: %d", w.Code)
	}
	_, body := s.cal(s.kim, "?month=2026-03&day=2026-03-15")
	if strings.Contains(body, "note-s15") || !strings.Contains(body, "No shown entries on this day") {
		t.Error("a private entry is on the calendar")
	}
	s.sam.post("/entries", "/entries/private", url.Values{"id": {"s15"}, "private": {"0"}})
	// Pausing removes a member's days and the overlap.
	s.kim.post("/spaces/"+s.space, "/spaces/"+s.space+"/settings", url.Values{"paused": {"1"}})
	_, body = s.cal(s.sam, "?month=2026-03&day=2026-03-10")
	if n := len(bothCellRE.FindAllString(body, -1)); n != 0 || strings.Contains(body, "note-k10") || strings.Contains(body, "note-k20") {
		t.Errorf("a paused member is still on the calendar (%d highlighted)", n)
	}
	if !strings.Contains(body, "note-s10") {
		t.Error("sam's own entry should remain")
	}
	s.kim.post("/spaces/"+s.space, "/spaces/"+s.space+"/settings", url.Values{})
	// Leaving removes everything of kim's at once.
	s.kim.post("/spaces/"+s.space, "/spaces/"+s.space+"/leave", url.Values{})
	_, body = s.cal(s.sam, "?month=2026-03&day=2026-03-10")
	if strings.Contains(body, "note-k10") || strings.Contains(body, "Person kim") {
		t.Error("a member who left is still on the calendar")
	}
}

func TestCalendarIsClosedToOutsidersAndStaleConsent(t *testing.T) {
	s := calSetup(t)
	code, body := s.cal(s.lou, "?month=2026-03")
	if code != 404 || strings.Contains(body, "Person sam") || strings.Contains(body, "March") {
		t.Errorf("outsider: %d", code)
	}
	w1 := s.lou.do("GET", "/spaces/"+s.space+"/calendar", nil)
	w2 := s.lou.do("GET", "/spaces/sp00000000000000000000000000000000/calendar", nil)
	if w1.Code != w2.Code || w1.Body.String() != w2.Body.String() {
		t.Error("an outsider can tell a real space from a missing one")
	}
	if w := s.e.client("192.168.1.99:1").do("GET", "/spaces/"+s.space+"/calendar", nil); w.Code == 200 {
		t.Error("a signed-out visitor got the calendar")
	}
	s.e.exec(`UPDATE membership SET consent_version = 1 WHERE space_id = ? AND user_id = ?`, s.space, s.e.idOf("kim"))
	if w := s.kim.do("GET", "/spaces/"+s.space+"/calendar?month=2026-03", nil); w.Code != 303 {
		t.Errorf("a member on older wording got %d", w.Code)
	}
	// And sam no longer sees kim on it.
	_, body = s.cal(s.sam, "?month=2026-03&day=2026-03-10")
	if strings.Contains(body, "note-k10") {
		t.Error("kim is shown although she has not agreed to the current wording")
	}
}

func TestCalendarSurvivesOddInput(t *testing.T) {
	s := calSetup(t)
	for _, q := range []string{"?month=zzz", "?month=9999-01", "?month=1800-01", "?day=2026-02-31", "?day=garbage", "?month=2026-3", "?month=2026-03&day=1800-01-01",
		"?month=" + strings.Repeat("9", 5000), "?day=%00"} {
		if code, _ := s.cal(s.sam, q); code != 200 {
			t.Errorf("%.40s: %d", q, code)
		}
	}
	// Month navigation links stay in range and well formed.
	_, body := s.cal(s.sam, "?month=2026-12")
	if !strings.Contains(body, "month=2027-01") || !strings.Contains(body, "month=2026-11") {
		t.Error("the month links are wrong around the new year")
	}
}

func TestCalendarPageEscapesNames(t *testing.T) {
	s := calSetup(t)
	s.sam.post("/profile", "/profile", url.Values{"name": {"<script>x</script>"}})
	_, body := s.cal(s.kim, "?month=2026-03&day=2026-03-10")
	if strings.Contains(body, "<script>x") || !strings.Contains(body, "&lt;script&gt;") {
		t.Error("a name was not escaped on the calendar")
	}
}

func TestCalendarGridShowsNeighbouringDaysOfAdjacentMonths(t *testing.T) {
	s := calSetup(t)
	at := func(m time.Month, d int) int64 { return time.Date(2026, m, d, 8, 0, 0, 0, time.UTC).UnixMilli() }
	for id, ms := range map[string]int64{"edge1": at(time.February, 28), "edge2": at(time.April, 1)} {
		s.e.exec(`INSERT INTO cache_row(owner_id, tbl, rid, pos, data) VALUES (?,?,?,0,?)`, s.e.idOf("sam"), "encounters", id, fmt.Sprintf(`{"id":%q,"startAt":%d}`, id, ms))
	}
	_, body := s.cal(s.kim, "?month=2026-03")
	for _, want := range []string{`aria-label="Saturday 28 February, Person sam 1`, `aria-label="Wednesday 1 April, Person sam 1`} {
		if !strings.Contains(body, want) {
			t.Errorf("the grid lacks %q", want)
		}
	}
}
