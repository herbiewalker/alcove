package server

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/herbiewalker/alcove/internal/netsec"
)

func warnCount(body string) int { return strings.Count(body, "Needs attention:") }

func TestHealthPageSaysWhatNeedsAttentionAndNothingPrivate(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := e.setupAdmin()
	e.addMember("samuel-private-name", "sam has a long password")

	w := admin.do("GET", "/admin/health", nil)
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, "Storage and protection") {
		t.Fatalf("health page: %d %.300s", w.Code, body)
	}
	// A new server has never made a backup, run a drill or checked the log chain.
	for _, want := range []string{"Last instance backup", "Last restore drill", "Admin log chain"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	if n := warnCount(body); n != 3 || !strings.Contains(body, "3 things need your attention") {
		t.Errorf("a new server should show 3 warnings, got %d", n)
	}
	if strings.Contains(body, "samuel-private-name") {
		t.Error("the health page names a person")
	}
	if !strings.Contains(admin.do("GET", "/admin", nil).Body.String(), "3 things need your attention") {
		t.Error("the admin console does not summarise the health")
	}
	if !has(e.logActions(), "admin.health.view") {
		t.Error("opening the health page is not logged")
	}

	// Do the three things: the warnings go away.
	now := e.clock.Unix()
	e.exec(`INSERT INTO meta(key, value) VALUES ('backup.last_ok', ?), ('backup.last_drill', ?)`, fmt.Sprint(now), fmt.Sprintf("%d:synthetic", now))
	admin.post("/admin/log", "/admin/log/verify", nil)
	body = admin.do("GET", "/admin/health", nil).Body.String()
	if n := warnCount(body); n != 0 || !strings.Contains(body, "Nothing needs your attention") {
		t.Errorf("after backup, drill and verify there are still %d warnings: %.600s", n, body)
	}

	// Time passes: a month later the backup and the check are stale again.
	e.clock = e.clock.Add(31 * 24 * time.Hour)
	admin, _ = e.login("192.168.1.10:5000", "owner", adminPW)
	body = admin.do("GET", "/admin/health", nil).Body.String()
	if n := warnCount(body); n != 2 || !strings.Contains(body, "Older than 30 days") {
		t.Errorf("a month later expected 2 stale warnings, got %d", n)
	}
}

func TestHealthShowsAFailedLogCheckAndIsAdminOnly(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := e.setupAdmin()
	e.exec(`INSERT INTO meta(key, value) VALUES ('backup.last_ok', ?), ('backup.last_drill', ?)`, fmt.Sprint(e.clock.Unix()), fmt.Sprintf("%d:synthetic", e.clock.Unix()))
	// A check that failed: the line the verify action writes.
	admin.post("/admin/log", "/admin/log/verify", nil)
	e.exec(`DROP TRIGGER admin_log_no_update`)
	e.exec(`UPDATE admin_log SET fields = 'failed' WHERE action = 'adminlog.verify'`)
	if body := admin.do("GET", "/admin/health", nil).Body.String(); !strings.Contains(body, "FAILED") || warnCount(body) != 1 {
		t.Errorf("a failed chain check is not flagged: %d", warnCount(body))
	}
	e.addMember("kim", "Kim-Password-1234")
	kim, _ := e.login("192.168.1.21:1", "kim", "Kim-Password-1234")
	if w := kim.do("GET", "/admin/health", nil); w.Code == 200 {
		t.Errorf("a member opened the health page: %d", w.Code)
	}
	if w := e.client("192.168.1.99:1").do("GET", "/admin/health", nil); w.Code == 200 {
		t.Error("a signed-out visitor opened the health page")
	}
}
