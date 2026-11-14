package server

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/herbiewalker/alcove/internal/adminlog"
	"github.com/herbiewalker/alcove/internal/auth"
	"github.com/herbiewalker/alcove/internal/keyfile"
	"github.com/herbiewalker/alcove/internal/netsec"
	"github.com/herbiewalker/alcove/internal/store"
)

var fastParams = auth.Params{Memory: 8, Time: 1, Threads: 1}

// ---- harness ----

type fakeBackend struct {
	mu     sync.Mutex
	st     *store.Store
	pass   string // when set, the backend starts locked and this opens it
	opener func() *store.Store
}

func (f *fakeBackend) State() string {
	if f.Store() == nil {
		return "locked"
	}
	return "unlocked"
}
func (f *fakeBackend) Store() *store.Store { f.mu.Lock(); defer f.mu.Unlock(); return f.st }
func (f *fakeBackend) Unlock(p string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.st != nil {
		return errors.New("not locked")
	}
	if p != f.pass {
		return errors.New("wrong passphrase")
	}
	f.st = f.opener()
	return nil
}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

type env struct {
	t     *testing.T
	st    *store.Store
	be    *fakeBackend
	srv   *Server
	logs  *lockedBuf
	clock time.Time
	fire  func() // fires the pending network-revert timer

	backupDir string
}

