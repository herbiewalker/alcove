package server

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/herbiewalker/alcove/internal/netsec"
	"github.com/herbiewalker/alcove/internal/trystbk/synth"
)

func TestDownloadSetsTheMarkerOnlyForAValidToken(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := adminReady(t, e)
	sam, _ := memberWithUpload(t, e, admin, "sam", synth.Options{Seed: 71, Encounters: 3})
	sam.post("/stepup", "/stepup", url.Values{"password": {"sam has a long password"}, "next": {"/uploads"}})

	// A good token comes back as a cookie the script can read, with nothing secret in it.
	w := sam.post("/uploads", "/uploads/download", url.Values{"dltoken": {"abc123def456"}})
	if w.Code != 200 {
		t.Fatalf("download: %d", w.Code)
	}
	var got string
	for _, c := range w.Result().Cookies() {
		if c.Name == "alcove_dl" {
			got = c.Value
			if c.Path != "/" || c.MaxAge <= 0 || c.MaxAge > 300 || c.HttpOnly {
				t.Errorf("marker cookie attributes: %+v", c)
			}
			if c.SameSite != 3 { // http.SameSiteStrictMode
				t.Errorf("marker cookie is not SameSite=Strict: %v", c.SameSite)
			}
		}
	}
	if got != "abc123def456" {
		t.Errorf("marker = %q", got)
	}
	// Anything that is not a short lowercase token sets nothing, so a form cannot plant arbitrary cookie text.
	for _, bad := range []string{"", "ABC", "a b", "x;y=1", "short", strings.Repeat("a", 60), "<script>", "ab\r\nSet-Cookie: x=1"} {
		w := sam.post("/uploads", "/uploads/download", url.Values{"dltoken": {bad}})
		for _, c := range w.Result().Cookies() {
			if c.Name == "alcove_dl" {
				t.Errorf("token %q set a marker cookie", bad)
			}
		}
	}
}

func TestSlowFormsSayWhatIsHappening(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := e.setupAdmin()
	login := e.client("192.168.1.88:1").do("GET", "/login", nil).Body.String()
	if !strings.Contains(login, `data-busy="Signing in...`) {
		t.Error("the sign-in form has no busy message")
	}
	for page, want := range map[string][]string{
		"/uploads":      {`action="/uploads/download" data-download data-busy="Preparing your download`},
		"/admin/backup": {`data-busy="Starting the backup`, `data-busy="Starting the rebuild`},
		"/admin/users":  {`data-busy="Creating the account`},
	} {
		body := admin.do("GET", page, nil).Body.String()
		for _, w := range want {
			if !strings.Contains(body, w) {
				t.Errorf("%s lacks %q", page, w)
			}
		}
	}
	// Only the download form is marked as a download.
	if n := strings.Count(admin.do("GET", "/admin/backup", nil).Body.String(), "data-download"); n != 0 {
		t.Errorf("the backup page marks %d forms as downloads", n)
	}
}

func TestARunningJobShowsABarAndAClock(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := steppedUp(t, e)
	release := make(chan struct{})
	started := e.srv.jobs.start("run", "", func() (string, error) { <-release; return "done", nil }, func() time.Time { return e.srv.now() })
	if !started {
		t.Fatal("the job did not start")
	}
	defer close(release)
	body := admin.do("GET", "/admin/backup", nil).Body.String()
	for _, want := range []string{`<progress aria-label="Working">`, `data-since="`, "Working on a backup", `http-equiv="refresh"`} {
		if !strings.Contains(body, want) {
			t.Errorf("a running job's page lacks %q", want)
		}
	}
	if strings.Contains(body, "Working on a run") {
		t.Error("the job kind leaked into the wording")
	}
	// The action buttons are disabled while it runs, so a second click cannot start another.
	if strings.Count(body, "disabled") < 2 {
		t.Error("the buttons are not disabled while a job runs")
	}
}
