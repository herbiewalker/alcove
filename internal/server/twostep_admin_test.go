package server

import (
	"context"
	"database/sql"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/herbiewalker/alcove/internal/auth"
	"github.com/herbiewalker/alcove/internal/netsec"
	"github.com/herbiewalker/alcove/internal/totp"
)

const secondAdminPW = "Second-Admin-Pass-1"

// addAdmin makes a second administrator with a known password and no forced change.
func (e *env) addAdmin(username string) auth.User {
	e.t.Helper()
	h, _ := auth.Hash(secondAdminPW, fastParams)
	var u auth.User
	if err := e.st.Update(context.Background(), true, func(tx *sql.Tx) (err error) {
		u, err = auth.Create(tx, e.clock, username, username, auth.RoleAdmin, h)
		return
	}); err != nil {
		e.t.Fatal(err)
	}
	return u
}

// Another administrator is the second way back in (D-49): it turns the person's two-step sign-in off,
// signs them out everywhere, and is logged.
func TestAnotherAdministratorCanResetTwoStep(t *testing.T) {
	e, secret, _ := twoStepEnv(t)
	owner, _ := e.userByName("owner")
	casey := e.addAdmin("casey")

	// The owner signs in with the code and has a live session.
	oc, _ := e.login("192.168.1.60:1", "owner", adminPW)
	if w := oc.post("/login", "/login/code", url.Values{"code": {totp.Code(secret, e.now()+1)}}); w.Code != 303 || !e.signedIn(oc) {
		t.Fatalf("setup: owner sign-in: %d", w.Code)
	}

	cc, _ := e.login("192.168.1.61:1", "casey", secondAdminPW)
	form := url.Values{"id": {owner.ID}}
	if w := cc.post("/admin/users", "/admin/users/reset2fa", form); w.Code != 303 || !strings.HasPrefix(w.Header().Get("Location"), "/stepup") {
		t.Fatalf("without a step-up: %d %s", w.Code, w.Header().Get("Location"))
	}
	if !e.twoOn(owner.ID) {
		t.Fatal("a refused request turned two-step off")
	}
	if w := cc.post("/stepup", "/stepup", url.Values{"password": {secondAdminPW}, "next": {"/admin/users"}}); w.Code != 303 {
		t.Fatalf("step-up: %d", w.Code)
	}
	if page := cc.do("GET", "/admin/users", nil).Body.String(); !strings.Contains(page, "two-step on") || !strings.Contains(page, "/admin/users/reset2fa") {
		t.Error("the People page does not offer the reset for a person who has it on")
	}
	if w := cc.post("/admin/users", "/admin/users/reset2fa", form); w.Code != 303 || e.twoOn(owner.ID) {
		t.Fatalf("the reset: %d, still on=%v", w.Code, e.twoOn(owner.ID))
	}
	found := false
	for _, l := range e.adminLines() {
		if l.Action == "user.totp.reset" && l.TargetID == owner.ID && l.Fields == "totp" {
			found = true
			if l.Actor != casey.ID {
				t.Errorf("the reset is logged against %q, not the administrator who did it", l.Actor)
			}
		}
	}
	if !found {
		t.Error("the reset is not in the admin log")
	}
	if e.signedIn(oc) {
		t.Error("the person stayed signed in after their two-step was reset")
	}
	// Their password alone works again.
	if c, w := e.login("192.168.1.62:1", "owner", adminPW); w.Header().Get("Location") != "/" || !e.signedIn(c) {
		t.Errorf("sign-in after the reset: %d %s", w.Code, w.Header().Get("Location"))
	}
	// Nothing left to reset, and nobody resets their own this way.
	if page := cc.do("GET", "/admin/users", nil).Body.String(); strings.Contains(page, "/admin/users/reset2fa") {
		t.Error("the reset is still offered after it was done")
	}
	if w := cc.post("/admin/users", "/admin/users/reset2fa", form); w.Code != 400 || !strings.Contains(w.Body.String(), "not on for that person") {
		t.Errorf("a second reset: %d", w.Code)
	}
	if w := cc.post("/admin/users", "/admin/users/reset2fa", url.Values{"id": {casey.ID}}); w.Code != 400 {
		t.Errorf("resetting your own: %d", w.Code)
	}
}

func TestOnlyAnAdministratorCanResetTwoStep(t *testing.T) {
	e, _, _ := twoStepEnv(t)
	owner, _ := e.userByName("owner")
	e.addMember("kim", "Kim-Password-1234")
	kim, _ := e.login("192.168.1.63:1", "kim", "Kim-Password-1234")
	if w := kim.post("/account", "/admin/users/reset2fa", url.Values{"id": {owner.ID}}); w.Code != 404 {
		t.Errorf("a member reset an administrator's two-step: %d", w.Code)
	}
	if w := e.client("192.168.1.99:1").do("POST", "/admin/users/reset2fa", url.Values{"id": {owner.ID}}); w.Code == 200 || w.Code == 303 && !strings.HasPrefix(w.Header().Get("Location"), "/login") {
		t.Errorf("a signed-out visitor: %d %s", w.Code, w.Header().Get("Location"))
	}
	if !e.twoOn(owner.ID) {
		t.Error("two-step was turned off by someone who may not")
	}
}

func TestHealthShowsTwoStepAndTheClockAndWarnsOnLowRecoveryCodes(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := adminReady(t, e)
	page := admin.do("GET", "/admin/health", nil).Body.String()
	if !strings.Contains(page, "off for every administrator") || !strings.Contains(page, "Server clock (UTC)") || !strings.Contains(page, "2026-11-13 10:00:0") {
		t.Fatalf("health with it off: %.600s", page)
	}
	base := warnCount(admin.do("GET", "/admin/health", nil).Body.String())

	owner, _ := e.userByName("owner")
	secret, _ := totp.NewSecret()
	codes, hashes, _ := totp.NewRecoveryCodes()
	if err := e.st.Update(context.Background(), true, func(tx *sql.Tx) error {
		return totp.Enable(tx, e.clock, owner.ID, secret, 0, hashes)
	}); err != nil {
		t.Fatal(err)
	}
	if p := admin.do("GET", "/admin/health", nil).Body.String(); !strings.Contains(p, "1 of 1 administrators use it") || strings.Contains(p, "running low") {
		i := strings.Index(p, "Network and settings")
		t.Fatalf("health with it on: %.900s", p[i:])
	}
	if got := warnCount(admin.do("GET", "/admin/health", nil).Body.String()); got != base {
		t.Errorf("warnings with full recovery codes: %d, want %d", got, base)
	}
	// Spend eight of ten: two are left, which is the line.
	if err := e.st.Update(context.Background(), true, func(tx *sql.Tx) error {
		for _, c := range codes[:8] {
			if ok, _, err := totp.UseRecovery(tx, e.clock.Add(time.Minute), owner.ID, c); err != nil || !ok {
				t.Fatalf("spend: %v %v", ok, err)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if p := admin.do("GET", "/admin/health", nil).Body.String(); !strings.Contains(p, "Recovery codes running low") {
		t.Error("two recovery codes left raised no warning")
	}
	if got := warnCount(admin.do("GET", "/admin/health", nil).Body.String()); got != base+1 {
		t.Errorf("warnings with two codes left: %d, want %d", got, base+1)
	}
	for _, bad := range []string{totp.Base32(secret), codes[9]} {
		if strings.Contains(admin.do("GET", "/admin/health", nil).Body.String(), bad) {
			t.Errorf("the health page shows %q", bad)
		}
	}
}
