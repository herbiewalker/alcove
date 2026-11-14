package server

import (
	"github.com/herbiewalker/alcove/internal/netsec"
	"net/url"
	"strings"
	"testing"
)

func saveProfile(c *client, name string, extra url.Values) int {
	f := url.Values{"name": {name}}
	for k, v := range extra {
		f[k] = v
	}
	return c.post("/profile", "/profile", f).Code
}

func TestProfileSharingShowsOnlyWhatIsTickedAndAgeForBirth(t *testing.T) {
	s := newSharing(t)
	code := saveProfile(s.sam, "Sam Showing", url.Values{
		"v_height": {"172 cm"}, "s_height": {"1"},
		"v_sex":   {"a private detail"},
		"v_birth": {"1990-04-23"}, "s_birth": {"1"},
		"v_about": {"<b>hello</b>"}, "s_about": {"1"},
	})
	if code != 303 {
		t.Fatalf("save: %d", code)
	}
	page := s.kim.do("GET", "/spaces/"+s.space, nil).Body.String()
	for _, want := range []string{"Sam Showing", "172 cm", "Age ", "&lt;b&gt;hello&lt;/b&gt;"} {
		if !strings.Contains(page, want) {
			t.Errorf("kim does not see %q", want)
		}
	}
	for _, leak := range []string{"a private detail", "1990-04-23", "<b>hello</b>"} {
		if strings.Contains(page, leak) {
			t.Errorf("kim sees %q", leak)
		}
	}
	// The owner sees all his own values, including the private one and the exact date.
	own := s.sam.do("GET", "/profile", nil).Body.String()
	for _, want := range []string{"a private detail", "1990-04-23"} {
		if !strings.Contains(own, want) {
			t.Errorf("sam does not see his own %q", want)
		}
	}
	// An outsider gets nothing of it.
	if code, body := s.list(s.lou, s.space); code != 404 || strings.Contains(body, "172") {
		t.Errorf("outsider: %d", code)
	}
	if w := s.lou.do("GET", "/spaces/"+s.space, nil); w.Code != 404 || strings.Contains(w.Body.String(), "172 cm") {
		t.Errorf("outsider space page: %d", w.Code)
	}
	// Untick: it disappears for kim at once.
	saveProfile(s.sam, "Sam Showing", url.Values{"v_height": {"172 cm"}})
	if strings.Contains(s.kim.do("GET", "/spaces/"+s.space, nil).Body.String(), "172 cm") {
		t.Error("an unticked field is still shown")
	}
}

func TestPauseAndLeaveHideTheCard(t *testing.T) {
	s := newSharing(t)
	saveProfile(s.sam, "Sam", url.Values{"v_height": {"172 cm"}, "s_height": {"1"}})
	has := func() bool { return strings.Contains(s.kim.do("GET", "/spaces/"+s.space, nil).Body.String(), "172 cm") }
	if !has() {
		t.Fatal("setup: the shared field is not shown")
	}
	s.sam.post("/spaces/"+s.space, "/spaces/"+s.space+"/settings", url.Values{"paused": {"1"}})
	if has() {
		t.Error("a paused member's card still shows a field")
	}
	if !strings.Contains(s.kim.do("GET", "/spaces/"+s.space, nil).Body.String(), "Nothing is shown right now") {
		t.Error("a paused card should say so")
	}
	s.sam.post("/spaces/"+s.space, "/spaces/"+s.space+"/settings", url.Values{})
	if !has() {
		t.Fatal("unpausing did not bring the card back")
	}
	// A member who leaves has no card: kim shares something, sam looks, kim leaves.
	saveProfile(s.kim, "Kim", url.Values{"v_height": {"kim-height"}, "s_height": {"1"}})
	if !strings.Contains(s.sam.do("GET", "/spaces/"+s.space, nil).Body.String(), "kim-height") {
		t.Fatal("setup: kim's field is not shown")
	}
	s.kim.post("/spaces/"+s.space, "/spaces/"+s.space+"/leave", url.Values{})
	if strings.Contains(s.sam.do("GET", "/spaces/"+s.space, nil).Body.String(), "kim-height") {
		t.Error("a member who left still has a card")
	}
}

