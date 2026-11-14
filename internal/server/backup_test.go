package server

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/herbiewalker/alcove/internal/backup"
	"github.com/herbiewalker/alcove/internal/netsec"
)

// waitJob polls the backup page until the background job finishes and returns the final page.
func waitJob(t *testing.T, c *client) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		body := c.do("GET", "/admin/backup", nil).Body.String()
		if !strings.Contains(body, "This page refreshes by itself") {
			return body
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the backup job did not finish")
	return ""
}

func TestBackupPageNeedsAdminAndStepUp(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := e.setupAdmin()
	if w := admin.post("/admin/backup", "/admin/backup/run", nil); w.Code != 303 || !strings.HasPrefix(w.Header().Get("Location"), "/stepup") {
		t.Fatalf("run without step-up: %d %s", w.Code, w.Header().Get("Location"))
	}
	if w := admin.post("/stepup", "/stepup", url.Values{"password": {adminPW}, "next": {"/admin/backup"}}); w.Code != 303 || w.Header().Get("Location") != "/admin/backup" {
		t.Fatalf("step-up should return to the backup page: %d %s", w.Code, w.Header().Get("Location"))
	}
	e.addMember("sam", "sam has a long password")
	member, _ := e.login("192.168.1.50:1", "sam", "sam has a long password")
	for _, p := range []string{"/admin/backup/run", "/admin/backup/verify", "/admin/backup/drill"} {
		if w := member.post("/account", p, url.Values{"name": {"x"}}); w.Code != 404 {
			t.Errorf("a member reached POST %s: %d", p, w.Code)
		}
	}
	if ents, _ := os.ReadDir(e.backupDir); len(ents) != 0 {
		t.Errorf("a refused request still wrote files: %v", ents)
	}
}

func TestBackupRunVerifyDrillEndToEnd(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := steppedUp(t, e)
	admin.post("/stepup", "/stepup", url.Values{"password": {adminPW}, "next": {"/admin/backup"}})

	if body := admin.do("GET", "/admin/backup", nil).Body.String(); !strings.Contains(body, "No backups yet") || !strings.Contains(body, "never") {
		t.Fatal("an empty backup page should say so")
	}
	// Back up.
	if w := admin.post("/admin/backup", "/admin/backup/run", nil); w.Code != 303 {
		t.Fatalf("run: %d %s", w.Code, w.Body.String())
	}
	body := waitJob(t, admin)
	if !strings.Contains(body, "written and verified") {
		t.Fatalf("backup result: %s", body)
	}
	list, err := backup.List(e.backupDir)
	if err != nil || len(list) != 1 {
		t.Fatalf("backups on disk: %v %v", list, err)
	}
	if _, err := backup.Verify(list[0].Path); err != nil {
		t.Fatalf("the written backup does not verify: %v", err)
	}
	if v, ok, _ := e.st.GetMeta("backup.last_ok"); !ok || v == "" {
		t.Error("the time of the last good backup was not recorded")
	}
	if !strings.Contains(body, list[0].Name) {
		t.Error("the page does not list the new backup")
	}

	// Verify and drill by name.
	if w := admin.post("/admin/backup", "/admin/backup/verify", url.Values{"name": {list[0].Name}}); w.Code != 303 {
		t.Fatalf("verify: %d", w.Code)
	}
	if body := waitJob(t, admin); !strings.Contains(body, "every checksum matches") {
		t.Fatalf("verify result: %s", body)
	}
	if w := admin.post("/admin/backup", "/admin/backup/drill", url.Values{"name": {list[0].Name}}); w.Code != 303 {
		t.Fatalf("drill: %d", w.Code)
	}
	body = waitJob(t, admin)
	if !strings.Contains(body, "Restore drill passed") || !strings.Contains(body, "1 account(s)") {
		t.Fatalf("drill result: %s", body)
	}
	if v, ok, _ := e.st.GetMeta("backup.last_drill"); !ok || !strings.HasSuffix(v, ":ok") {
		t.Errorf("drill outcome not recorded: %q", v)
	}
	// The drill leaves nothing behind in the backup folder.
	if ents, _ := os.ReadDir(e.backupDir); len(ents) != 3 { // tar + sidecar + srvtmp
		var names []string
		for _, x := range ents {
			names = append(names, x.Name())
		}
		t.Errorf("unexpected files in the backup folder: %v", names)
	}

	// Every action is in the admin log, with names and outcomes but no content.
	acts := e.logActions()
	for _, want := range []string{"backup.view", "backup.run", "backup.verify", "backup.drill"} {
		if !has(acts, want) {
			t.Errorf("no log entry for %s (have %v)", want, acts)
		}
	}
	if out := e.logs.String(); strings.Contains(out, adminPW) {
		t.Error("a password reached the log")
	}

	// A damaged backup is reported, not hidden.
	raw, _ := os.ReadFile(list[0].Path)
	raw[len(raw)/2] ^= 0xff
	if err := os.WriteFile(list[0].Path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	admin.post("/admin/backup", "/admin/backup/verify", url.Values{"name": {list[0].Name}})
	if body := waitJob(t, admin); !strings.Contains(body, "Verification FAILED") {
		t.Fatalf("a damaged backup was not reported: %s", body)
	}
}

func TestBackupNamesAreValidated(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := steppedUp(t, e)
	for _, bad := range []string{"../../etc/passwd", "/etc/passwd", "notes.txt", "", filepath.Join(e.backupDir, "x")} {
		w := admin.post("/admin/backup", "/admin/backup/drill", url.Values{"name": {bad}})
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "not found") {
			t.Errorf("%q: %d", bad, w.Code)
		}
	}
}

func TestOnlyOneBackupJobAtATime(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := steppedUp(t, e)
	admin.post("/stepup", "/stepup", url.Values{"password": {adminPW}, "next": {"/admin/backup"}})
	release := make(chan struct{})
	started := e.srv.jobs.start("backup", "", func() (string, error) { <-release; return "done", nil }, e.srv.now)
	if !started {
		t.Fatal("could not start the first job")
	}
	if w := admin.post("/admin/backup", "/admin/backup/run", nil); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "still running") {
		t.Fatalf("a second job was allowed: %d", w.Code)
	}
	if body := admin.do("GET", "/admin/backup", nil).Body.String(); !strings.Contains(body, `http-equiv="refresh"`) || !strings.Contains(body, "refreshes by itself") {
		t.Error("the page must refresh itself while a job runs")
	}
	close(release)
	waitJob(t, admin)
}

func TestBackupNotConfigured(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	e.backupDir = ""
	e.srv = e.build(netsec.Env{})
	admin := e.setupAdmin()
	if body := admin.do("GET", "/admin/backup", nil).Body.String(); !strings.Contains(body, "No backup folder") {
		t.Fatal("an unconfigured server must say so")
	}
	admin.post("/stepup", "/stepup", url.Values{"password": {adminPW}, "next": {"/admin/backup"}})
	if w := admin.post("/admin/backup", "/admin/backup/run", nil); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "No backup folder") {
		t.Fatalf("run with no folder: %d", w.Code)
	}
}
