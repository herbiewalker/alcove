package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/herbiewalker/alcove/internal/netsec"
	"github.com/herbiewalker/alcove/internal/trystbk/synth"
)

type formPart struct {
	name, value string // a plain field
	file        []byte // when non-nil this part is the file
	filename    string
}

// multipartBody builds a body with the parts in exactly the order given.
func multipartBody(t *testing.T, parts []formPart) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, p := range parts {
		if p.file != nil {
			w, err := mw.CreateFormFile(p.name, p.filename)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write(p.file)
			continue
		}
		if err := mw.WriteField(p.name, p.value); err != nil {
			t.Fatal(err)
		}
	}
	_ = mw.Close()
	return &buf, mw.FormDataContentType()
}

// upload posts a backup the way the page does: csrf, password, remember, then the file.
func (c *client) upload(file []byte, filename, password string, remember bool, ajax bool) *httptest.ResponseRecorder {
	c.e.t.Helper()
	parts := []formPart{{name: "csrf", value: c.token("/uploads")}, {name: "password", value: password}}
	if remember {
		parts = append(parts, formPart{name: "remember", value: "1"})
	}
	parts = append(parts, formPart{name: "file", file: file, filename: filename})
	return c.rawUpload(parts, ajax, nil)
}

func (c *client) rawUpload(parts []formPart, ajax bool, mod func(*http.Request)) *httptest.ResponseRecorder {
	c.e.t.Helper()
	body, ctype := multipartBody(c.e.t, parts)
	r := httptest.NewRequest("POST", "/uploads", body)
	r.RemoteAddr = c.remote
	r.Header.Set("Content-Type", ctype)
	if ajax {
		r.Header.Set("X-Requested-With", "alcove")
	}
	for k, v := range c.hdr {
		r.Header.Set(k, v)
	}
	for k, v := range c.cookies {
		r.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	if mod != nil {
		mod(r)
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

func synthFile(t *testing.T, o synth.Options) *synth.Result {
	t.Helper()
	res, err := synth.Build(o)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func (e *env) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.st.View(context.Background(), func(tx *sql.Tx) error { return tx.QueryRow(q, args...).Scan(&n) }); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// blobCount counts stored uploads only (not the photos copied out of them); anyBlobCount counts every file.
func (e *env) blobCount() int { return e.countBlobs(true) }

func (e *env) anyBlobCount() int { return e.countBlobs(false) }

func (e *env) countBlobs(uploadsOnly bool) int {
	n := 0
	_ = filepath.WalkDir(filepath.Join(e.st.DataDir(), "blobs"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && (!uploadsOnly || filepath.Base(filepath.Dir(p)) == "u") {
			n++
		}
		return nil
	})
	return n
}

func (e *env) nothingStored() {
	e.t.Helper()
	if n := e.count("SELECT COUNT(*) FROM upload"); n != 0 {
		e.t.Errorf("uploads recorded: %d", n)
	}
	if n := e.anyBlobCount(); n != 0 {
		e.t.Errorf("blobs left behind: %d", n)
	}
	if n := e.count("SELECT COUNT(*) FROM backup_password"); n != 0 {
		e.t.Errorf("a password was stored by a refused upload")
	}
}

type reply struct {
	OK       bool   `json:"ok"`
	Message  string `json:"message"`
	Redirect string `json:"redirect"`
}

func jsonReply(t *testing.T, w *httptest.ResponseRecorder) reply {
	t.Helper()
	var r reply
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
		t.Fatalf("not JSON (%d): %.200s", w.Code, w.Body.String())
	}
	return r
}

func TestUploadHappyPathPlainForm(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := e.setupAdmin()
	e.addMember("sam", "sam has a long password")
	sam, _ := e.login("192.168.1.50:1", "sam", "sam has a long password")

	res := synthFile(t, synth.Options{Seed: 21, Encounters: 7, Photos: 1, Password: "Sam-Backup-Password-1"})
	w := sam.upload(res.File, "../../my phone/Tryst Backup.tryst", res.Password, true, false)
	if w.Code != 303 || w.Header().Get("Location") != "/uploads" {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	page := sam.do("GET", "/uploads", nil).Body.String()
	for _, want := range []string{"Backup uploaded and checked", "Tryst Backup.tryst", "<b>current</b>", ">7<"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if strings.Contains(page, "my phone") {
		t.Error("the path part of the file name was kept")
	}
	if home := sam.do("GET", "/", nil).Body.String(); !strings.Contains(home, "<b>7</b> entries") || !strings.Contains(home, "<b>1</b> photos") {
		t.Errorf("home does not show the data: %.300s", home)
	}
	// Isolation: nobody else sees it.
	if body := admin.do("GET", "/uploads", nil).Body.String(); !strings.Contains(body, "Nothing uploaded yet") {
		t.Error("another account saw this upload")
	}
	// What was stored is the file, and the password is not in any log (constraint 8).
	if e.count("SELECT COUNT(*) FROM upload") != 1 || e.blobCount() != 1 {
		t.Fatal("expected one upload and one blob")
	}
	out := e.logs.String()
	for _, secret := range []string{"Sam-Backup-Password-1", "Tryst Backup.tryst", "Synthetic"} {
		if strings.Contains(out, secret) {
			t.Errorf("the log contains %q", secret)
		}
	}
	if !strings.Contains(out, "upload accepted") {
		t.Error("an accepted upload should leave a line with counts")
	}
}

func TestUploadAjaxAndStoredPassword(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	c := e.setupAdmin()
	a := synthFile(t, synth.Options{Seed: 22, Password: "First-Backup-Password"})
	r := jsonReply(t, c.upload(a.File, "a.tryst", a.Password, true, true))
	if !r.OK || r.Redirect != "/uploads" {
		t.Fatalf("ajax upload: %+v", r)
	}
	// Second, different file with the password field left empty: the remembered one is used.
	b := synthFile(t, synth.Options{Seed: 23, Password: "First-Backup-Password", Encounters: 3})
	w := c.upload(b.File, "b.tryst", "", true, true)
	if r := jsonReply(t, w); !r.OK {
		t.Fatalf("upload with the remembered password: %+v", r)
	}
	if e.count("SELECT COUNT(*) FROM revision") != 2 {
		t.Fatal("expected two revisions")
	}
	// With nothing remembered and no password typed, it asks.
	c2 := e.client("192.168.1.51:1")
	e.addMember("kim", "kim has a long password")
	kim, _ := e.login("192.168.1.52:1", "kim", "kim has a long password")
	_ = c2
	w = kim.upload(b.File, "b.tryst", "", true, true)
	if w.Code != 400 || !strings.Contains(jsonReply(t, w).Message, "Enter the password") {
		t.Fatalf("no password anywhere: %d %s", w.Code, w.Body.String())
	}
}

func TestUploadRefusalsSaveNothing(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	c := e.setupAdmin()
	res := synthFile(t, synth.Options{Seed: 24, Password: "Right-Password-1"})

	w := c.upload(res.File, "x.tryst", "wrong password here", true, true)
	if w.Code != 401 || !strings.Contains(jsonReply(t, w).Message, "not the password") {
		t.Fatalf("wrong password: %d %s", w.Code, w.Body.String())
	}
	w = c.upload([]byte("this is just a text file, long enough to look like something"), "x.txt", res.Password, true, true)
	if w.Code != 422 || !strings.Contains(jsonReply(t, w).Message, "not a Tryst backup") {
		t.Fatalf("not a backup: %d %s", w.Code, w.Body.String())
	}
	bad := append([]byte(nil), res.File...)
	bad[len(bad)/2] ^= 0xff
	if w = c.upload(bad, "x.tryst", res.Password, true, true); w.Code < 400 {
		t.Fatalf("a damaged backup was accepted: %d", w.Code)
	}
	if w = c.upload(res.File, "x.tryst", res.Password, true, false); w.Code != 303 {
		t.Fatalf("the good file after all that: %d %s", w.Code, w.Body.String())
	}
	e.logs.b.Reset()
	// Duplicate.
	w = c.upload(res.File, "again.tryst", res.Password, true, true)
	if w.Code != 409 || !strings.Contains(jsonReply(t, w).Message, "already uploaded") {
		t.Fatalf("duplicate: %d %s", w.Code, w.Body.String())
	}
	if e.count("SELECT COUNT(*) FROM upload") != 1 || e.blobCount() != 1 {
		t.Fatal("the duplicate left something behind")
	}
	// The plain-form refusal is a readable page, not JSON.
	w = c.upload(res.File, "again.tryst", res.Password, true, false)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "The upload was not saved") {
		t.Fatalf("plain refusal: %d", w.Code)
	}
	// And on a fresh server nothing at all remains from the refusals.
	e2 := newEnv(t, netsec.Env{})
	c2 := e2.setupAdmin()
	c2.upload(res.File, "x.tryst", "wrong password here", true, true)
	c2.upload([]byte("not a backup, just some text that is long enough"), "x", res.Password, true, true)
	e2.nothingStored()
}

// The token is checked before a byte of the file is accepted.
func TestUploadChecksTheTokenFirst(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	c := e.setupAdmin()
	res := synthFile(t, synth.Options{Seed: 25})
	good := c.token("/uploads")
	file := formPart{name: "file", file: res.File, filename: "x.tryst"}
	pw := formPart{name: "password", value: res.Password}

	cases := map[string][]formPart{
		"no token":           {pw, file},
		"wrong token":        {{name: "csrf", value: "nope"}, pw, file},
		"token after file":   {pw, file, {name: "csrf", value: good}},
		"file before fields": {file, {name: "csrf", value: good}, pw},
	}
	for label, parts := range cases {
		w := c.rawUpload(parts, true, nil)
		if w.Code != 403 {
			t.Errorf("%s: %d %s", label, w.Code, w.Body.String())
		}
		if w.Header().Get("Connection") != "close" {
			t.Errorf("%s: the refused connection should close", label)
		}
	}
	e.nothingStored()

	w := c.rawUpload([]formPart{{name: "csrf", value: good}, pw, file}, true, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") })
	if w.Code != 403 {
		t.Errorf("cross-site: %d", w.Code)
	}
	e.nothingStored()

	// Anonymous callers never reach the handler.
	anon := e.client("192.168.1.99:1")
	if w := anon.rawUpload([]formPart{{name: "csrf", value: "x"}, pw, file}, true, nil); w.Code != 303 || w.Header().Get("Location") != "/login" {
		t.Errorf("anonymous upload: %d %s", w.Code, w.Header().Get("Location"))
	}
	e.nothingStored()

	// A well-formed upload with the token first works (the control).
	if w := c.rawUpload([]formPart{{name: "csrf", value: good}, pw, file}, true, nil); w.Code != 200 || !jsonReply(t, w).OK {
		t.Fatalf("control upload: %d %s", w.Code, w.Body.String())
	}
}

func TestUploadSizeLimits(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	e.maxUpload = 1 << 20
	e.srv = e.build(netsec.Env{})
	c := e.setupAdmin()
	big := bytes.Repeat([]byte{7}, 3<<20)

	// A declared size over the cap is refused before anything is read.
	w := c.upload(big, "big.tryst", "pw", true, true)
	if w.Code != 413 || !strings.Contains(jsonReply(t, w).Message, "1 MiB") {
		t.Fatalf("declared oversize: %d %s", w.Code, w.Body.String())
	}
	// Without a declared length (chunked) the stream is cut off at the cap.
	parts := []formPart{{name: "csrf", value: c.token("/uploads")}, {name: "password", value: "pw"}, {name: "file", file: big, filename: "big.tryst"}}
	w = c.rawUpload(parts, true, func(r *http.Request) { r.ContentLength = -1 })
	if w.Code != 413 {
		t.Fatalf("chunked oversize: %d %s", w.Code, w.Body.String())
	}
	e.nothingStored()
}

func TestUploadInterruptedLeavesNothing(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	c := e.setupAdmin()
	res := synthFile(t, synth.Options{Seed: 26})
	parts := []formPart{{name: "csrf", value: c.token("/uploads")}, {name: "password", value: res.Password}, {name: "file", file: res.File, filename: "x.tryst"}}
	body, ctype := multipartBody(t, parts)
	cut := body.Bytes()[:body.Len()/2] // the connection dropped half way
	r := httptest.NewRequest("POST", "/uploads", bytes.NewReader(cut))
	r.RemoteAddr = c.remote
	r.Header.Set("Content-Type", ctype)
	r.Header.Set("X-Requested-With", "alcove")
	for k, v := range c.cookies {
		r.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	w := httptest.NewRecorder()
	e.srv.ServeHTTP(w, r)
	if w.Code < 400 {
		t.Fatalf("a cut-off upload was accepted: %d %s", w.Code, w.Body.String())
	}
	e.nothingStored()
}

func TestRebuildFromTheAdminPage(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := steppedUp(t, e)
	admin.post("/stepup", "/stepup", url.Values{"password": {adminPW}, "next": {"/admin/backup"}})
	res := synthFile(t, synth.Options{Seed: 27, Encounters: 6})
	if w := admin.upload(res.File, "a.tryst", res.Password, true, true); !jsonReply(t, w).OK {
		t.Fatalf("upload: %s", w.Body.String())
	}
	before := e.count("SELECT COUNT(*) FROM cache_row")
	_ = e.st.Update(context.Background(), true, func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM cache_row; DELETE FROM search; DELETE FROM cache_head`)
		return err
	})
	if e.count("SELECT COUNT(*) FROM cache_row") != 0 {
		t.Fatal("the test did not wreck the cache")
	}
	if w := admin.post("/admin/backup", "/admin/backup/rebuild", nil); w.Code != 303 {
		t.Fatalf("rebuild: %d %s", w.Code, w.Body.String())
	}
	if body := waitJob(t, admin); !strings.Contains(body, "Rebuilt the search index and cache for") {
		t.Fatalf("rebuild result: %s", body)
	}
	if e.count("SELECT COUNT(*) FROM cache_row") != before || e.count("SELECT COUNT(*) FROM search") == 0 {
		t.Error("the rebuild did not restore the cache and the search index")
	}
	if !has(e.logActions(), "cache.rebuild") {
		t.Error("the rebuild is not in the admin log")
	}
	// A member cannot trigger it.
	e.addMember("sam", "sam has a long password")
	sam, _ := e.login("192.168.1.60:1", "sam", "sam has a long password")
	if w := sam.post("/account", "/admin/backup/rebuild", nil); w.Code != 404 {
		t.Errorf("member rebuild: %d", w.Code)
	}
}

var scriptRE = regexp.MustCompile(`<script[^>]*>`)

// oneScript checks that a page carries exactly one script, the same-origin ui.js, and no inline script.
func oneScript(t *testing.T, name, body string) {
	t.Helper()
	got := scriptRE.FindAllString(body, -1)
	if len(got) != 1 || !strings.Contains(got[0], `src="/static/ui.js"`) || !strings.Contains(got[0], "defer") {
		t.Errorf("%s: scripts = %v, want exactly the one ui.js", name, got)
	}
	if strings.Contains(body, "onclick=") || strings.Contains(body, "onsubmit=") || strings.Contains(body, " style=") {
		t.Errorf("%s: an inline handler or style", name)
	}
}

func TestEveryPageUsesTheOneSameOriginScriptUnderACleanCSP(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	c := e.setupAdmin()
	w := c.do("GET", "/uploads", nil)
	oneScript(t, "/uploads", w.Body.String())
	csp := w.Header().Get("Content-Security-Policy")
	for _, want := range []string{"script-src 'self'", "connect-src 'self'", "default-src 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP lacks %q: %s", want, csp)
		}
	}
	if strings.Contains(csp, "unsafe") || strings.Contains(csp, "http:") || strings.Contains(csp, "https:") || strings.Contains(csp, "*") {
		t.Errorf("CSP is too loose: %s", csp)
	}
	// Every page has the same single script: N11 (at most one), now on every page (D-36).
	for _, p := range []string{"/", "/account", "/admin", "/admin/log", "/admin/settings", "/admin/backup", "/spaces", "/profile", "/entries", "/search"} {
		oneScript(t, p, c.do("GET", p, nil).Body.String())
	}
	oneScript(t, "/login", e.client("192.168.1.77:1").do("GET", "/login", nil).Body.String())
	js := c.do("GET", "/static/ui.js", nil)
	if js.Code != 200 || !strings.HasPrefix(js.Header().Get("Content-Type"), "text/javascript") || !strings.Contains(js.Body.String(), "XMLHttpRequest") {
		t.Fatalf("ui.js: %d %s", js.Code, js.Header().Get("Content-Type"))
	}
	src := js.Body.String()
	for _, bad := range []string{"eval(", "innerHTML", "document.write", "localStorage", "sessionStorage", "fetch(", "http://", "https://", "WebSocket", "sendBeacon"} {
		if strings.Contains(src, bad) {
			t.Errorf("ui.js uses %s", bad)
		}
	}
	// The one cookie it touches is the download marker, never anything else.
	for _, line := range strings.Split(src, "\n") {
		if strings.Contains(line, "document.cookie") && !strings.Contains(line, "alcove_dl") {
			t.Errorf("ui.js touches a cookie other than alcove_dl: %s", strings.TrimSpace(line))
		}
	}
	// The script never touches the password field.
	if strings.Contains(src, "password") && !strings.Contains(src, "never touches a password") {
		t.Error("ui.js refers to the password")
	}
	// The old file is gone.
	if c.do("GET", "/static/upload.js", nil).Code != 404 {
		t.Error("the old upload.js is still served")
	}
}

type countingBody struct {
	r io.Reader
	n int
}

func (c *countingBody) Read(p []byte) (int, error) { n, err := c.r.Read(p); c.n += n; return n, err }
func (c *countingBody) Close() error               { return nil }

// A declared size over the cap is refused FAST: not one byte of the body is read.
func TestOversizeUploadIsRefusedWithoutReadingIt(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	e.maxUpload = 1 << 20
	e.srv = e.build(netsec.Env{})
	c := e.setupAdmin()
	big := bytes.Repeat([]byte{9}, 4<<20)
	parts := []formPart{{name: "csrf", value: c.token("/uploads")}, {name: "password", value: "pw"}, {name: "file", file: big, filename: "big.tryst"}}
	var cb *countingBody
	w := c.rawUpload(parts, true, func(r *http.Request) {
		cb = &countingBody{r: r.Body}
		r.Body = cb
	})
	if w.Code != 413 {
		t.Fatalf("status %d", w.Code)
	}
	if cb.n != 0 {
		t.Fatalf("the server read %d bytes of a body it should have refused up front", cb.n)
	}
}
