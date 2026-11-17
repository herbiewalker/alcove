package server

import (
	"encoding/csv"
	"net/url"
	"strings"
	"testing"

	"github.com/herbiewalker/alcove/internal/netsec"
)

func TestAdminLogCanBeFilteredAndKeepsTheFilterWhilePaging(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := e.setupAdmin()
	admin.do("GET", "/admin/settings", nil) // settings.view lines
	admin.do("GET", "/admin/users", nil)

	all := admin.do("GET", "/admin/log", nil).Body.String()
	if !strings.Contains(all, "settings.view") || !strings.Contains(all, "Find entries") {
		t.Fatalf("the log page: %.400s", all)
	}
	got := admin.do("GET", "/admin/log?action=settings.", nil).Body.String()
	if !strings.Contains(got, "settings.view") || strings.Contains(got, "users.view") {
		t.Errorf("the action filter did not narrow the log")
	}
	if !strings.Contains(got, `value="settings."`) {
		t.Error("the filter is not shown back as typed")
	}
	if body := admin.do("GET", "/admin/log?who=owner&action=nothing.like.this", nil).Body.String(); !strings.Contains(body, "Nothing matches this filter.") {
		t.Error("an empty result is not explained")
	}
	if body := admin.do("GET", "/admin/log?who=nobody-by-this-name", nil).Body.String(); strings.Contains(body, "settings.view") {
		t.Error("an unknown user name showed lines")
	}
	if body := admin.do("GET", "/admin/log?action=a%27%3B--&from=yesterday", nil).Body.String(); !strings.Contains(body, "was ignored") || !strings.Contains(body, "settings.view") {
		t.Error("bad filter words are not explained, or they blanked the page")
	}
	// Fifty lines to a page: enough lines make a second page that still carries the filter.
	for i := 0; i < 60; i++ {
		admin.do("GET", "/admin/settings", nil)
	}
	if body := admin.do("GET", "/admin/log?action=settings.", nil).Body.String(); !strings.Contains(body, "page=2&amp;action=settings.") {
		t.Errorf("the pager drops the filter: %s", pagerOf(body))
	}
	if !has(e.logActions(), "adminlog.view") {
		t.Error("viewing the log is not logged")
	}
}

func pagerOf(body string) string {
	if i := strings.Index(body, `class="pager"`); i >= 0 {
		return body[i:min(len(body), i+300)]
	}
	return "(no pager)"
}

func TestAdminLogExportIsStepUpGatedLoggedAndSafe(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := e.setupAdmin()
	admin.do("GET", "/admin/settings", nil)
	form := url.Values{"action": {"settings."}}

	if w := admin.post("/admin/log", "/admin/log/export", form); w.Code != 303 || !strings.HasPrefix(w.Header().Get("Location"), "/stepup?next=/admin/log") {
		t.Fatalf("an export without a step-up: %d %s", w.Code, w.Header().Get("Location"))
	}
	if w := admin.post("/stepup", "/stepup", url.Values{"password": {adminPW}, "next": {"/admin/log"}}); w.Code != 303 || w.Header().Get("Location") != "/admin/log" {
		t.Fatalf("step-up should return to the log: %d %s", w.Code, w.Header().Get("Location"))
	}
	w := admin.post("/admin/log", "/admin/log/export", form)
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/csv") || !strings.Contains(w.Header().Get("Content-Disposition"), "alcove-admin-log-") {
		t.Fatalf("export: %d %v", w.Code, w.Header())
	}
	rows, err := csv.NewReader(strings.NewReader(w.Body.String())).ReadAll()
	if err != nil || len(rows) < 2 || strings.Join(rows[0], ",") != "seq,time_utc,who,action,target_type,target_id,fields" {
		t.Fatalf("the CSV is not well formed: %v %v", err, rows)
	}
	for _, r := range rows[1:] {
		if !strings.HasPrefix(r[3], "settings.") {
			t.Errorf("the export ignored the filter: %v", r)
		}
		if r[2] != "owner" {
			t.Errorf("who should be the user name: %v", r)
		}
	}
	if !has(e.logActions(), "adminlog.export") {
		t.Error("the export is not in the log")
	}
	// A member cannot export, and a signed-out visitor cannot either.
	e.addMember("kim", "Kim-Password-1234")
	kim, _ := e.login("192.168.1.21:1", "kim", "Kim-Password-1234")
	if w := kim.post("/account", "/admin/log/export", form); w.Code != 404 && w.Code != 403 {
		t.Errorf("a member exported the admin log: %d", w.Code)
	}
	if w := e.client("192.168.1.99:1").do("POST", "/admin/log/export", form); w.Code == 200 {
		t.Error("a signed-out visitor exported the admin log")
	}
}

func TestCSVCellsCannotRunAsFormulas(t *testing.T) {
	for in, want := range map[string]string{"=1+1": "'=1+1", "-5": "'-5", "@sum": "'@sum", "+x": "'+x", "data.edit": "data.edit", "": ""} {
		if got := csvCell(in); got != want {
			t.Errorf("csvCell(%q) = %q, want %q", in, got, want)
		}
	}
}
