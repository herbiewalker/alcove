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
	"github.com/herbiewalker/alcove/internal/netsec"
	"github.com/herbiewalker/alcove/internal/totp"
)

// twoStepEnv makes a server whose administrator ("owner") has two-step sign-in on, writing the rows
// the way the setup page will (piece 3). It returns the secret and the recovery codes.
func twoStepEnv(t *testing.T) (*env, []byte, []string) {
	t.Helper()
	e := newEnv(t, netsec.Env{})
	e.setupAdmin()
	u, err := e.userByName("owner")
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := totp.NewSecret()
	codes, hashes, _ := totp.NewRecoveryCodes()
	if err := e.st.Update(context.Background(), true, func(tx *sql.Tx) error {
		return totp.Enable(tx, e.clock, u.ID, secret, totp.Step(e.clock)-1, hashes)
	}); err != nil {
		t.Fatal(err)
	}
	return e, secret, codes
}

func (e *env) now() int64 { return totp.Step(e.clock) }

func (e *env) adminLines() []adminlog.Entry {
	var out []adminlog.Entry
	_ = e.st.View(context.Background(), func(tx *sql.Tx) error {
		var err error
		out, err = adminlog.List(tx, 1000, 0)
		return err
	})
	return out
}

func countAction(es []adminlog.Entry, action string) int {
	n := 0
	for _, x := range es {
		if x.Action == action {
			n++
		}
	}
	return n
}

func (e *env) signedIn(c *client) bool { return c.do("GET", "/account", nil).Code == 200 }

func TestSignInNeedsTheSecondStepAndTheCodeWorksOnce(t *testing.T) {
	e, secret, _ := twoStepEnv(t)
	loginsBefore := countAction(e.adminLines(), "session.login")

	c, w := e.login("192.168.1.30:1", "owner", adminPW)
	if w.Code != 303 || w.Header().Get("Location") != "/login/code" {
		t.Fatalf("a right password should lead to the second step: %d %s", w.Code, w.Header().Get("Location"))
	}
	if e.signedIn(c) {
		t.Fatal("a password alone gave a session")
	}
	if countAction(e.adminLines(), "session.login") != loginsBefore {
		t.Error("a sign-in was logged before the second step")
	}
	if p := c.do("GET", "/login/code", nil); p.Code != 200 || !strings.Contains(p.Body.String(), "Second step") {
		t.Fatalf("the second step page: %d", p.Code)
	}

	if w := c.post("/login/code", "/login/code", url.Values{"code": {"000000"}}); w.Code != 401 || e.signedIn(c) {
		t.Fatalf("a wrong code: %d", w.Code)
	}
	good := totp.Code(secret, e.now())
	if w := c.post("/login/code", "/login/code", url.Values{"code": {good}}); w.Code != 303 || w.Header().Get("Location") != "/" {
		t.Fatalf("the right code: %d %s", w.Code, w.Header().Get("Location"))
	}
	if !e.signedIn(c) {
		t.Fatal("no session after the right code")
	}
	lines := e.adminLines()
	if countAction(lines, "session.login") != loginsBefore+1 {
		t.Error("the completed sign-in is not in the admin log exactly once")
	}
	for _, l := range lines {
		if l.Action == "session.login" && l.Fields == "2fa:totp" {
			goto found
		}
	}
	t.Error("the sign-in line does not say which second step was used")
found:

	// The same code a moment later is a replay.
	c.post("/account", "/logout", nil)
	c2, _ := e.login("192.168.1.30:2", "owner", adminPW)
	if w := c2.post("/login/code", "/login/code", url.Values{"code": {good}}); w.Code != 401 || e.signedIn(c2) {
		t.Fatalf("a used code was accepted again: %d", w.Code)
	}
	// The next step's code is fine.
	e.clock = e.clock.Add(totp.Period * time.Second)
	next := totp.Code(secret, e.now())
	if w := c2.post("/login/code", "/login/code", url.Values{"code": {next}}); w.Code != 303 || !e.signedIn(c2) {
		t.Fatalf("the next code: %d", w.Code)
	}
}

