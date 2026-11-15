package server

import (
	"context"
	"database/sql"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/herbiewalker/alcove/internal/adminlog"
	"github.com/herbiewalker/alcove/internal/auth"
)

// logRows reads the whole admin log, oldest first.
func logRows(t *testing.T, s *sharing) []adminlog.Entry {
	t.Helper()
	var all []adminlog.Entry
	err := s.e.st.View(context.Background(), func(tx *sql.Tx) (err error) {
		all, err = adminlog.List(tx, 1000, 0)
		return
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}
	return all
}

func logCount(t *testing.T, s *sharing, action string) int {
	n := 0
	for _, e := range logRows(t, s) {
		if e.Action == action {
			n++
		}
	}
	return n
}

// adminAgain signs the administrator in again (after a long clock jump) and steps up.
func adminAgain(t *testing.T, s *sharing) *client {
	t.Helper()
	c, w := s.e.login("192.168.1.10:5000", "owner", adminPW)
	if w.Code != 303 {
		t.Fatalf("admin sign-in: %d", w.Code)
	}
	if w := c.post("/stepup", "/stepup", url.Values{"password": {adminPW}, "next": {"/admin/users"}}); w.Code != 303 {
		t.Fatalf("step-up: %d", w.Code)
	}
	return c
}

func samBase(s *sharing) string { return "/admin/people/" + s.e.idOf("sam") }

func TestTheAdministratorEditsAPersonsEntryAndItIsLoggedWithoutContent(t *testing.T) {
	s := newSharing(t)
	id, _ := samEntry(t, s)
	edit := samBase(s) + "/entries/" + url.PathEscape(id) + "/edit"
	form := s.admin.do("GET", edit, nil)
	body := form.Body.String()
	if form.Code != 200 || !strings.Contains(body, `action="`+edit+`"`) || !strings.Contains(body, "as administrator") {
		t.Fatalf("admin form: %d %.400s", form.Code, body)
	}
	const secret = "ADMIN-WROTE-THIS-SECRET-NOTE"
	w := s.admin.post(edit, edit, url.Values{"rev": {formRev(t, body)}, "when": {extractWhen(t, body)}, "rating": {"2"}, "note": {secret}})
	if w.Code != 303 || w.Header().Get("Location") != samBase(s)+"/entries/"+id {
		t.Fatalf("admin save: %d %s %.300s", w.Code, w.Header().Get("Location"), w.Body.String())
	}
	if got := cacheValue(t, s, id, "$.note"); got != secret {
		t.Errorf("the note was not saved: %q", got)
	}
	// The owner and their space see it, and the owner's history names the administrator.
	if !strings.Contains(s.sam.do("GET", "/entries/"+id, nil).Body.String(), secret) {
		t.Error("the owner does not see the admin's edit")
	}
	if !strings.Contains(s.kim.do("GET", "/spaces/"+s.space+"/entry/"+s.e.idOf("sam")+"/"+id, nil).Body.String(), secret) {
		t.Error("the space does not see the admin's edit")
	}
	if h := s.sam.do("GET", "/entries/"+id+"/history", nil).Body.String(); !strings.Contains(h, "The administrator") {
		t.Errorf("the owner's history does not say who edited: %.500s", h)
	}
	// One log line: which table, whose row, which fields. Never a value.
	var edits []adminlog.Entry
	for _, e := range logRows(t, s) {
		if e.Action == "data.edit" {
			edits = append(edits, e)
		}
	}
	if len(edits) != 1 {
		t.Fatalf("expected one data.edit entry, got %d", len(edits))
	}
	e := edits[0]
	if e.TargetType != "encounters" || e.TargetID != s.e.idOf("sam")+":"+id || e.Actor != s.e.idOf("owner") {
		t.Errorf("log target: %+v", e)
	}
	if !strings.Contains(e.Fields, "note") || strings.Contains(e.Fields, secret) {
		t.Errorf("log fields: %q", e.Fields)
	}
	for _, r := range logRows(t, s) {
		if strings.Contains(r.Fields+r.TargetID+r.Action, secret) {
			t.Errorf("the log carries content: %+v", r)
		}
	}
	// The chain still verifies.
	if err := s.e.st.View(context.Background(), func(tx *sql.Tx) error { _, err := adminlog.Verify(tx); return err }); err != nil {
		t.Errorf("chain: %v", err)
	}
	// The owner can undo what the administrator did, and the administrator can undo it too.
	hist := s.admin.do("GET", samBase(s)+"/entries/"+id+"/history", nil)
	if hist.Code != 200 || !strings.Contains(hist.Body.String(), "An administrator") {
		t.Fatalf("admin history: %d %.400s", hist.Code, hist.Body.String())
	}
	revID := between(t, hist.Body.String(), `name="revision" value="`)
	rv := s.admin.post(samBase(s)+"/entries/"+id+"/history", samBase(s)+"/entries/"+id+"/revert",
		url.Values{"rev": {formRev(t, hist.Body.String())}, "revision": {revID}})
	if rv.Code != 303 {
		t.Fatalf("admin revert: %d %.300s", rv.Code, rv.Body.String())
	}
	if cacheValue(t, s, id, "$.note") == secret {
		t.Error("the revert did not put the note back")
	}
	if logCount(t, s, "data.revert") != 1 {
		t.Errorf("the revert was not logged")
	}
}

func TestEveryAdminLookAtAPersonsDataIsLogged(t *testing.T) {
	s := newSharing(t)
	id, _ := samEntry(t, s)
	base := samBase(s)
	before := logCount(t, s, "data.view")
	for i, p := range []string{base, base + "/entries/" + id, base + "/entries/" + id + "/edit", base + "/entries/" + id + "/history",
		base + "/entries/deleted", base + "/partners", base + "/entries/" + id + "/delete"} {
		if w := s.admin.do("GET", p, nil); w.Code != 200 {
			t.Fatalf("%s: %d", p, w.Code)
		}
		if got := logCount(t, s, "data.view"); got != before+i+1 {
			t.Errorf("%s was not logged (%d entries)", p, got-before)
		}
	}
	for _, r := range logRows(t, s) {
		if r.Action == "data.view" && !strings.HasPrefix(r.TargetID, s.e.idOf("sam")) {
			t.Errorf("a view log names the wrong person: %+v", r)
		}
	}
	// The entry page is the person's entry, with no photos for the administrator.
	page := s.admin.do("GET", base+"/entries/"+id, nil).Body.String()
	if !strings.Contains(page, "Person sam") || !strings.Contains(page, base+"/entries/"+id+"/edit") {
		t.Errorf("admin entry page: %.600s", page)
	}
}

func TestOnlyTheAdministratorReachesAPersonsDataThroughTheAdminPages(t *testing.T) {
	s := newSharing(t)
	id, _ := samEntry(t, s)
	base := samBase(s)
	before := cacheValue(t, s, id, "$.note")
	logged := len(logRows(t, s))
	for name, c := range map[string]*client{"the owner": s.sam, "another member": s.kim, "an outsider": s.lou} {
		for _, p := range []string{base, base + "/entries/" + id, base + "/entries/" + id + "/edit", base + "/partners"} {
			if w := c.do("GET", p, nil); w.Code == 200 {
				t.Errorf("%s opened %s", name, p)
			}
		}
		if w := c.post("/entries", base+"/entries/"+id+"/edit", url.Values{"rev": {"x"}, "when": {"2026-01-01T10:00"}, "note": {"HIJACK"}}); w.Code == 200 || w.Code == 303 {
			t.Errorf("%s saved through the admin page: %d", name, w.Code)
		}
	}
	if cacheValue(t, s, id, "$.note") != before {
		t.Error("someone other than the administrator changed the entry")
	}
	if len(logRows(t, s)) != logged {
		t.Error("a refused request wrote to the admin log")
	}
	if w := s.admin.do("GET", "/admin/people/unobody", nil); w.Code != 404 {
		t.Errorf("a person who does not exist: %d", w.Code)
	}
}

func TestAnAdminChangeAsksForThePasswordAgain(t *testing.T) {
	s := newSharing(t)
	id, _ := samEntry(t, s)
	edit := samBase(s) + "/entries/" + url.PathEscape(id) + "/edit"
	body := s.admin.do("GET", edit, nil).Body.String()
	before := cacheValue(t, s, id, "$.note")
	s.e.clock = s.e.clock.Add(auth.StepUpTTL + time.Minute)
	w := s.admin.post(edit, edit, url.Values{"rev": {formRev(t, body)}, "when": {extractWhen(t, body)}, "note": {"TOO-LATE"}})
	if w.Code != 303 || !strings.HasPrefix(w.Header().Get("Location"), "/stepup") {
		t.Fatalf("a change without a fresh step-up: %d %s", w.Code, w.Header().Get("Location"))
	}
	if cacheValue(t, s, id, "$.note") != before || logCount(t, s, "data.edit") != 0 {
		t.Error("the change went through")
	}
}

func TestTheAdministratorDeletesAndRestoresAnEntryInTheLongerWindow(t *testing.T) {
	s := newSharing(t)
	id, _ := samEntry(t, s)
	base := samBase(s)
	del := base + "/entries/" + id + "/delete"
	page := s.admin.do("GET", del, nil)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "As administrator you are about to delete") {
		t.Fatalf("confirm: %d %.300s", page.Code, page.Body.String())
	}
	if w := s.admin.post(del, del, url.Values{"rev": {formRev(t, page.Body.String())}}); w.Code != 303 || w.Header().Get("Location") != base+"/entries/deleted" {
		t.Fatalf("delete: %d %s", w.Code, w.Header().Get("Location"))
	}
	if w := s.sam.do("GET", "/entries/"+id, nil); w.Code != 404 {
		t.Errorf("the deleted entry still opens for its owner: %d", w.Code)
	}
	if !strings.Contains(s.sam.do("GET", "/entries/deleted", nil).Body.String(), `action="/entries/`+id+`/restore"`) {
		t.Error("the owner cannot restore what the administrator deleted")
	}
	for _, e := range logRows(t, s) {
		if e.Action == "data.delete" && (e.TargetType != "encounters" || e.TargetID != s.e.idOf("sam")+":"+id || e.Fields != "") {
			t.Errorf("delete log: %+v", e)
		}
	}
	if logCount(t, s, "data.delete") != 1 {
		t.Fatal("the delete was not logged")
	}
	// Twenty days on: past the person's ten days, inside the administrator's forty-five.
	s.e.clock = s.e.clock.Add(20 * 24 * time.Hour)
	sam := s.e.reLogin("sam")
	if strings.Contains(sam.do("GET", "/entries/deleted", nil).Body.String(), `/restore"`) {
		t.Error("the owner can restore past their window")
	}
	admin := adminAgain(t, s)
	list := admin.do("GET", base+"/entries/deleted", nil)
	if !strings.Contains(list.Body.String(), `action="`+base+`/entries/`+id+`/restore"`) {
		t.Fatalf("the administrator has no restore inside the long window: %.600s", list.Body.String())
	}
	// A person cannot restore through the admin route.
	if w := sam.post("/entries/deleted", base+"/entries/"+id+"/restore", url.Values{"rev": {formRev(t, list.Body.String())}}); w.Code == 303 || w.Code == 200 {
		t.Errorf("the owner restored through the admin route: %d", w.Code)
	}
	if w := admin.post(base+"/entries/deleted", base+"/entries/"+id+"/restore", url.Values{"rev": {formRev(t, list.Body.String())}}); w.Code != 303 {
		t.Fatalf("admin restore: %d %.300s", w.Code, w.Body.String())
	}
	if logCount(t, s, "data.restore") != 1 {
		t.Error("the restore was not logged")
	}
	if w := s.e.reLogin("sam").do("GET", "/entries/"+id, nil); w.Code != 200 {
		t.Errorf("the restored entry does not open: %d", w.Code)
	}
}

