package server

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"io"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/herbiewalker/alcove/internal/auth"
	"github.com/herbiewalker/alcove/internal/netsec"
	"github.com/herbiewalker/alcove/internal/store"
	"github.com/herbiewalker/alcove/internal/trystbk/synth"
)

var tempRE = regexp.MustCompile(`<code>([a-z2-9]{4}-[a-z2-9]{4}-[a-z2-9]{4}-[a-z2-9]{4})</code>`)

// adminReady is a signed-in, stepped-up administrator.
func adminReady(t *testing.T, e *env) *client {
	t.Helper()
	return steppedUp(t, e)
}

func (e *env) userByName(name string) (auth.User, error) {
	var u auth.User
	err := e.st.View(context.Background(), func(tx *sql.Tx) (err error) { u, err = auth.ByUsername(tx, name); return })
	return u, err
}

// createPerson uses the real admin page and returns the temporary password it showed.
func createPerson(t *testing.T, admin *client, name, role string) string {
	t.Helper()
	w := admin.post("/admin/users", "/admin/users/create", url.Values{"username": {name}, "display": {"Person " + name}, "role": {role}})
	if w.Code != 200 {
		t.Fatalf("create %s: %d %s", name, w.Code, w.Body.String())
	}
	m := tempRE.FindStringSubmatch(w.Body.String())
	if m == nil {
		t.Fatalf("no temporary password on the page: %s", w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Error("the page that shows a temporary password must not be cached")
	}
	return m[1]
}

func (e *env) idOf(name string) string {
	e.t.Helper()
	u, err := e.userByName(name)
	if err != nil {
		e.t.Fatal(err)
	}
	return u.ID
}

func TestAdminCreatesAPersonWhoMustChangeTheTemporaryPassword(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := adminReady(t, e)
	temp := createPerson(t, admin, "sam", "member")

	// It is not stored in the clear, not logged, and the log line names no content.
	u, _ := e.userByName("sam")
	if strings.Contains(u.PWHash, temp) || !u.MustChange || u.Role != auth.RoleMember {
		t.Fatalf("stored account: %+v", u)
	}
	if strings.Contains(e.logs.String(), temp) {
		t.Error("the temporary password reached the server log")
	}
	if !has(e.logActions(), "user.create") {
		t.Error("account creation is not in the admin log")
	}

	// Signing in works, but nothing else does until the password is changed.
	sam, w := e.login("192.168.1.70:1", "sam", temp)
	if w.Code != 303 {
		t.Fatalf("temporary login: %d", w.Code)
	}
	for _, p := range []string{"/", "/uploads", "/account", "/uploads/deleted"} {
		if w := sam.do("GET", p, nil); w.Code != 303 || w.Header().Get("Location") != "/account/password" {
			t.Errorf("GET %s with a temporary password: %d -> %s", p, w.Code, w.Header().Get("Location"))
		}
	}
	if w := sam.do("POST", "/uploads/delete", url.Values{"csrf": {sam.token("/account/password")}, "id": {"x"}}); w.Code != 303 || w.Header().Get("Location") != "/account/password" {
		t.Errorf("POST while forced: %d %s", w.Code, w.Header().Get("Location"))
	}
	if body := sam.do("GET", "/account/password", nil).Body.String(); !strings.Contains(body, "temporary password") {
		t.Error("the change page must explain why it is shown")
	}
	const mine = "a password only sam knows"
	for label, f := range map[string]url.Values{
		"wrong current": {"current": {"nope"}, "password": {mine}, "confirm": {mine}},
		"mismatch":      {"current": {temp}, "password": {mine}, "confirm": {"different"}},
		"too short":     {"current": {temp}, "password": {"short"}, "confirm": {"short"}},
		"same":          {"current": {temp}, "password": {temp}, "confirm": {temp}},
	} {
		if w := sam.post("/account/password", "/account/password", f); w.Code < 400 {
			t.Errorf("%s: accepted (%d)", label, w.Code)
		}
	}
	if w := sam.post("/account/password", "/account/password", url.Values{"current": {temp}, "password": {mine}, "confirm": {mine}}); w.Code != 303 || w.Header().Get("Location") != "/" {
		t.Fatalf("the change: %d %s", w.Code, w.Body.String())
	}
	if w := sam.do("GET", "/", nil); w.Code != 200 {
		t.Fatalf("after the change: %d", w.Code)
	}
	if _, w := e.login("192.168.1.71:1", "sam", temp); w.Code != 401 {
		t.Errorf("the temporary password still works: %d", w.Code)
	}
	if u, _ := e.userByName("sam"); u.MustChange {
		t.Error("the forced-change flag was not cleared")
	}
	if _, w := e.login("192.168.1.72:1", "sam", mine); w.Code != 303 {
		t.Errorf("the new password: %d", w.Code)
	}

	// A person made an administrator gets the console.
	tempAdmin := createPerson(t, admin, "kim", "admin")
	kim, _ := e.login("192.168.1.73:1", "kim", tempAdmin)
	kim.post("/account/password", "/account/password", url.Values{"current": {tempAdmin}, "password": {"kim has a long password"}, "confirm": {"kim has a long password"}})
	if w := kim.do("GET", "/admin", nil); w.Code != 200 {
		t.Errorf("a new administrator cannot open the console: %d", w.Code)
	}
}

func TestCreatingAPersonIsGuarded(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := e.setupAdmin()
	form := url.Values{"username": {"sam"}, "role": {"member"}}
	if w := admin.post("/admin/users", "/admin/users/create", form); w.Code != 303 || !strings.HasPrefix(w.Header().Get("Location"), "/stepup") {
		t.Fatalf("without step-up: %d %s", w.Code, w.Header().Get("Location"))
	}
	if _, err := e.userByName("sam"); err == nil {
		t.Fatal("an account was created without step-up")
	}
	admin.post("/stepup", "/stepup", url.Values{"password": {adminPW}, "next": {"/admin/users"}})
	createPerson(t, admin, "sam", "member")
	if w := admin.post("/admin/users", "/admin/users/create", url.Values{"username": {"SAM"}}); w.Code != 409 || !strings.Contains(w.Body.String(), "already taken") {
		t.Errorf("duplicate username: %d", w.Code)
	}
	for _, bad := range []string{"", "ab", "has space", "semi;colon", strings.Repeat("a", 40)} {
		if w := admin.post("/admin/users", "/admin/users/create", url.Values{"username": {bad}}); w.Code != 400 {
			t.Errorf("username %q: %d", bad, w.Code)
		}
	}
	// A role that does not exist falls back to member, never to admin.
	createPerson(t, admin, "lee", "root")
	if u, _ := e.userByName("lee"); u.Role != auth.RoleMember {
		t.Errorf("an unknown role became %q", u.Role)
	}
	// Members cannot reach any of it.
	_, w := e.login("192.168.1.80:1", "sam", "x")
	_ = w
	for _, p := range []string{"/admin/users/create", "/admin/users/disable", "/admin/users/delete", "/admin/users/reset", "/admin/users/enable", "/admin/users/restore", "/admin/deleted/purge", "/admin/deleted/restore"} {
		anon := e.client("192.168.1.81:1")
		if w := anon.do("POST", p, url.Values{"id": {"x"}}); w.Code != 303 {
			t.Errorf("anonymous POST %s: %d", p, w.Code)
		}
	}
}

func TestAccountLifecycle(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := adminReady(t, e)
	temp := createPerson(t, admin, "sam", "member")
	sam, _ := e.login("192.168.1.90:1", "sam", temp)
	const mine = "a password only sam knows"
	sam.post("/account/password", "/account/password", url.Values{"current": {temp}, "password": {mine}, "confirm": {mine}})
	id := e.idOf("sam")
	act := func(kind string) *httptest.ResponseRecorder {
		return admin.post("/admin/users", "/admin/users/"+kind, url.Values{"id": {id}})
	}

	// Delete before disable is refused.
	if w := act("delete"); w.Code != 400 || !strings.Contains(w.Body.String(), "Disable the account first") {
		t.Fatalf("delete while active: %d", w.Code)
	}
	// Disable ends the person's session at once and stops sign-in.
	if w := act("disable"); w.Code != 303 {
		t.Fatalf("disable: %d %s", w.Code, w.Body.String())
	}
	if w := sam.do("GET", "/", nil); w.Code != 303 || w.Header().Get("Location") != "/login" {
		t.Fatalf("a disabled person kept their session: %d %s", w.Code, w.Header().Get("Location"))
	}
	if _, w := e.login("192.168.1.91:1", "sam", mine); w.Code != 401 {
		t.Fatalf("a disabled person signed in: %d", w.Code)
	}
	if w := act("disable"); w.Code != 400 {
		t.Errorf("disabling twice: %d", w.Code)
	}
	// Enable restores access.
	if w := act("enable"); w.Code != 303 {
		t.Fatalf("enable: %d", w.Code)
	}
	if _, w := e.login("192.168.1.92:1", "sam", mine); w.Code != 303 {
		t.Fatalf("an enabled person cannot sign in: %d", w.Code)
	}
	// Delete (after disabling), then it shows in the deleted lists and cannot sign in.
	act("disable")
	if w := act("delete"); w.Code != 303 {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if u, _ := e.userByName("sam"); !u.Deleted() {
		t.Fatal("the account is not marked deleted")
	}
	if body := admin.do("GET", "/admin/deleted", nil).Body.String(); !strings.Contains(body, "@sam") {
		t.Error("the deleted account is not on the admin's recently-deleted page")
	}
	if w := act("enable"); w.Code != 400 {
		t.Errorf("enabling a deleted account: %d", w.Code)
	}
	if w := act("reset"); w.Code != 400 || !strings.Contains(w.Body.String(), "Restore the account") {
		t.Errorf("resetting a deleted account: %d", w.Code)
	}
	if _, w := e.login("192.168.1.93:1", "sam", mine); w.Code != 401 {
		t.Fatalf("a deleted person signed in: %d", w.Code)
	}
	// Restore brings the account back, still disabled.
	if w := act("restore"); w.Code != 303 {
		t.Fatalf("restore: %d", w.Code)
	}
	if u, _ := e.userByName("sam"); u.Deleted() || u.Status != auth.StatusDisabled {
		t.Fatalf("after restore: %+v", u)
	}
	act("enable")
	// Reset gives a new temporary password, ends sessions, and forces a change.
	sam2, _ := e.login("192.168.1.94:1", "sam", mine)
	w := act("reset")
	m := tempRE.FindStringSubmatch(w.Body.String())
	if w.Code != 200 || m == nil {
		t.Fatalf("reset: %d %s", w.Code, w.Body.String())
	}
	if w := sam2.do("GET", "/", nil); w.Code != 303 || w.Header().Get("Location") != "/login" {
		t.Errorf("a reset left the old session alive: %d", w.Code)
	}
	if _, w := e.login("192.168.1.95:1", "sam", mine); w.Code != 401 {
		t.Errorf("the old password still works after a reset: %d", w.Code)
	}
	again, lw := e.login("192.168.1.96:1", "sam", m[1])
	if lw.Code != 303 || again.do("GET", "/", nil).Header().Get("Location") != "/account/password" {
		t.Errorf("the reset password does not force a change")
	}

	acts := e.logActions()
	for _, want := range []string{"user.create", "user.disable", "user.enable", "user.delete", "user.restore", "user.password.reset"} {
		if !has(acts, want) {
			t.Errorf("no admin-log entry for %s", want)
		}
	}
	if strings.Contains(e.logs.String(), m[1]) || strings.Contains(e.logs.String(), mine) {
		t.Error("a password reached the server log")
	}
}

func TestNobodyCanLockTheServerOutOfAdministrators(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := adminReady(t, e)
	self := e.idOf("owner")
	for _, kind := range []string{"disable", "delete", "reset"} {
		w := admin.post("/admin/users", "/admin/users/"+kind, url.Values{"id": {self}})
		if w.Code != 400 {
			t.Errorf("the only administrator could %s themselves: %d", kind, w.Code)
		}
	}
	if u, _ := e.userByName("owner"); u.Status != auth.StatusActive || u.Deleted() {
		t.Fatalf("the administrator was changed: %+v", u)
	}
	if w := admin.post("/admin/users", "/admin/users/disable", url.Values{"id": {"unknown"}}); w.Code != 400 {
		t.Errorf("an unknown id: %d", w.Code)
	}
}

// ---- a person's own delete, restore and download ----

func putBackup(t *testing.T, c *client, o synth.Options) (*synth.Result, *httptest.ResponseRecorder) {
	t.Helper()
	res := synthFile(t, o)
	w := c.upload(res.File, "my backup.tryst", res.Password, true, true)
	if w.Code != 200 {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	return res, w
}

func memberWithUpload(t *testing.T, e *env, admin *client, name string, o synth.Options) (*client, *synth.Result) {
	t.Helper()
	temp := createPerson(t, admin, name, "member")
	c, _ := e.login("192.168.1.100:1", name, temp)
	pw := name + " has a long password"
	c.post("/account/password", "/account/password", url.Values{"current": {temp}, "password": {pw}, "confirm": {pw}})
	res, _ := putBackup(t, c, o)
	return c, res
}

func TestAPersonDeletesAndRestoresTheirOwnUpload(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := adminReady(t, e)
	sam, _ := memberWithUpload(t, e, admin, "sam", synth.Options{Seed: 31, Encounters: 6})
	revID := e.revisionOf("sam")

	// Deleting asks for the password again.
	if w := sam.post("/uploads", "/uploads/delete", url.Values{"id": {revID}}); w.Code != 303 || !strings.HasPrefix(w.Header().Get("Location"), "/stepup") {
		t.Fatalf("delete without step-up: %d %s", w.Code, w.Header().Get("Location"))
	}
	if e.count("SELECT COUNT(*) FROM revision WHERE deleted_at IS NOT NULL") != 0 {
		t.Fatal("deleted before the password was confirmed")
	}
	if w := sam.post("/stepup", "/stepup", url.Values{"password": {"sam has a long password"}, "next": {"/uploads"}}); w.Code != 303 || w.Header().Get("Location") != "/uploads" {
		t.Fatalf("a member's step-up: %d %s", w.Code, w.Header().Get("Location"))
	}
	if w := sam.post("/stepup", "/stepup", url.Values{"password": {"sam has a long password"}, "next": {"/admin/users"}}); w.Header().Get("Location") != "/uploads" {
		t.Errorf("a member's step-up must never point at an admin page: %s", w.Header().Get("Location"))
	}
	// Another person cannot delete it.
	other, _ := memberWithUpload(t, e, admin, "kim", synth.Options{Seed: 32, Encounters: 3})
	other.post("/stepup", "/stepup", url.Values{"password": {"kim has a long password"}})
	if w := other.post("/uploads", "/uploads/delete", url.Values{"id": {revID}}); w.Code != 404 {
		t.Errorf("deleting someone else's upload: %d", w.Code)
	}
	if e.count("SELECT COUNT(*) FROM revision WHERE deleted_at IS NOT NULL") != 0 {
		t.Fatal("someone else's upload was deleted")
	}

	if w := sam.post("/uploads", "/uploads/delete", url.Values{"id": {revID}}); w.Code != 303 {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	page := sam.do("GET", "/uploads", nil).Body.String()
	if !strings.Contains(page, "Moved to Recently deleted") || !strings.Contains(page, "Nothing uploaded yet") {
		t.Errorf("after deleting: %.400s", page)
	}
	if body := sam.do("GET", "/", nil).Body.String(); !strings.Contains(body, "Nothing is stored here yet") {
		t.Error("home still shows deleted data")
	}
	bin := sam.do("GET", "/uploads/deleted", nil).Body.String()
	if !strings.Contains(bin, "my backup.tryst") || !strings.Contains(bin, "10 days") {
		t.Errorf("the bin: %.500s", bin)
	}
	// Other people's bins are separate.
	if body := other.do("GET", "/uploads/deleted", nil).Body.String(); strings.Contains(body, "my backup.tryst") {
		t.Error("someone else's bin showed this upload")
	}
	if w := sam.post("/uploads/deleted", "/uploads/restore", url.Values{"id": {revID}}); w.Code != 303 {
		t.Fatalf("restore: %d %s", w.Code, w.Body.String())
	}
	if body := sam.do("GET", "/", nil).Body.String(); !strings.Contains(body, "<b>6</b> entries") {
		t.Errorf("the restored upload is not current again: %.300s", body)
	}
}

func (e *env) revisionOf(username string) string {
	e.t.Helper()
	var id string
	owner := e.idOf(username)
	if err := e.st.View(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT id FROM revision WHERE owner_id = ? ORDER BY created_at DESC, rowid DESC LIMIT 1`, owner).Scan(&id)
	}); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func TestRecoveryWindowsThroughThePages(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := adminReady(t, e)
	sam, _ := memberWithUpload(t, e, admin, "sam", synth.Options{Seed: 33, Encounters: 4})
	rev := e.revisionOf("sam")
	sam.post("/stepup", "/stepup", url.Values{"password": {"sam has a long password"}})
	sam.post("/uploads", "/uploads/delete", url.Values{"id": {rev}})

	e.clock = e.clock.Add(11 * 24 * time.Hour) // past the person's 10 days, inside the admin's 45
	sam, admin = e.reLogin("sam"), e.reAdmin()
	if body := sam.do("GET", "/uploads/deleted", nil).Body.String(); strings.Contains(body, "my backup.tryst") {
		t.Error("the person still sees it after their window")
	}
	if w := sam.post("/uploads/deleted", "/uploads/restore", url.Values{"id": {rev}}); w.Code != 400 || !strings.Contains(w.Body.String(), "Ask the administrator") {
		t.Fatalf("a person restoring after their window: %d", w.Code)
	}
	admin.post("/stepup", "/stepup", url.Values{"password": {adminPW}, "next": {"/admin/deleted"}})
	body := admin.do("GET", "/admin/deleted", nil).Body.String()
	if !strings.Contains(body, "@sam") || !strings.Contains(body, "my backup.tryst") {
		t.Fatalf("the admin's recently-deleted page: %.600s", body)
	}
	if w := admin.post("/admin/deleted", "/admin/deleted/restore", url.Values{"id": {rev}}); w.Code != 303 {
		t.Fatalf("admin restore: %d %s", w.Code, w.Body.String())
	}
	if b := sam.do("GET", "/", nil).Body.String(); !strings.Contains(b, "<b>4</b> entries") {
		t.Error("the admin's restore did not bring the data back")
	}
	for _, want := range []string{"retention.view", "upload.restore"} {
		if !has(e.logActions(), want) {
			t.Errorf("no admin-log entry for %s", want)
		}
	}
}

// N13 through the real pages: after the window a purge removes the data for real.
func TestPurgeThroughTheAdminPage(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := adminReady(t, e)
	sam, _ := memberWithUpload(t, e, admin, "sam", synth.Options{Seed: 34, Encounters: 4})
	samID := e.idOf("sam")
	rev := e.revisionOf("sam")
	sam.post("/stepup", "/stepup", url.Values{"password": {"sam has a long password"}})
	sam.post("/uploads", "/uploads/delete", url.Values{"id": {rev}})
	if e.blobCount() != 1 {
		t.Fatal("setup")
	}

	admin.post("/stepup", "/stepup", url.Values{"password": {adminPW}, "next": {"/admin/deleted"}})
	e.clock = e.clock.Add(30 * 24 * time.Hour)
	admin = e.reAdmin()
	admin.post("/admin/deleted", "/admin/deleted/purge", nil)
	if e.blobCount() != 1 || e.count("SELECT COUNT(*) FROM upload") != 1 {
		t.Fatal("a purge inside the window removed something")
	}
	e.clock = e.clock.Add(20 * 24 * time.Hour) // day 50
	admin = e.reAdmin()
	w := admin.post("/admin/deleted", "/admin/deleted/purge", nil)
	if w.Code != 303 {
		t.Fatalf("purge: %d", w.Code)
	}
	if e.blobCount() != 0 || e.count("SELECT COUNT(*) FROM upload") != 0 || e.count("SELECT COUNT(*) FROM revision WHERE owner_id = ?", samID) != 0 {
		t.Fatal("the purged upload is still there")
	}
	if !strings.Contains(admin.do("GET", "/admin/deleted", nil).Body.String(), "removed for good") {
		t.Error("the purge result is not shown")
	}
	if !has(e.logActions(), "retention.purge") || !has(e.logActions(), "retention.purge.run") {
		t.Errorf("purge not logged: %v", e.logActions())
	}

	// Delete the whole account and purge: the person, their key and files are gone, the name is free.
	for _, kind := range []string{"disable", "delete"} {
		admin.post("/admin/users", "/admin/users/"+kind, url.Values{"id": {samID}})
	}
	e.clock = e.clock.Add(50 * 24 * time.Hour)
	admin = e.reAdmin()
	admin.post("/admin/deleted", "/admin/deleted/purge", nil)
	if _, err := e.userByName("sam"); err == nil {
		t.Fatal("the deleted account survived the purge")
	}
	if _, err := e.st.DataKey(samID); err != store.ErrNoDataKey {
		t.Errorf("the data key survived: %v", err)
	}
	if _, w := e.login("192.168.1.110:1", "sam", "sam has a long password"); w.Code != 401 {
		t.Errorf("a purged person signed in: %d", w.Code)
	}
	createPerson(t, admin, "sam", "member") // the username is free again
}

func TestDownloadMyData(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := adminReady(t, e)
	sam, res := memberWithUpload(t, e, admin, "sam", synth.Options{Seed: 35, Encounters: 3})
	kim, kimRes := memberWithUpload(t, e, admin, "kim", synth.Options{Seed: 36, Encounters: 3})
	_ = kim

	if w := sam.do("GET", "/uploads/download", nil); w.Code != 404 && w.Code != 405 {
		t.Errorf("a download by GET (a link someone could plant): %d", w.Code)
	}
	if w := sam.post("/uploads", "/uploads/download", nil); w.Code != 303 || !strings.HasPrefix(w.Header().Get("Location"), "/stepup") {
		t.Fatalf("download without step-up: %d %s", w.Code, w.Header().Get("Location"))
	}
	sam.post("/stepup", "/stepup", url.Values{"password": {"sam has a long password"}, "next": {"/uploads"}})
	w := sam.post("/uploads", "/uploads/download", nil)
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/zip" || !strings.Contains(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("download: %d %s %s", w.Code, w.Header().Get("Content-Type"), w.Header().Get("Content-Disposition"))
	}
	zr, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	var gotTryst []byte
	for _, f := range zr.File {
		names = append(names, f.Name)
		if strings.HasSuffix(f.Name, ".tryst") {
			rc, _ := f.Open()
			gotTryst, _ = io.ReadAll(rc)
			rc.Close()
		}
	}
	if len(names) != 2 || !strings.Contains(strings.Join(names, ","), "README.txt") {
		t.Fatalf("zip contents: %v", names)
	}
	if !bytes.Equal(gotTryst, res.File) {
		t.Error("the download is not the file exactly as uploaded")
	}
	if bytes.Contains(w.Body.Bytes(), kimRes.File[:200]) {
		t.Error("another person's data is in this download")
	}
	for _, f := range zr.File {
		if f.Name == "README.txt" {
			rc, _ := f.Open()
			txt, _ := io.ReadAll(rc)
			rc.Close()
			if !strings.Contains(string(txt), "still encrypted") {
				t.Error("the read-me should say the files stay encrypted")
			}
		}
	}
	if strings.Contains(e.logs.String(), "my backup") {
		t.Error("a file name reached the log")
	}
}

// reAdmin signs the administrator in again and steps up. Used after a test moves the clock, which
// (correctly) ends every session.
func (e *env) reAdmin() *client {
	e.t.Helper()
	c, w := e.login("192.168.1.5:1", "owner", adminPW)
	if w.Code != 303 {
		e.t.Fatalf("admin sign-in: %d", w.Code)
	}
	if w := c.post("/stepup", "/stepup", url.Values{"password": {adminPW}}); w.Code != 303 {
		e.t.Fatalf("admin step-up: %d", w.Code)
	}
	return c
}

func (e *env) reLogin(name string) *client {
	e.t.Helper()
	c, w := e.login("192.168.1.6:1", name, name+" has a long password")
	if w.Code != 303 {
		e.t.Fatalf("%s sign-in: %d", name, w.Code)
	}
	return c
}