func TestProfileRefusesAChildAndAnUnrealDateAndSavesNothing(t *testing.T) {
	s := newSharing(t)
	saveProfile(s.sam, "Sam", url.Values{"v_sex": {"value-one"}})
	for _, bad := range []string{"2015-01-01", "2999-01-01", "23/04/1990", "1990-02-30"} {
		w := s.sam.post("/profile", "/profile", url.Values{"name": {"Changed"}, "v_sex": {"value-two"}, "v_birth": {bad}})
		if w.Code != 400 {
			t.Errorf("%q: %d, want 400", bad, w.Code)
		}
	}
	body := s.sam.do("GET", "/profile", nil).Body.String()
	if !strings.Contains(body, "value-one") || strings.Contains(body, "value-two") || strings.Contains(body, "Changed") {
		t.Errorf("a refused save changed the profile or the name: first=%v second=%v changed=%v", strings.Contains(body, "value-one"), strings.Contains(body, "value-two"), strings.Contains(body, "Changed"))
	}
	if n := s.e.count(`SELECT COUNT(*) FROM profile WHERE field = 'birth'`); n != 0 {
		t.Errorf("%d birth dates stored", n)
	}
	// An overlong name or a control character is refused too.
	if w := s.sam.post("/profile", "/profile", url.Values{"name": {strings.Repeat("a", 81)}}); w.Code != 400 {
		t.Errorf("long name: %d", w.Code)
	}
	if w := s.sam.post("/profile", "/profile", url.Values{"name": {"  "}}); w.Code != 400 {
		t.Errorf("blank name: %d", w.Code)
	}
}

func TestProfileWritesOnlyTheCallersOwnRows(t *testing.T) {
	s := newSharing(t)
	saveProfile(s.kim, "Kim", url.Values{"v_height": {"kims"}})
	// A forged owner field means nothing: the owner is the session.
	s.sam.post("/profile", "/profile", url.Values{"name": {"Sam"}, "v_height": {"sams"}, "owner": {s.e.idOf("kim")}, "owner_id": {s.e.idOf("kim")}})
	if n := s.e.count(`SELECT COUNT(*) FROM profile WHERE owner_id = ? AND value = 'sams'`, s.e.idOf("kim")); n != 0 {
		t.Error("sam wrote a row for kim")
	}
	if n := s.e.count(`SELECT COUNT(*) FROM profile WHERE owner_id = ? AND value = 'kims'`, s.e.idOf("kim")); n != 1 {
		t.Error("kim's row was disturbed")
	}
}

func TestPrefillOffersThenImportsOnlyWhatIsTickedAndSharesNothing(t *testing.T) {
	s := newSharing(t)
	// The synthetic profile row holds the name "Synthetic Self" and nothing else. Put a few
	// more values into sam's cached profile row, as a real backup would have.
	s.e.exec(`UPDATE cache_row SET data = ? WHERE owner_id = ? AND tbl = 'profile'`,
		`{"id":"self","displayName":"Synthetic Self","sex":"F","height":172,"birthDate":"1990-04-23","orientation":"x"}`, s.e.idOf("sam"))
	page := s.sam.do("GET", "/profile/prefill", nil).Body.String()
	for _, want := range []string{"Synthetic Self", "172", "1990-04-23", `name="take_name" value="1" checked`} {
		if !strings.Contains(page, want) {
			t.Errorf("the offers page lacks %q", want)
		}
	}
	if strings.Contains(page, `name="take_height" value="1" checked`) || strings.Contains(page, `name="take_sex" value="1" checked`) {
		t.Error("a sensitive field is pre-ticked")
	}
	if s.e.count(`SELECT COUNT(*) FROM profile`) != 0 {
		t.Fatal("looking at the offers imported something")
	}
	// Import the name and the height only.
	if w := s.sam.post("/profile/prefill", "/profile/prefill", url.Values{"take_name": {"1"}, "take_height": {"1"}}); w.Code != 303 {
		t.Fatalf("import: %d", w.Code)
	}
	if n := s.e.count(`SELECT COUNT(*) FROM profile WHERE owner_id = ?`, s.e.idOf("sam")); n != 1 {
		t.Errorf("%d fields imported, want 1", n)
	}
	if n := s.e.count(`SELECT COUNT(*) FROM profile WHERE shared = 1`); n != 0 {
		t.Error("importing turned sharing on")
	}
	if !strings.Contains(s.sam.do("GET", "/account", nil).Body.String(), "Synthetic Self") {
		t.Error("the name was not imported")
	}
	// Kim sees the new name but not the height.
	page = s.kim.do("GET", "/spaces/"+s.space, nil).Body.String()
	if !strings.Contains(page, "Synthetic Self") || strings.Contains(page, "172") {
		t.Error("the space page should show the name and nothing private")
	}
	// A forged field in the import form cannot bring in a value that is not in the backup.
	s.sam.post("/profile/prefill", "/profile/prefill", url.Values{"take_height": {"1"}, "v_height": {"999"}, "value_height": {"999"}})
	if n := s.e.count(`SELECT COUNT(*) FROM profile WHERE value = '999'`); n != 0 {
		t.Error("a value from the form was imported")
	}
}

func TestPrefillWithNoBackupSaysSo(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := adminReady(t, e)
	c := person(t, e, admin, "newbie")
	w := c.do("GET", "/profile/prefill", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Nothing to fill from") {
		t.Errorf("%d %.200s", w.Code, w.Body.String())
	}
	// Nobody signed in can reach the profile pages.
	for _, p := range []string{"/profile", "/profile/prefill"} {
		if w := e.client("192.168.1.99:1").do("GET", p, nil); w.Code == 200 {
			t.Errorf("%s open to a signed-out visitor", p)
		}
	}
}