func TestTheAdministratorCannotRestoreAnEntryPastTheirWindow(t *testing.T) {
	s := newSharing(t)
	id, _ := samEntry(t, s)
	base := samBase(s)
	del := base + "/entries/" + id + "/delete"
	page := s.admin.do("GET", del, nil)
	if w := s.admin.post(del, del, url.Values{"rev": {formRev(t, page.Body.String())}}); w.Code != 303 {
		t.Fatalf("delete: %d", w.Code)
	}
	s.e.clock = s.e.clock.Add(50 * 24 * time.Hour)
	admin := adminAgain(t, s)
	list := admin.do("GET", base+"/entries/deleted", nil)
	if strings.Contains(list.Body.String(), `/restore"`) {
		t.Errorf("a restore button past the administrator's window: %.500s", list.Body.String())
	}
	pid := strings.TrimSuffix(between(t, admin.do("GET", base+"/partners", nil).Body.String(), base+"/partners/"), "/edit")
	rev := formRev(t, admin.do("GET", base+"/partners/"+pid+"/edit", nil).Body.String())
	if w := admin.post(base+"/entries/deleted", base+"/entries/"+id+"/restore", url.Values{"rev": {rev}}); w.Code != 409 {
		t.Errorf("a forced restore past the window: %d", w.Code)
	}
}

