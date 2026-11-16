package server

import (
	"net/url"
	"strings"
	"testing"

	"github.com/herbiewalker/alcove/internal/netsec"
	"github.com/herbiewalker/alcove/internal/share"
)

// The console's default time zone beats the environment's, survives a restart, falls back to the
// environment's when emptied, needs a step-up, and is logged by field name only.
func TestDefaultZoneSetting(t *testing.T) {
	_ = share.SetDefaultZone("")
	t.Cleanup(func() { _ = share.SetDefaultZone("") })
	if err := share.SetDefaultZone("Europe/Berlin"); err != nil { // the environment's value
		t.Fatal(err)
	}
	e := newEnv(t, netsec.Env{})
	admin := e.setupAdmin()

	form := url.Values{"tz": {"America/New_York"}}
	if w := admin.post("/admin/settings", "/admin/settings/timezone", form); w.Code != 303 || !strings.HasPrefix(w.Header().Get("Location"), "/stepup") {
		t.Fatalf("a change without step-up: %d %s", w.Code, w.Header().Get("Location"))
	}
	if w := admin.post("/stepup", "/stepup", url.Values{"password": {adminPW}, "next": {"/admin/settings"}}); w.Code != 303 {
		t.Fatalf("step-up: %d", w.Code)
	}
	if w := admin.post("/admin/settings", "/admin/settings/timezone", url.Values{"tz": {"Mars/Olympus"}}); w.Code != 400 {
		t.Fatalf("a bad zone name: %d", w.Code)
	}
	if w := admin.post("/admin/settings", "/admin/settings/timezone", form); w.Code != 303 || w.Header().Get("Location") != "/admin/settings" {
		t.Fatalf("saving the zone: %d %s", w.Code, w.Header().Get("Location"))
	}
	if got := share.DefaultZoneName(); got != "America/New_York" {
		t.Fatalf("zone in use = %q", got)
	}
	if w := admin.do("GET", "/admin/settings", nil); !strings.Contains(w.Body.String(), "America/New_York") {
		t.Fatal("the settings page does not show the saved zone")
	}
	if !has(e.logActions(), "settings.timezone.change") {
		t.Fatalf("the change was not logged: %v", e.logActions())
	}

	// A restart (the environment gives Berlin again) still uses the saved zone.
	_ = share.SetDefaultZone("Europe/Berlin")
	e.srv = e.build(netsec.Env{})
	e.client("192.168.1.9:1").do("GET", "/login", nil)
	if got := share.DefaultZoneName(); got != "America/New_York" {
		t.Fatalf("after a restart the zone is %q, want the saved one", got)
	}

	// Emptied, it goes back to the environment's zone.
	admin = e.client("192.168.1.9:2")
	if w := admin.post("/login", "/login", url.Values{"username": {"owner"}, "password": {adminPW}}); w.Code != 303 {
		t.Fatalf("sign in again: %d", w.Code)
	}
	admin.post("/stepup", "/stepup", url.Values{"password": {adminPW}, "next": {"/admin/settings"}})
	if w := admin.post("/admin/settings", "/admin/settings/timezone", url.Values{"tz": {""}}); w.Code != 303 {
		t.Fatalf("clearing the zone: %d", w.Code)
	}
	if got := share.DefaultZoneName(); got != "Europe/Berlin" {
		t.Fatalf("after clearing the zone is %q, want the environment's", got)
	}
}
