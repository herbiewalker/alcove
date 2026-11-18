package server

import (
	"net/url"
	"strings"
	"testing"

	"github.com/herbiewalker/alcove/internal/netsec"
)

// F31: the admin sees the server address as text and as a local QR code; an invite page offers a QR
// for the join page and never puts the secret code into the picture.
func TestQRCodesAreLocalAndNeverHoldTheInviteCode(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := adminReady(t, e)

	home := admin.do("GET", "/admin", nil).Body.String()
	if !strings.Contains(home, "Open Alcove on a phone") || !strings.Contains(home, `<svg class="qr"`) {
		t.Fatalf("the admin page has no address QR: %.300s", home)
	}

	w := admin.post("/admin/users", "/admin/invites/create", url.Values{"label": {"qr test"}, "days": {"7"}})
	body := w.Body.String()
	code := codeFrom(t, w)
	if !strings.Contains(body, `<svg class="qr"`) {
		t.Fatal("the invite page has no QR")
	}
	i := strings.Index(body, "<svg")
	svg := body[i : i+strings.Index(body[i:], "</svg>")+6]
	if strings.Contains(svg, code) {
		t.Error("the QR picture carries text")
	}
	// Nothing in the page fetches from outside, and the picture needs no script or inline style.
	for _, bad := range []string{"<script", " style=", "src=\"http", "href=\"http"} {
		if strings.Contains(svg, bad) {
			t.Errorf("the QR picture contains %q", bad)
		}
	}
}

// F22: the hide-screen control is in every signed-in page, hidden until the script turns it on, and
// the script only covers the page (no storage, no requests).
func TestHideScreenButtonIsHiddenWithoutTheScriptAndTheScriptStoresNothing(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := e.setupAdmin()
	if b := admin.do("GET", "/", nil).Body.String(); !strings.Contains(b, `id="hide-screen" hidden`) {
		t.Error("the page has no hide-screen button, or shows it without the script")
	}
	js := admin.do("GET", "/static/ui.js", nil).Body.String()
	i := strings.Index(js, "// 5. Hide screen")
	if i < 0 {
		t.Fatal("the hide-screen code is missing")
	}
	for _, bad := range []string{"localStorage", "sessionStorage", "XMLHttpRequest", "fetch(", "document.cookie"} {
		if strings.Contains(js[i:], bad) {
			t.Errorf("the hide-screen code uses %s", bad)
		}
	}
}