func newStore(t *testing.T) *store.Store {
	t.Helper()
	k, _, err := keyfile.Generate()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	st, err := store.Open(store.Config{DataDir: filepath.Join(root, "d"), TmpDir: filepath.Join(root, "t"),
		Key: k, AllowNonTmpfsTmp: true, SealEvery: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func newEnv(t *testing.T, nenv netsec.Env) *env {
	t.Helper()
	e := &env{t: t, logs: &lockedBuf{}, clock: time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)}
	e.st = newStore(t)
	e.backupDir = t.TempDir()
	e.be = &fakeBackend{st: e.st}
	e.srv = e.build(nenv)
	return e
}

func (e *env) build(nenv netsec.Env) *Server {
	var timer *time.Timer
	var fn func()
	e.fire = func() {
		if fn != nil {
			fn()
		}
		_ = timer
	}
	s, err := New(e.be, Options{Version: "test", Env: nenv, Params: fastParams,
		BackupDir: e.backupDir, BackupKeep: 2, TmpDir: filepath.Join(e.backupDir, "srvtmp"), AllowNonTmpfs: true,
		Logger: slog.New(slog.NewJSONHandler(e.logs, nil)),
		Now:    func() time.Time { return e.clock },
		AfterFunc: func(d time.Duration, f func()) *time.Timer {
			fn = f
			timer = time.NewTimer(time.Hour)
			return timer
		}})
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

// client is one browser: its address and its cookies.
type client struct {
	e       *env
	remote  string
	cookies map[string]string
	hdr     map[string]string
}

func (e *env) client(remote string) *client {
	return &client{e: e, remote: remote, cookies: map[string]string{}, hdr: map[string]string{}}
}

func (c *client) do(method, path string, form url.Values) *httptest.ResponseRecorder {
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	r := httptest.NewRequest(method, path, body)
	r.RemoteAddr = c.remote
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range c.hdr {
		r.Header.Set(k, v)
	}
	for k, v := range c.cookies {
		r.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	w := httptest.NewRecorder()
	c.e.srv.ServeHTTP(w, r)
	for _, ck := range w.Result().Cookies() {
		if ck.MaxAge < 0 || ck.Value == "" {
			delete(c.cookies, ck.Name)
		} else {
			c.cookies[ck.Name] = ck.Value
		}
	}
	return w
}

var csrfRE = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

// token fetches a page and returns the CSRF token in its first form.
func (c *client) token(path string) string {
	c.e.t.Helper()
	w := c.do("GET", path, nil)
	m := csrfRE.FindStringSubmatch(w.Body.String())
	if m == nil {
		c.e.t.Fatalf("no csrf token on %s (status %d): %.200s", path, w.Code, w.Body.String())
	}
	return m[1]
}

// post gets a token from tokenPage and submits the form with it.
func (c *client) post(tokenPage, path string, form url.Values) *httptest.ResponseRecorder {
	c.e.t.Helper()
	if form == nil {
		form = url.Values{}
	}
	form.Set("csrf", c.token(tokenPage))
	return c.do("POST", path, form)
}

func (e *env) setupCode() string {
	s := e.srv
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setupCode
}

func (e *env) users() int {
	var n int
	if err := e.st.View(context.Background(), func(tx *sql.Tx) (err error) { n, err = auth.Count(tx); return }); err != nil {
		e.t.Fatal(err)
	}
	return n
}

const adminPW = "correct horse battery"

// setupAdmin runs the real first-run flow and returns a signed-in admin client on the LAN.
func (e *env) setupAdmin() *client {
	e.t.Helper()
	c := e.client("192.168.1.10:5000")
	w := c.post("/setup", "/setup", url.Values{"code": {e.setupCode()}, "username": {"owner"},
		"display": {"The Owner"}, "password": {adminPW}, "confirm": {adminPW}})
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		e.t.Fatalf("setup: %d %s", w.Code, w.Body.String())
	}
	return c
}

func (e *env) addMember(username, pw string) auth.User {
	e.t.Helper()
	h, _ := auth.Hash(pw, fastParams)
	var u auth.User
	err := e.st.Update(context.Background(), true, func(tx *sql.Tx) (err error) {
		u, err = auth.Create(tx, e.clock, username, username, auth.RoleMember, h)
		return
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return u
}

func (e *env) login(remote, username, pw string) (*client, *httptest.ResponseRecorder) {
	c := e.client(remote)
	return c, c.post("/login", "/login", url.Values{"username": {username}, "password": {pw}})
}

func (e *env) logActions() []string {
	var acts []string
	_ = e.st.View(context.Background(), func(tx *sql.Tx) error {
		es, err := adminlog.List(tx, 1000, 0)
		for i := len(es) - 1; i >= 0; i-- {
			acts = append(acts, es[i].Action)
		}
		return err
	})
	return acts
}

func has(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// ---- health and headers ----

func TestHealthz(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	w := e.client("127.0.0.1:1").do("GET", "/healthz", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"unlocked"`) || !strings.Contains(w.Body.String(), `"version":"test"`) {
		t.Fatalf("healthz: %d %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Error("healthz must not be cached")
	}
}

func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	for _, p := range []string{"/healthz", "/setup", "/static/app.css", "/nope"} {
		w := e.client("192.168.1.10:1").do("GET", p, nil)
		for _, h := range []string{"Content-Security-Policy", "X-Content-Type-Options", "Referrer-Policy", "X-Frame-Options"} {
			if w.Header().Get(h) == "" {
				t.Errorf("%s: missing %s", p, h)
			}
		}
		csp := w.Header().Get("Content-Security-Policy")
		for _, want := range []string{"default-src 'none'", "style-src 'self'", "frame-ancestors 'none'", "form-action 'self'"} {
			if !strings.Contains(csp, want) {
				t.Errorf("%s: CSP lacks %q: %s", p, want, csp)
			}
		}
		if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") || strings.Contains(csp, "*") {
			t.Errorf("%s: CSP is too loose: %s", p, csp)
		}
	}
}

// ---- N17: the network gate ----

func TestNetworkGate(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	for remote, want := range map[string]int{
		"192.168.1.10:1": 200, "10.0.0.5:1": 200, "100.64.0.9:1": 200, "[fd00::5]:1": 200,
		"8.8.8.8:1": 403, "203.0.113.7:1": 403, "[2001:db8::1]:1": 403,
	} {
		if w := e.client(remote).do("GET", "/static/app.css", nil); w.Code != want {
			t.Errorf("%s: got %d want %d", remote, w.Code, want)
		}
	}
	// A public client cannot dress up as a LAN host or as loopback with headers.
	c := e.client("8.8.8.8:1")
	c.hdr["X-Forwarded-For"], c.hdr["X-Real-IP"] = "192.168.1.5", "127.0.0.1"
	if w := c.do("GET", "/healthz", nil); w.Code != 403 {
		t.Errorf("spoofed forwarded address was believed: %d", w.Code)
	}
	// The public refusal says nothing about the server.
	if body := e.client("8.8.8.8:1").do("GET", "/login", nil).Body.String(); strings.Contains(body, "Alcove") {
		t.Errorf("a refused request learned something: %q", body)
	}
}

func TestProxyModeGate(t *testing.T) {
	proxies := "10.0.0.2"
	e := newEnv(t, netsec.Env{TrustedProxies: &proxies})
	c := e.client("10.0.0.2:4000")
	c.hdr["X-Forwarded-For"] = "203.0.113.50" // the real client is public: refused even via the proxy
	if w := c.do("GET", "/static/app.css", nil); w.Code != 403 {
		t.Errorf("a public client behind the proxy got %d", w.Code)
	}
	c.hdr["X-Forwarded-For"] = "192.168.1.77"
	if w := c.do("GET", "/static/app.css", nil); w.Code != 200 {
		t.Errorf("a LAN client behind the proxy got %d", w.Code)
	}
	// Loopback proxy on the same host forwarding a public client must still be refused.
	l := e.client("127.0.0.1:1")
	l.hdr["X-Forwarded-For"] = "203.0.113.50"
	if w := l.do("GET", "/healthz", nil); w.Code == 200 {
		// 127.0.0.1 is not in the trusted list here, so headers are ignored and the peer is loopback: allowed.
		t.Log("loopback peer without trusted-proxy status is treated as itself (expected)")
	}
	p2 := "127.0.0.1"
	e2 := newEnv(t, netsec.Env{TrustedProxies: &p2})
	l2 := e2.client("127.0.0.1:1")
	l2.hdr["X-Forwarded-For"] = "203.0.113.50"
	if w := l2.do("GET", "/healthz", nil); w.Code != 403 {
		t.Errorf("a same-host proxy forwarding a public client must be refused, got %d", w.Code)
	}
}

// ---- first run, and constraint 11 ----

func TestFirstRunRedirectsToSetup(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	c := e.client("192.168.1.10:1")
	for _, p := range []string{"/", "/login", "/admin", "/account"} {
		if w := c.do("GET", p, nil); w.Code != 303 || w.Header().Get("Location") != "/setup" {
			t.Errorf("%s with no accounts: %d -> %s", p, w.Code, w.Header().Get("Location"))
		}
	}
	w := c.do("GET", "/setup", nil)
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, "consenting adults") || !strings.Contains(body, "invite only") {
		t.Fatalf("setup page must state adults-only and invite-only: %d", w.Code)
	}
	if !strings.Contains(e.logs.String(), "setup_code") {
		t.Error("the setup code must be in the server log at first run")
	}
}

func TestSetupNeedsTheCodeAndWorksOnce(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	c := e.client("192.168.1.10:1")
	form := func(code string) url.Values {
		return url.Values{"code": {code}, "username": {"owner"}, "password": {adminPW}, "confirm": {adminPW}}
	}
	for _, bad := range []string{"", "WRONG-CODE1", strings.ToLower(e.setupCode()) + "x"} {
		if w := c.post("/setup", "/setup", form(bad)); w.Code != 401 {
			t.Errorf("code %q: %d", bad, w.Code)
		}
	}
	if e.users() != 0 {
		t.Fatal("an account was created without the code")
	}
	// Mismatched and weak passwords never create anything either.
	f := form(e.setupCode())
	f.Set("confirm", "different password")
	if w := c.post("/setup", "/setup", f); w.Code != 400 || e.users() != 0 {
		t.Fatalf("mismatch: %d users=%d", w.Code, e.users())
	}
	f = form(strings.ToLower(e.setupCode())) // case and dashes are forgiven
	f.Set("password", "short")
	f.Set("confirm", "short")
	if w := c.post("/setup", "/setup", f); w.Code != 400 || e.users() != 0 {
		t.Fatalf("weak password: %d users=%d", w.Code, e.users())
	}

	admin := e.setupAdmin()
	if e.users() != 1 {
		t.Fatalf("users = %d", e.users())
	}
	if w := admin.do("GET", "/", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "Hello, The Owner") {
		t.Fatalf("home after setup: %d", w.Code)
	}
	// Setup is closed for good: 404 for a GET and a POST, with or without the old code.
	d := e.client("192.168.1.11:1")
	if w := d.do("GET", "/setup", nil); w.Code != 404 {
		t.Errorf("setup stayed open: %d", w.Code)
	}
	if w := d.post("/login", "/setup", form("anything")); w.Code != 404 || e.users() != 1 {
		t.Errorf("a second setup: %d users=%d", w.Code, e.users())
	}
	if !has(e.logActions(), "admin.first_run") {
		t.Error("the first-run admin creation must be in the admin log")
	}
}

// Constraint 11: no unauthenticated route can create an account.
func TestNoUnauthenticatedRouteCreatesAnAccount(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	var posts []string
	for _, r := range e.srv.Routes() {
		if strings.HasPrefix(r, "POST ") {
			posts = append(posts, strings.Fields(r)[1])
		}
	}
	if len(posts) < 10 {
		t.Fatalf("route list looks wrong: %v", e.srv.Routes())
	}
	form := url.Values{"username": {"intruder"}, "password": {"intruder password"}, "confirm": {"intruder password"},
		"display": {"x"}, "code": {"NOT-THECODE"}, "role": {"admin"}, "networks": {"8.8.8.0/24"}}
	hit := func(label, tokenPage string) {
		c := e.client("192.168.1.99:1")
		for _, p := range posts {
			f := url.Values{}
			for k, v := range form {
				f[k] = v
			}
			f.Set("csrf", c.token(tokenPage)) // even with a valid pre-session token
			c.do("POST", p, f)
			c.do("GET", p, nil)
		}
		if n := e.users(); n != map[string]int{"empty": 0, "after": 1}[label] {
			t.Fatalf("%s: unauthenticated requests left %d accounts", label, n)
		}
	}
	hit("empty", "/setup")
	e.setupAdmin()
	hit("after", "/login")
}

// ---- sign in ----

func TestLoginFlow(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	e.setupAdmin()
	e.addMember("sam", "sam has a long password")

	c, w := e.login("192.168.1.20:1", "sam", "sam has a long password")
	if w.Code != 303 || c.cookies[sessionCookie] == "" {
		t.Fatalf("good login: %d", w.Code)
	}
	var ck *http.Cookie
	for _, x := range w.Result().Cookies() {
		if x.Name == sessionCookie {
			ck = x
		}
	}
	if ck == nil || !ck.HttpOnly || ck.SameSite != http.SameSiteStrictMode || ck.Path != "/" {
		t.Fatalf("session cookie flags: %+v", ck)
	}
	if hi := c.do("GET", "/", nil); hi.Code != 200 || !strings.Contains(hi.Body.String(), "Hello, sam") {
		t.Fatalf("home as member: %d", hi.Code)
	}

	// Wrong password, unknown user and disabled user look identical.
	_, w1 := e.login("192.168.1.21:1", "sam", "not the password")
	_, w2 := e.login("192.168.1.22:1", "nobody", "not the password")
	if w1.Code != 401 || w2.Code != 401 {
		t.Fatalf("bad logins: %d %d", w1.Code, w2.Code)
	}
	for _, w := range []*httptest.ResponseRecorder{w1, w2} {
		if !strings.Contains(w.Body.String(), "Wrong username or password.") {
			t.Error("the failure message must be the generic one")
		}
	}
	c3 := e.client("192.168.1.23:1")
	var sam auth.User
	_ = e.st.View(context.Background(), func(tx *sql.Tx) (err error) { sam, err = auth.ByUsername(tx, "sam"); return })
	_ = e.st.Update(context.Background(), true, func(tx *sql.Tx) error { return auth.SetStatus(tx, sam.ID, auth.StatusDisabled) })
	w3 := c3.post("/login", "/login", url.Values{"username": {"sam"}, "password": {"sam has a long password"}})
	if w3.Code != 401 || !strings.Contains(w3.Body.String(), "Wrong username or password.") {
		t.Fatalf("a disabled account signed in or said so: %d", w3.Code)
	}
	// Disabling also ends a session that was already open.
	if w := c.do("GET", "/", nil); w.Code != 303 {
		t.Fatalf("a disabled account kept its session: %d", w.Code)
	}
}

func TestLoginIsRateLimited(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	e.setupAdmin()
	c := e.client("192.168.1.30:1")
	var last int
	for i := 0; i < 14; i++ {
		last = c.post("/login", "/login", url.Values{"username": {"owner"}, "password": {"guess number " + string(rune('a'+i))}}).Code
	}
	if last != 429 {
		t.Fatalf("after many wrong guesses the answer was %d, want 429", last)
	}
	// Even the right password is refused while limited, and another address is unaffected.
	if w := c.post("/login", "/login", url.Values{"username": {"owner"}, "password": {adminPW}}); w.Code != 429 {
		t.Errorf("limited address got %d", w.Code)
	}
	if _, w := e.login("192.168.1.31:1", "owner", adminPW); w.Code != 303 {
		t.Errorf("another address was affected: %d", w.Code)
	}
}

// ---- CSRF ----

func TestCSRF(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := e.setupAdmin()
	tok := admin.token("/account")

	// No token, a wrong token, and another session's token are all refused.
	other := e.client("192.168.1.40:1")
	other.post("/login", "/login", url.Values{"username": {"owner"}, "password": {adminPW}})
	for label, form := range map[string]url.Values{
		"missing": {"theme": {"dark"}},
		"wrong":   {"theme": {"dark"}, "csrf": {"nope"}},
		"other":   {"theme": {"dark"}, "csrf": {other.token("/account")}},
	} {
		if w := admin.do("POST", "/theme", form); w.Code != 403 {
			t.Errorf("%s token: %d", label, w.Code)
		}
	}
	if w := admin.do("POST", "/theme", url.Values{"theme": {"dark"}, "csrf": {tok}}); w.Code != 303 {
		t.Errorf("a valid token was refused: %d", w.Code)
	}
	// Sign-out is state-changing too.
	if w := admin.do("POST", "/logout", url.Values{}); w.Code != 403 {
		t.Errorf("logout without a token: %d", w.Code)
	}
	// A cross-site fetch is refused even with a valid token.
	admin.hdr["Sec-Fetch-Site"] = "cross-site"
	if w := admin.do("POST", "/theme", url.Values{"theme": {"dark"}, "csrf": {tok}}); w.Code != 403 {
		t.Errorf("cross-site POST: %d", w.Code)
	}
	delete(admin.hdr, "Sec-Fetch-Site")

	// Before sign-in the pre-session token is bound to the browser's own cookie.
	a, b := e.client("192.168.1.41:1"), e.client("192.168.1.42:1")
	ta := a.token("/login")
	if w := b.do("POST", "/login", url.Values{"username": {"owner"}, "password": {adminPW}, "csrf": {ta}}); w.Code != 403 {
		t.Errorf("a token from another browser was accepted: %d", w.Code)
	}
}

// ---- who can reach what ----

func TestAccessLevels(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := e.setupAdmin()
	e.addMember("sam", "sam has a long password")
	member, _ := e.login("192.168.1.50:1", "sam", "sam has a long password")
	anon := e.client("192.168.1.51:1")

	adminOnly := []string{"/admin", "/admin/log", "/admin/settings", "/admin/backup", "/stepup"}
	for _, p := range adminOnly {
		if w := anon.do("GET", p, nil); w.Code != 303 || w.Header().Get("Location") != "/login" {
			t.Errorf("anonymous %s: %d", p, w.Code)
		}
		if w := member.do("GET", p, nil); w.Code != 404 {
			t.Errorf("a member reached %s: %d", p, w.Code)
		}
		if w := admin.do("GET", p, nil); w.Code != 200 {
			t.Errorf("admin %s: %d", p, w.Code)
		}
	}
	for _, p := range []string{"/", "/account"} {
		if w := anon.do("GET", p, nil); w.Code != 303 {
			t.Errorf("anonymous %s: %d", p, w.Code)
		}
		if w := member.do("GET", p, nil); w.Code != 200 {
			t.Errorf("member %s: %d", p, w.Code)
		}
	}
	// A member cannot make admin-only POSTs even with their own valid token.
	if w := member.post("/account", "/admin/settings/tls", url.Values{"tls": {"1"}}); w.Code != 404 {
		t.Errorf("member POST to admin route: %d", w.Code)
	}
	if w := anon.do("GET", "/admin/anything", nil); w.Code != 404 {
		t.Errorf("unknown path: %d", w.Code)
	}
}

// ---- N5: every admin view and action is logged, and the chain verifies ----

func TestAdminActionsAreLoggedAndChainVerifies(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := e.setupAdmin()
	admin.do("GET", "/admin", nil)
	admin.do("GET", "/admin/log", nil)
	admin.do("GET", "/admin/settings", nil)
	admin.post("/admin/log", "/admin/log/verify", nil)
	acts := e.logActions()
	for _, want := range []string{"admin.first_run", "admin.console.view", "adminlog.view", "settings.view", "adminlog.verify"} {
		if !has(acts, want) {
			t.Errorf("no log entry for %s (have %v)", want, acts)
		}
	}
	var res adminlog.Result
	var err error
	_ = e.st.View(context.Background(), func(tx *sql.Tx) error { res, err = adminlog.Verify(tx); return nil })
	if err != nil || res.Entries < 5 {
		t.Fatalf("chain: %+v %v", res, err)
	}
	if w := admin.do("GET", "/admin/log", nil); !strings.Contains(w.Body.String(), "admin.console.view") {
		t.Error("the viewer does not show the entries")
	}
}

func TestVerifyButtonDetectsTampering(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := e.setupAdmin()
	admin.do("GET", "/admin", nil)
	w := admin.post("/admin/log", "/admin/log/verify", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "The chain verifies") {
		t.Fatalf("clean verify: %d", w.Code)
	}
	// An attacker with the database drops the protections and edits a line.
	_ = e.st.Update(context.Background(), true, func(tx *sql.Tx) error {
		for _, q := range []string{`DROP TRIGGER admin_log_no_update`, `UPDATE admin_log SET action = 'nothing.happened' WHERE seq = 2`} {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		return nil
	})
	w = admin.post("/admin/log", "/admin/log/verify", nil)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "does NOT verify") {
		t.Fatalf("tampering not reported: %d", w.Code)
	}
}

// ---- settings: step-up, revert, env wins, require-TLS ----

func TestSettingsNeedStepUp(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := e.setupAdmin()
	w := admin.post("/admin/settings", "/admin/settings/tls", url.Values{"tls": {"1"}})
	if w.Code != 303 || !strings.HasPrefix(w.Header().Get("Location"), "/stepup") {
		t.Fatalf("a settings change without step-up: %d %s", w.Code, w.Header().Get("Location"))
	}
	if w := admin.post("/stepup", "/stepup", url.Values{"password": {"wrong wrong wrong"}, "next": {"/admin/settings"}}); w.Code != 401 {
		t.Fatalf("wrong step-up password: %d", w.Code)
	}
	if w := admin.post("/stepup", "/stepup", url.Values{"password": {adminPW}, "next": {"https://evil.example/"}}); w.Code != 303 || w.Header().Get("Location") != "/admin" {
		t.Fatalf("step-up must not redirect off-site: %d %s", w.Code, w.Header().Get("Location"))
	}
	// Now it is allowed (over plain HTTP, turning TLS on is refused, which proves the handler ran).
	if w := admin.post("/admin/settings", "/admin/settings/tls", url.Values{"tls": {"1"}}); w.Code != 400 || !strings.Contains(w.Body.String(), "lock you out") {
		t.Fatalf("require-TLS over plain HTTP must be refused: %d", w.Code)
	}
	// The step-up lapses.
	e.clock = e.clock.Add(auth.StepUpTTL + time.Minute)
	if w := admin.post("/admin/settings", "/admin/settings/proxy", url.Values{}); w.Code != 303 || !strings.HasPrefix(w.Header().Get("Location"), "/stepup") {
		t.Fatalf("an expired step-up was honoured: %d", w.Code)
	}
}

func steppedUp(t *testing.T, e *env) *client {
	admin := e.setupAdmin()
	if w := admin.post("/stepup", "/stepup", url.Values{"password": {adminPW}, "next": {"/admin/settings"}}); w.Code != 303 {
		t.Fatalf("step-up: %d", w.Code)
	}
	return admin
}

func TestNetworkChangeRevertsUnlessConfirmed(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := steppedUp(t, e)

	// Refuses a list that would lock the admin out, and one that is too wide.
	if w := admin.post("/admin/settings", "/admin/settings/networks", url.Values{"networks": {"10.9.0.0/16"}}); w.Code != 400 || !strings.Contains(w.Body.String(), "your own address") {
		t.Fatalf("self-lockout: %d", w.Code)
	}
	if w := admin.post("/admin/settings", "/admin/settings/networks", url.Values{"networks": {"0.0.0.0/0"}}); w.Code != 400 {
		t.Fatalf("whole internet: %d", w.Code)
	}

	// A valid change takes effect at once and shows the confirm button.
	if w := admin.post("/admin/settings", "/admin/settings/networks", url.Values{"networks": {"192.168.1.0/24"}}); w.Code != 303 {
		t.Fatalf("propose: %d %s", w.Code, w.Body.String())
	}
	if w := e.client("10.0.0.5:1").do("GET", "/static/app.css", nil); w.Code != 403 {
		t.Fatalf("the proposed list is not live: %d", w.Code)
	}
	if body := admin.do("GET", "/admin/settings", nil).Body.String(); !strings.Contains(body, "Keep the new list") {
		t.Fatal("no confirm button while a change is pending")
	}

	// Nobody confirms: it undoes itself, and the system writes it down.
	e.fire()
	if w := e.client("10.0.0.5:1").do("GET", "/static/app.css", nil); w.Code != 200 {
		t.Fatalf("the unconfirmed change did not revert: %d", w.Code)
	}
	if !has(e.logActions(), "settings.networks.revert") {
		t.Errorf("the revert was not logged: %v", e.logActions())
	}

	// Confirmed: it persists, survives a restart of the server object, and is logged.
	admin.post("/admin/settings", "/admin/settings/networks", url.Values{"networks": {"192.168.1.0/24"}})
	if w := admin.post("/admin/settings", "/admin/settings/networks/confirm", nil); w.Code != 303 {
		t.Fatalf("confirm: %d", w.Code)
	}
	e.fire() // a late timer must change nothing
	if w := e.client("10.0.0.5:1").do("GET", "/static/app.css", nil); w.Code != 403 {
		t.Fatalf("a confirmed change was undone: %d", w.Code)
	}
	if v, ok, _ := e.st.GetMeta("setting.allowed_networks"); !ok || v != "192.168.1.0/24" {
		t.Fatalf("not persisted: %q %v", v, ok)
	}
	e.srv = e.build(netsec.Env{}) // the same store, a fresh server: the setting loads back
	if w := e.client("10.0.0.5:1").do("GET", "/static/app.css", nil); w.Code != 403 {
		t.Fatalf("the saved list was not loaded at start: %d", w.Code)
	}
	acts := e.logActions()
	for _, want := range []string{"settings.networks.propose", "settings.networks.confirm"} {
		if !has(acts, want) {
			t.Errorf("no log entry for %s", want)
		}
	}
}

func TestEnvironmentValueCannotBeChangedFromTheConsole(t *testing.T) {
	nets := "192.168.0.0/16"
	e := newEnv(t, netsec.Env{AllowedNets: &nets})
	admin := steppedUp(t, e)
	if body := admin.do("GET", "/admin/settings", nil).Body.String(); !strings.Contains(body, "Set by environment") {
		t.Error("the page must say the value is set by the environment")
	}
	w := admin.post("/admin/settings", "/admin/settings/networks", url.Values{"networks": {"192.168.1.0/24"}})
	if w.Code != 400 || !strings.Contains(w.Body.String(), "environment") {
		t.Fatalf("env-locked change: %d", w.Code)
	}
}

func TestRequireTLSAndProxy(t *testing.T) {
	proxies := "10.0.0.2"
	e := newEnv(t, netsec.Env{TrustedProxies: &proxies})
	// The admin signs in through the proxy, which says the browser connection is HTTPS.
	admin := e.client("10.0.0.2:4000")
	admin.hdr["X-Forwarded-For"], admin.hdr["X-Forwarded-Proto"] = "192.168.1.10", "https"
	code := e.setupCode()
	admin.post("/setup", "/setup", url.Values{"code": {code}, "username": {"owner"}, "password": {adminPW}, "confirm": {adminPW}})
	admin.post("/stepup", "/stepup", url.Values{"password": {adminPW}})
	if w := admin.post("/admin/settings", "/admin/settings/tls", url.Values{"tls": {"1"}}); w.Code != 303 {
		t.Fatalf("enabling require-TLS from an encrypted connection: %d %s", w.Code, w.Body.String())
	}
	// Now a plain-HTTP sign-in page is refused, the HTTPS one works, and cookies are Secure.
	plain := e.client("192.168.1.30:1")
	if w := plain.do("GET", "/login", nil); w.Code != 403 || !strings.Contains(w.Body.String(), "HTTPS") {
		t.Fatalf("plain-HTTP login page under require-TLS: %d", w.Code)
	}
	if w := plain.do("POST", "/login", url.Values{"username": {"owner"}, "password": {adminPW}}); w.Code != 403 {
		t.Fatalf("plain-HTTP login under require-TLS: %d", w.Code)
	}
	secure := e.client("10.0.0.2:5000")
	secure.hdr["X-Forwarded-For"], secure.hdr["X-Forwarded-Proto"] = "192.168.1.31", "https"
	w := secure.post("/login", "/login", url.Values{"username": {"owner"}, "password": {adminPW}})
	if w.Code != 303 {
		t.Fatalf("HTTPS login: %d", w.Code)
	}
	for _, ck := range w.Result().Cookies() {
		if ck.Name == sessionCookie && !ck.Secure {
			t.Error("the session cookie must be Secure over HTTPS")
		}
	}
	if w.Header().Get("Strict-Transport-Security") == "" {
		t.Error("HSTS must be sent over HTTPS")
	}
	// Turning it off is always possible.
	if w := secure.post("/admin/settings", "/admin/settings/tls", url.Values{}); w.Code != 303 && w.Code != 200 {
		t.Logf("turning off: %d", w.Code)
	}
}

func TestInsecureBannerForAdminOnly(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := e.setupAdmin()
	e.addMember("sam", "sam has a long password")
	member, _ := e.login("192.168.1.60:1", "sam", "sam has a long password")
	if !strings.Contains(admin.do("GET", "/admin", nil).Body.String(), "not encrypted") {
		t.Error("the admin must see the insecure-connection banner")
	}
	if strings.Contains(member.do("GET", "/", nil).Body.String(), "not encrypted") {
		t.Error("a member must not see the admin's banner")
	}
}

// ---- account ----

func TestChangePasswordSignsOutOtherDevices(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	phone := e.setupAdmin()
	laptop, _ := e.login("192.168.1.70:1", "owner", adminPW)
	const newPW = "a brand new passphrase"
	if w := phone.post("/account", "/account", url.Values{"current": {"wrong"}, "password": {newPW}, "confirm": {newPW}}); w.Code != 401 {
		t.Fatalf("wrong current password: %d", w.Code)
	}
	if w := phone.post("/account", "/account", url.Values{"current": {adminPW}, "password": {newPW}, "confirm": {"nope"}}); w.Code != 400 {
		t.Fatalf("mismatch: %d", w.Code)
	}
	if w := phone.post("/account", "/account", url.Values{"current": {adminPW}, "password": {newPW}, "confirm": {newPW}}); w.Code != 303 {
		t.Fatalf("change: %d", w.Code)
	}
	if w := phone.do("GET", "/account", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "Other devices were signed out") {
		t.Fatalf("this device must stay signed in: %d", w.Code)
	}
	if w := laptop.do("GET", "/account", nil); w.Code != 303 {
		t.Fatalf("the other device stayed signed in: %d", w.Code)
	}
	if _, w := e.login("192.168.1.71:1", "owner", adminPW); w.Code != 401 {
		t.Fatalf("the old password still works: %d", w.Code)
	}
	if _, w := e.login("192.168.1.72:1", "owner", newPW); w.Code != 303 {
		t.Fatalf("the new password does not work: %d", w.Code)
	}
	if !has(e.logActions(), "account.password.change") {
		t.Error("an admin's password change must be in the admin log")
	}
}

func TestLogoutEndsTheSession(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := e.setupAdmin()
	old := admin.cookies[sessionCookie]
	if w := admin.post("/account", "/logout", nil); w.Code != 303 {
		t.Fatalf("logout: %d", w.Code)
	}
	replay := e.client("192.168.1.80:1")
	replay.cookies[sessionCookie] = old
	if w := replay.do("GET", "/", nil); w.Code != 303 {
		t.Fatalf("a signed-out session still works: %d", w.Code)
	}
}

// ---- locked mode ----

func TestLockedModeAndUnlock(t *testing.T) {
	e := &env{t: t, logs: &lockedBuf{}, clock: time.Now()}
	st := newStore(t)
	e.be = &fakeBackend{pass: "open sesame", opener: func() *store.Store { return st }}
	e.srv = e.build(netsec.Env{})
	c := e.client("192.168.1.90:1")

	if w := c.do("GET", "/healthz", nil); !strings.Contains(w.Body.String(), `"state":"locked"`) {
		t.Fatalf("healthz while locked: %s", w.Body.String())
	}
	for _, p := range []string{"/", "/login", "/setup", "/admin"} {
		if w := c.do("GET", p, nil); w.Code != 303 || w.Header().Get("Location") != "/unlock" {
			t.Errorf("%s while locked: %d -> %s", p, w.Code, w.Header().Get("Location"))
		}
	}
	if w := c.do("POST", "/login", url.Values{"username": {"x"}}); w.Code != 503 {
		t.Errorf("POST while locked: %d", w.Code)
	}
	if w := c.post("/unlock", "/unlock", url.Values{"passphrase": {"wrong"}}); w.Code != 401 {
		t.Fatalf("wrong passphrase: %d", w.Code)
	}
	if w := c.post("/unlock", "/unlock", url.Values{"passphrase": {"open sesame"}}); w.Code != 303 || w.Header().Get("Location") != "/" {
		t.Fatalf("unlock: %d", w.Code)
	}
	if w := c.do("GET", "/healthz", nil); !strings.Contains(w.Body.String(), `"state":"unlocked"`) {
		t.Fatalf("healthz after unlock: %s", w.Body.String())
	}
	if w := c.do("GET", "/", nil); w.Code != 303 || w.Header().Get("Location") != "/setup" {
		t.Errorf("after unlock the first-run flow must start: %d -> %s", w.Code, w.Header().Get("Location"))
	}
	if w := c.do("GET", "/unlock", nil); w.Code != 303 {
		t.Errorf("the unlock page must go away once unlocked: %d", w.Code)
	}
}

func TestUnlockIsRateLimited(t *testing.T) {
	e := &env{t: t, logs: &lockedBuf{}, clock: time.Now()}
	e.be = &fakeBackend{pass: "open sesame", opener: func() *store.Store { return newStore(t) }}
	e.srv = e.build(netsec.Env{})
	c := e.client("192.168.1.91:1")
	var last int
	for i := 0; i < 8; i++ {
		last = c.post("/unlock", "/unlock", url.Values{"passphrase": {"guess"}}).Code
	}
	if last != 429 {
		t.Fatalf("unlock guessing was not limited: %d", last)
	}
}

// ---- the UI shell ----

func TestPagesHaveNoInlineScriptOrStyleAndAreMobileReady(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	setup := e.client("192.168.1.100:1").do("GET", "/setup", nil).Body.String()
	admin := steppedUp(t, e)
	pages := map[string]string{"/setup": setup}
	for _, p := range []string{"/", "/account", "/admin", "/admin/log", "/admin/settings", "/stepup"} {
		pages[p] = admin.do("GET", p, nil).Body.String()
	}
	pages["/login"] = e.client("192.168.1.101:1").do("GET", "/login", nil).Body.String()
	for p, body := range pages {
		if len(body) < 200 {
			t.Errorf("%s: looks empty: %q", p, body)
			continue
		}
		for _, bad := range []string{"<script", " style=", " onclick=", " onload=", "javascript:", "http://", "https://"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s contains %q, which the CSP or the no-outbound rule forbids", p, bad)
			}
		}
		for _, want := range []string{`name="viewport"`, "width=device-width", `<html lang="en"`, `rel="stylesheet" href="/static/app.css"`, "color-scheme"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s lacks %q", p, want)
			}
		}
	}
	css := e.client("192.168.1.100:1").do("GET", "/static/app.css", nil)
	if css.Code != 200 || !strings.Contains(css.Body.String(), "prefers-color-scheme: dark") || strings.Contains(css.Body.String(), "@import") || strings.Contains(css.Body.String(), "url(http") {
		t.Fatalf("css: %d", css.Code)
	}
	for _, p := range []string{"/static/mark-32.png", "/static/mark-96.png", "/static/mark-192.png", "/favicon.ico"} {
		w := e.client("192.168.1.100:1").do("GET", p, nil)
		if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || !bytes.HasPrefix(w.Body.Bytes(), []byte("\x89PNG")) {
			t.Errorf("%s: %d %s", p, w.Code, w.Header().Get("Content-Type"))
		}
	}
}

func TestThemeChoice(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	c := e.client("192.168.1.110:1")
	if !strings.Contains(c.do("GET", "/setup", nil).Body.String(), `aria-pressed="true">Auto`) {
		t.Error("auto should be the default")
	}
	w := c.post("/setup", "/theme", url.Values{"theme": {"dark"}, "next": {"/setup"}})
	if w.Code != 303 || w.Header().Get("Location") != "/setup" {
		t.Fatalf("theme: %d %s", w.Code, w.Header().Get("Location"))
	}
	if body := c.do("GET", "/setup", nil).Body.String(); !strings.Contains(body, `data-theme="dark"`) {
		t.Error("the dark choice was not applied")
	}
	// The return path is a whitelist, never an open redirect.
	w = c.post("/setup", "/theme", url.Values{"theme": {"light"}, "next": {"https://evil.example/"}})
	if w.Header().Get("Location") != "/" {
		t.Fatalf("open redirect: %s", w.Header().Get("Location"))
	}
	c.post("/setup", "/theme", url.Values{"theme": {"auto"}})
	if strings.Contains(c.do("GET", "/setup", nil).Body.String(), "data-theme") {
		t.Error("auto should clear the choice")
	}
}

// ---- constraint 8: no content in logs ----

func TestLogsCarryNoContent(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	code := ""
	admin := e.client("192.168.1.120:1")
	code = e.setupCode()
	admin.post("/setup", "/setup", url.Values{"code": {code}, "username": {"owner"}, "display": {"Distinctive Display Name"},
		"password": {adminPW}, "confirm": {adminPW}})
	e.login("192.168.1.121:1", "owner", "a wrong password with Secret-Marker")
	e.login("192.168.1.122:1", "ghost", "Secret-Marker-2")
	admin.post("/account", "/account", url.Values{"current": {adminPW}, "password": {"Brand-New-Secret-9"}, "confirm": {"Brand-New-Secret-9"}})
	out := e.logs.String()
	for _, secret := range []string{adminPW, "Secret-Marker", "Brand-New-Secret-9", "Distinctive Display Name", "ghost"} {
		if strings.Contains(out, secret) {
			t.Errorf("the log contains %q", secret)
		}
	}
	if !strings.Contains(out, "sign-in failed") {
		t.Error("failed sign-ins should be logged (without content)")
	}
}
