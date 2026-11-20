package server

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// F25 / D-51: off by default, switched on by an administrator, then a member sees the lines about
// them (kind, time, field names) and nothing else.
func TestMemberActivityPageIsOffUntilAnAdministratorTurnsItOn(t *testing.T) {
	s := newSharing(t)
	id, _ := samEntry(t, s)

	if w := s.sam.do("GET", "/account/activity", nil); w.Code != 404 {
		t.Fatalf("the page exists while the switch is off: %d", w.Code)
	}
	if strings.Contains(s.sam.do("GET", "/account", nil).Body.String(), "/account/activity") {
		t.Error("the Account page links to a page that is off")
	}
	if w := s.e.client("192.168.1.99:1").do("GET", "/account/activity", nil); w.Code == 200 {
		t.Errorf("a signed-out visitor got the page: %d", w.Code)
	}
	// Only an administrator can flip it.
	if w := s.sam.post("/account", "/admin/settings/activity", url.Values{"activity": {"1"}}); w.Code != 404 {
		t.Errorf("a member flipped the switch: %d", w.Code)
	}

	// The administrator edits Sam's entry (which opens the edit page first).
	edit := samBase(s) + "/entries/" + url.PathEscape(id) + "/edit"
	form := s.admin.do("GET", edit, nil).Body.String()
	const secret = "ACTIVITY-PAGE-MUST-NOT-SHOW-THIS"
	if w := s.admin.post(edit, edit, url.Values{"rev": {formRev(t, form)}, "when": {extractWhen(t, form)}, "rating": {"2"}, "note": {secret}}); w.Code != 303 {
		t.Fatalf("admin edit: %d", w.Code)
	}

	if w := s.admin.post("/admin/settings", "/admin/settings/activity", url.Values{"activity": {"1"}}); w.Code != 303 {
		t.Fatalf("turning it on: %d", w.Code)
	}
	if logCount(t, s, "settings.activity") != 1 {
		t.Error("the switch change is not in the admin log")
	}
	if !strings.Contains(s.sam.do("GET", "/account", nil).Body.String(), `href="/account/activity"`) {
		t.Error("the Account page does not link to the page once it is on")
	}
	w := s.sam.do("GET", "/account/activity", nil)
	page := w.Body.String()
	if w.Code != 200 || !strings.Contains(page, "changed one of your entries") || !strings.Contains(page, "<code>") || !strings.Contains(page, "note") {
		t.Fatalf("sam's page: %d %.1200s", w.Code, page)
	}
	if !strings.Contains(page, "opened the edit page") {
		t.Errorf("the administrator's look at the edit page is not listed: %.1200s", page)
	}
	for _, bad := range []string{secret, s.e.idOf("owner"), s.e.idOf("sam")} {
		if strings.Contains(page, bad) {
			t.Errorf("sam's activity page shows %q", bad)
		}
	}
	// Other people see nothing of Sam's lines.
	for name, c := range map[string]*client{"kim": s.kim, "lou": s.lou, "the administrator": s.admin} {
		p := c.do("GET", "/account/activity", nil).Body.String()
		if strings.Contains(p, "<td>An administrator opened") || strings.Contains(p, "changed one of your") {
			t.Errorf("%s sees lines about sam: %s", name, regexp.MustCompile(`(?s)<tbody>.*?</tbody>`).FindString(p))
		}
	}
	// Viewing the page writes nothing to the admin log.
	before := len(logRows(t, s))
	s.sam.do("GET", "/account/activity", nil)
	if len(logRows(t, s)) != before {
		t.Error("looking at the activity page wrote to the admin log")
	}
	// Off again: gone, and logged.
	if w := s.admin.post("/admin/settings", "/admin/settings/activity", url.Values{}); w.Code != 303 {
		t.Fatalf("turning it off: %d", w.Code)
	}
	if w := s.sam.do("GET", "/account/activity", nil); w.Code != 404 || logCount(t, s, "settings.activity") != 2 {
		t.Errorf("after turning it off: %d, log lines %d", w.Code, logCount(t, s, "settings.activity"))
	}
}

// A new admin action that touches a person must be a conscious choice: listed on the person's page
// or on the hidden list with a reason. This reads the source for every action given to the log.
func TestEveryAdminActionIsShownOrHidden(t *testing.T) {
	re := regexp.MustCompile(`(?s)(?:audit|Append|auditView)\(.{0,80}?"([a-z0-9_]+(?:\.[a-z0-9_]+)+)"`)
	// Actions built as "prefix." + kind are invisible to the pattern above, so they are listed by hand
	// and the scan fails if a new dynamic prefix appears.
	dyn := regexp.MustCompile(`(?s)(?:audit|Append)\(.{0,80}?"([a-z]+\.)"\s*\+`)
	known := map[string][]string{"user.": {"disable", "enable", "delete", "restore"}, "space.": {"delete", "restore"}}
	seen := map[string]bool{}
	for p, kinds := range known {
		for _, k := range kinds {
			seen[p+k] = true
		}
	}
	err := filepath.Walk("..", func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			seen[m[1]] = true
		}
		for _, m := range dyn.FindAllStringSubmatch(string(b), -1) {
			if _, ok := known[m[1]]; !ok {
				t.Errorf("%s builds admin log actions from the prefix %q; list its kinds in this test", p, m[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) < 30 {
		t.Fatalf("the scan found only %d actions; the pattern no longer matches the code", len(seen))
	}
	for a := range seen {
		_, shown := activityShown[a]
		_, hidden := activityHidden[a]
		switch {
		case shown && hidden:
			t.Errorf("%q is both shown and hidden", a)
		case !shown && !hidden:
			t.Errorf("admin log action %q is neither on the member activity page nor on the hidden list (D-51)", a)
		}
	}
}