func TestRecoveryCodeSignsInOnceAndIsLoggedByCount(t *testing.T) {
	e, _, codes := twoStepEnv(t)
	c, _ := e.login("192.168.1.31:1", "owner", adminPW)
	if w := c.post("/login/code", "/login/code", url.Values{"code": {strings.ToUpper(codes[0])}}); w.Code != 303 || !e.signedIn(c) {
		t.Fatalf("a recovery code: %d", w.Code)
	}
	want := "2fa:recovery,left:9"
	found := false
	for _, l := range e.adminLines() {
		if l.Action == "session.login" && l.Fields == want {
			found = true
		}
	}
	if !found {
		t.Errorf("no sign-in line with %q", want)
	}
	c.post("/account", "/logout", nil)
	c2, _ := e.login("192.168.1.31:2", "owner", adminPW)
	if w := c2.post("/login/code", "/login/code", url.Values{"code": {codes[0]}}); w.Code != 401 || e.signedIn(c2) {
		t.Fatalf("a spent recovery code worked again: %d", w.Code)
	}
	if w := c2.post("/login/code", "/login/code", url.Values{"code": {codes[1]}}); w.Code != 303 {
		t.Fatalf("a second, unspent code: %d", w.Code)
	}
}

func TestFiveWrongCodesLockTheSecondStepEvenForTheRightCode(t *testing.T) {
	e, secret, _ := twoStepEnv(t)
	c, _ := e.login("192.168.1.32:1", "owner", adminPW)
	for i := 0; i < 5; i++ {
		if w := c.post("/login/code", "/login/code", url.Values{"code": {"000000"}}); w.Code != 401 {
			t.Fatalf("wrong code %d: %d", i+1, w.Code)
		}
	}
	if n := countAction(e.adminLines(), "session.2fa.locked"); n != 1 {
		t.Errorf("the lock is in the admin log %d times, want once", n)
	}
	if w := c.post("/login/code", "/login/code", url.Values{"code": {totp.Code(secret, e.now())}}); w.Code != 429 || e.signedIn(c) {
		t.Fatalf("a right code during the lock: %d", w.Code)
	}
	// The lock is on the account, not the device: another browser is refused too.
	c2, _ := e.login("192.168.1.33:1", "owner", adminPW)
	if w := c2.post("/login/code", "/login/code", url.Values{"code": {totp.Code(secret, e.now())}}); w.Code != 429 {
		t.Fatalf("another device during the lock: %d", w.Code)
	}
	// Eleven minutes later the lock has ended (the old pending sign-in has expired, so the password is typed again).
	e.clock = e.clock.Add(11 * time.Minute)
	c3, _ := e.login("192.168.1.34:1", "owner", adminPW)
	if w := c3.post("/login/code", "/login/code", url.Values{"code": {totp.Code(secret, e.now())}}); w.Code != 303 || !e.signedIn(c3) {
		t.Fatalf("after the lock: %d", w.Code)
	}
}

func TestPendingSignInIsNotASessionAndExpires(t *testing.T) {
	e, secret, _ := twoStepEnv(t)
	c, _ := e.login("192.168.1.35:1", "owner", adminPW)
	tok := c.cookies[pendingCookie]
	if tok == "" {
		t.Fatal("no pending cookie")
	}
	// The pending token offered as a session cookie opens nothing.
	forged := e.client("192.168.1.36:1")
	forged.cookies[sessionCookie] = tok
	if forged.do("GET", "/account", nil).Code == 200 {
		t.Error("a pending token worked as a session")
	}
	// A made-up pending cookie, and none at all, lead back to the password.
	for name, cl := range map[string]*client{"made-up": e.client("192.168.1.37:1"), "none": e.client("192.168.1.38:1")} {
		if name == "made-up" {
			cl.cookies[pendingCookie] = "not-a-token"
		}
		if w := cl.do("GET", "/login/code", nil); w.Code != 303 || w.Header().Get("Location") != "/login" {
			t.Errorf("%s: %d %s", name, w.Code, w.Header().Get("Location"))
		}
		if w := cl.post("/login", "/login/code", url.Values{"code": {totp.Code(secret, e.now())}}); w.Code != 303 || cl.do("GET", "/account", nil).Code == 200 {
			t.Errorf("%s post: %d", name, w.Code)
		}
	}
	// After five minutes the pending sign-in is gone.
	e.clock = e.clock.Add(auth.PendingTTL + time.Second)
	if w := c.post("/login", "/login/code", url.Values{"code": {totp.Code(secret, e.now())}}); w.Code != 303 || w.Header().Get("Location") != "/login" || e.signedIn(c) {
		t.Fatalf("an expired pending sign-in: %d %s", w.Code, w.Header().Get("Location"))
	}
}

