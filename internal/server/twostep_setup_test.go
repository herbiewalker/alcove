package server

import (
	"context"
	"database/sql"
	"encoding/base32"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/herbiewalker/alcove/internal/netsec"
	"github.com/herbiewalker/alcove/internal/totp"
)

var (
	keyRE      = regexp.MustCompile(`Key: <code>([A-Z2-7]+)</code>`)
	recoveryRE = regexp.MustCompile(`<li><code>([a-z0-9]{4}-[a-z0-9]{4}-[a-z0-9]{4})</code></li>`)
)

func (e *env) twoOn(uid string) bool {
	var on bool
	_ = e.st.View(context.Background(), func(tx *sql.Tx) (err error) { on, err = totp.Enabled(tx, uid); return })
	return on
}

// startSetup presses "Set up" and returns the secret the page shows.
func startSetup(t *testing.T, c *client) []byte {
	t.Helper()
	w := c.post(twoStepPath, "/account/twostep/start", nil)
	m := keyRE.FindStringSubmatch(w.Body.String())
	if w.Code != 200 || m == nil || !strings.Contains(w.Body.String(), `<svg class="qr"`) {
		t.Fatalf("setup page: %d %.300s", w.Code, w.Body.String())
	}
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(m[1])
	if err != nil {
		t.Fatal(err)
	}
	return secret
}

func submitCode(c *client, path, code string) (int, string) {
	tokenPage := twoStepPath
	if strings.HasPrefix(path, "/login") {
		tokenPage = "/login"
	}
	w := c.post(tokenPage, path, url.Values{"code": {code}})
	return w.Code, w.Body.String()
}

// The whole life of the feature through the real pages: off, set up, sign in with it, new recovery
// codes, turn it off.
func TestTwoStepSetupThroughToTurnOff(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := adminReady(t, e)
	u, _ := e.userByName("owner")

	if b := admin.do("GET", twoStepPath, nil).Body.String(); !strings.Contains(b, "Two-step sign-in is off") {
		t.Fatalf("the page before setup: %.300s", b)
	}
	// A second device signed in as the same person.
	other, _ := e.login("192.168.1.50:1", "owner", adminPW)
	if !e.signedIn(other) {
		t.Fatal("setup: second device")
	}

	secret := startSetup(t, admin)
	if b := admin.do("GET", twoStepPath, nil).Body.String(); !strings.Contains(b, totp.Base32(secret)) {
		t.Error("a refresh during setup showed a different key")
	}
	// Nothing is on until a right code is typed.
	if code, _ := submitCode(admin, "/account/twostep/enable", "000000"); code != 401 || e.twoOn(u.ID) {
		t.Fatalf("a wrong code during setup: %d, on=%v", code, e.twoOn(u.ID))
	}
	status, body := submitCode(admin, "/account/twostep/enable", totp.Code(secret, e.now()))
	codes := recoveryRE.FindAllStringSubmatch(body, -1)
	if status != 200 || len(codes) != 10 || !e.twoOn(u.ID) {
		t.Fatalf("turning it on: %d, %d codes, on=%v", status, len(codes), e.twoOn(u.ID))
	}
	rec := make([]string, len(codes))
	for i, c := range codes {
		rec[i] = c[1]
	}
	if !has(e.logActions(), "totp.enable") {
		t.Error("turning it on is not in the admin log")
	}
	if e.signedIn(other) {
		t.Error("the other device stayed signed in after two-step was turned on")
	}
	if !e.signedIn(admin) {
		t.Error("the device that set it up was signed out")
	}
	if b := admin.do("GET", twoStepPath, nil).Body.String(); !strings.Contains(b, "Two-step sign-in is on") || !strings.Contains(b, "<b>10</b> of 10") {
		t.Errorf("the page after setup: %.400s", b)
	}

	// Sign in the new way. The code that proved the app works is already used.
	admin.post("/account", "/logout", nil)
	c, _ := e.login("192.168.1.51:1", "owner", adminPW)
	if code, _ := submitCode(c, "/login/code", totp.Code(secret, e.now())); code != 401 {
		t.Fatalf("the code used at setup worked again: %d", code)
	}
	e.clock = e.clock.Add(totp.Period * time.Second)
	if w := c.post("/login", "/login/code", url.Values{"code": {totp.Code(secret, e.now())}}); w.Code != 303 || !e.signedIn(c) {
		t.Fatalf("the first real sign-in: %d", w.Code)
	}
	adminStepUp(t, e, c)

	// New recovery codes need a current code, and replace the old ones.
	if code, _ := submitCode(c, "/account/twostep/recovery", "000000"); code != 401 {
		t.Fatalf("new recovery codes with a wrong code: %d", code)
	}
	e.clock = e.clock.Add(totp.Period * time.Second)
	status, body = submitCode(c, "/account/twostep/recovery", totp.Code(secret, e.now()))
	fresh := recoveryRE.FindAllStringSubmatch(body, -1)
	if status != 200 || len(fresh) != 10 || !has(e.logActions(), "totp.recovery.new") {
		t.Fatalf("new recovery codes: %d, %d codes", status, len(fresh))
	}
	c.post("/account", "/logout", nil)
	c2, _ := e.login("192.168.1.52:1", "owner", adminPW)
	if code, _ := submitCode(c2, "/login/code", rec[0]); code != 401 {
		t.Errorf("an old recovery code worked after new ones were made: %d", code)
	}
	if code, _ := submitCode(c2, "/login/code", fresh[0][1]); code != 303 {
		t.Errorf("a new recovery code did not work: %d", code)
	}
	adminStepUp(t, e, c2)

	// Turning it off needs a code too, and then a password is enough again.
	if code, _ := submitCode(c2, "/account/twostep/disable", "000000"); code != 401 || !e.twoOn(u.ID) {
		t.Fatalf("turning off with a wrong code: %d", code)
	}
	if code, _ := submitCode(c2, "/account/twostep/disable", fresh[1][1]); code != 303 || e.twoOn(u.ID) {
		t.Fatalf("turning off with a recovery code: %d on=%v", code, e.twoOn(u.ID))
	}
	if !has(e.logActions(), "totp.disable") {
		t.Error("turning it off is not in the admin log")
	}
	c2.post("/account", "/logout", nil)
	if c3, w := e.login("192.168.1.53:1", "owner", adminPW); w.Header().Get("Location") != "/" || !e.signedIn(c3) {
		t.Errorf("after turning it off a password should be enough: %d %s", w.Code, w.Header().Get("Location"))
	}
}