func TestTheAdministratorRenamesAPartnerAndItIsLogged(t *testing.T) {
	s := newSharing(t)
	base := samBase(s)
	list := s.admin.do("GET", base+"/partners", nil)
	if list.Code != 200 {
		t.Fatalf("partners: %d", list.Code)
	}
	pid := between(t, list.Body.String(), base+`/partners/`)
	pid = strings.TrimSuffix(pid, "/edit")
	edit := base + "/partners/" + pid + "/edit"
	form := s.admin.do("GET", edit, nil)
	if form.Code != 200 {
		t.Fatalf("partner form: %d", form.Code)
	}
	const name = "AdminGaveThisName"
	if w := s.admin.post(edit, edit, url.Values{"rev": {formRev(t, form.Body.String())}, "name": {name}, "note": {""}}); w.Code != 303 || w.Header().Get("Location") != base+"/partners" {
		t.Fatalf("rename: %d %s %.300s", w.Code, w.Header().Get("Location"), w.Body.String())
	}
	if !strings.Contains(s.sam.do("GET", "/partners", nil).Body.String(), name) {
		t.Error("the owner does not see the new name")
	}
	var found bool
	for _, e := range logRows(t, s) {
		if e.Action == "data.edit" && e.TargetType == "partners" && e.TargetID == s.e.idOf("sam")+":"+pid {
			found = true
			if strings.Contains(e.Fields, name) || !strings.Contains(e.Fields, "displayName") {
				t.Errorf("partner log fields: %q", e.Fields)
			}
		}
	}
	if !found {
		t.Error("the partner edit was not logged")
	}
}