func TestWrongPasswordLooksTheSameWhetherOrNotTwoStepIsOn(t *testing.T) {
	e, _, _ := twoStepEnv(t)
	e.addMember("kim", "Kim-Password-1234")
	_, a := e.login("192.168.1.40:1", "owner", "wrong password here")
	_, b := e.login("192.168.1.41:1", "kim", "wrong password here")
	if a.Code != b.Code || a.Code != 401 || !strings.Contains(a.Body.String(), "Wrong username or password.") || !strings.Contains(b.Body.String(), "Wrong username or password.") {
		t.Errorf("the two answers differ: %d vs %d", a.Code, b.Code)
	}
	if a.Header().Get("Set-Cookie") != "" && strings.Contains(a.Header().Get("Set-Cookie"), pendingCookie) {
		t.Error("a wrong password started a pending sign-in")
	}
	// A person without two-step still signs in with a password alone.
	if c, w := e.login("192.168.1.42:1", "kim", "Kim-Password-1234"); w.Code != 303 || w.Header().Get("Location") != "/" || !e.signedIn(c) {
		t.Errorf("a member without two-step: %d", w.Code)
	}
}

func TestDisabledAccountCannotFinishTheSecondStep(t *testing.T) {
	e, secret, _ := twoStepEnv(t)
	c, _ := e.login("192.168.1.43:1", "owner", adminPW)
	u, _ := e.userByName("owner")
	if err := e.st.Update(context.Background(), true, func(tx *sql.Tx) error { return auth.SetStatus(tx, u.ID, auth.StatusDisabled) }); err != nil {
		t.Fatal(err)
	}
	if w := c.post("/login/code", "/login/code", url.Values{"code": {totp.Code(secret, e.now())}}); w.Code != 401 || e.signedIn(c) {
		t.Fatalf("a disabled account finished sign-in: %d", w.Code)
	}
}

// Constraint 8: no secret, code or recovery code in any log.
func TestNoSecondStepSecretReachesAnyLog(t *testing.T) {
	e, secret, codes := twoStepEnv(t)
	c, _ := e.login("192.168.1.44:1", "owner", adminPW)
	typed := totp.Code(secret, e.now())
	c.post("/login/code", "/login/code", url.Values{"code": {"123456"}})
	c.post("/login/code", "/login/code", url.Values{"code": {typed}})
	c.post("/account", "/logout", nil)
	c2, _ := e.login("192.168.1.44:2", "owner", adminPW)
	c2.post("/login/code", "/login/code", url.Values{"code": {codes[3]}})

	var all strings.Builder
	all.WriteString(e.logs.String())
	for _, l := range e.adminLines() {
		all.WriteString(l.Action + " " + l.TargetType + " " + l.TargetID + " " + l.Fields + "\n")
	}
	hay := strings.ToLower(all.String())
	for _, secretText := range []string{strings.ToLower(totp.Base32(secret)), typed, "123456", strings.ToLower(codes[3]), strings.ToLower(strings.ReplaceAll(codes[3], "-", "")), totp.HashRecovery(codes[3])} {
		if strings.Contains(hay, secretText) {
			t.Errorf("a log carries %q", secretText)
		}
	}
}