func adminStepUp(t *testing.T, e *env, c *client) {
	t.Helper()
	if w := c.post("/stepup", "/stepup", url.Values{"password": {adminPW}, "next": {twoStepPath}}); w.Code != 303 {
		t.Fatalf("step-up: %d", w.Code)
	}
}

func TestTwoStepPagesNeedAStepUpAndAreAdminOnly(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := e.setupAdmin() // signed in, not stepped up
	for _, path := range []string{"/account/twostep/start", "/account/twostep/enable", "/account/twostep/recovery", "/account/twostep/disable"} {
		if w := admin.post(twoStepPath, path, nil); w.Code != 303 || !strings.HasPrefix(w.Header().Get("Location"), "/stepup?next=/account/twostep") {
			t.Errorf("%s without a step-up: %d %s", path, w.Code, w.Header().Get("Location"))
		}
	}
	if len(admin.cookies) > 0 && strings.Contains(admin.do("GET", twoStepPath, nil).Body.String(), "Key:") {
		t.Error("the page showed a key before setup")
	}
	e.addMember("kim", "Kim-Password-1234")
	kim, _ := e.login("192.168.1.54:1", "kim", "Kim-Password-1234")
	if w := kim.do("GET", twoStepPath, nil); w.Code != 404 {
		t.Errorf("a member opened the page: %d", w.Code)
	}
	if w := kim.post("/account", "/account/twostep/start", nil); w.Code != 404 {
		t.Errorf("a member started setup: %d", w.Code)
	}
	if w := e.client("192.168.1.99:1").do("POST", "/account/twostep/enable", url.Values{"code": {"123456"}}); w.Code == 200 {
		t.Error("a signed-out visitor reached setup")
	}
}

func TestSetupTimesOutAndFiveWrongCodesLockIt(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := adminReady(t, e)
	u, _ := e.userByName("owner")

	secret := startSetup(t, admin)
	e.clock = e.clock.Add(setupTTL + time.Second)
	adminStepUp(t, e, admin)
	if code, _ := submitCode(admin, "/account/twostep/enable", totp.Code(secret, e.now())); code != 409 || e.twoOn(u.ID) {
		t.Fatalf("a timed-out setup finished: %d", code)
	}

	secret = startSetup(t, admin)
	for i := 0; i < 5; i++ {
		submitCode(admin, "/account/twostep/enable", "000000")
	}
	if code, _ := submitCode(admin, "/account/twostep/enable", totp.Code(secret, e.now())); code != 429 || e.twoOn(u.ID) {
		t.Fatalf("a right code during the lock: %d", code)
	}
	if countAction(e.adminLines(), "session.2fa.locked") != 1 {
		t.Error("the lock is not in the admin log exactly once")
	}
}

func TestSetupSecretsAndCodesNeverReachALog(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := adminReady(t, e)
	secret := startSetup(t, admin)
	typed := totp.Code(secret, e.now())
	_, body := submitCode(admin, "/account/twostep/enable", typed)
	codes := recoveryRE.FindAllStringSubmatch(body, -1)
	if len(codes) != 10 {
		t.Fatalf("setup: %d codes", len(codes))
	}
	var all strings.Builder
	all.WriteString(e.logs.String())
	for _, l := range e.adminLines() {
		all.WriteString(l.Action + " " + l.TargetType + " " + l.TargetID + " " + l.Fields + "\n")
	}
	hay := strings.ToLower(all.String())
	needles := []string{strings.ToLower(totp.Base32(secret)), typed}
	for _, c := range codes {
		needles = append(needles, c[1], strings.ReplaceAll(c[1], "-", ""), totp.HashRecovery(c[1]))
	}
	for _, n := range needles {
		if strings.Contains(hay, n) {
			t.Errorf("a log carries %q", n)
		}
	}
	// The pages that show a secret or codes are never cached.
	w := admin.post(twoStepPath, "/account/twostep/start", nil)
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Error("the setup page can be cached")
	}
}
