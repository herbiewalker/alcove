package server

import (
	"context"
	"database/sql"
	"fmt"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/herbiewalker/alcove/internal/netsec"
	"github.com/herbiewalker/alcove/internal/spaces"
)

var (
	codeRE  = regexp.MustCompile(`<code>([a-z2-9]{4}-[a-z2-9]{4}-[a-z2-9]{4}-[a-z2-9]{4})</code>`)
	spaceRE = regexp.MustCompile(`href="/spaces/(sp[a-f0-9]+)"`)
)

// person makes a signed-in member through the real pages and returns their client.
func person(t *testing.T, e *env, admin *client, name string) *client {
	t.Helper()
	temp := createPerson(t, admin, name, "member")
	c, _ := e.login("192.168.1.150:1", name, temp)
	pw := name + " has a long password"
	if w := c.post("/account/password", "/account/password", url.Values{"current": {temp}, "password": {pw}, "confirm": {pw}}); w.Code != 303 {
		t.Fatalf("%s: set password: %d", name, w.Code)
	}
	return c
}

// startSpace has c create a space and returns its id.
func startSpace(t *testing.T, c *client, name string) string {
	t.Helper()
	w := c.post("/spaces", "/spaces", url.Values{"name": {name}})
	if w.Code != 303 || !strings.HasPrefix(w.Header().Get("Location"), "/spaces/sp") {
		t.Fatalf("create space: %d %s", w.Code, w.Body.String())
	}
	return strings.TrimPrefix(w.Header().Get("Location"), "/spaces/")
}

// codeFrom pulls the one-time code out of a "shown once" page.
func codeFrom(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	m := codeRE.FindStringSubmatch(w.Body.String())
	if w.Code != 200 || m == nil {
		t.Fatalf("no code on the page (%d): %.300s", w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Error("a page that shows a code must not be cached")
	}
	return m[1]
}

func (e *env) accounts() int { return e.users() }

func (e *env) member(spaceID, user string) (string, bool) {
	e.t.Helper()
	uid := e.idOf(user)
	var role string
	var ok bool
	_ = e.st.View(context.Background(), func(tx *sql.Tx) error { role, ok = spaces.Role(tx, spaceID, uid); return nil })
	return role, ok
}

func TestAPersonCreatesASpaceAndOthersCannotSeeIt(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := adminReady(t, e)
	sam, kim := person(t, e, admin, "sam"), person(t, e, admin, "kim")
	id := startSpace(t, sam, "Our private place")

	body := sam.do("GET", "/spaces/"+id, nil).Body.String()
	if !strings.Contains(body, "Our private place") || !strings.Contains(body, "the owner of this space") {
		t.Fatalf("the owner's page: %.300s", body)
	}
	if !strings.Contains(sam.do("GET", "/spaces", nil).Body.String(), "Our private place") {
		t.Error("the owner's list lacks the space")
	}
	// An outsider gets the very same answer as for a space that never existed, and never sees the name.
	nothing := kim.do("GET", "/spaces/spdoesnotexist0000000000", nil)
	got := kim.do("GET", "/spaces/"+id, nil)
	if got.Code != 404 || nothing.Code != 404 {
		t.Fatalf("outsider: %d, nonexistent: %d", got.Code, nothing.Code)
	}
	if strings.Contains(got.Body.String(), "Our private place") || strings.Contains(kim.do("GET", "/spaces", nil).Body.String(), "Our private place") {
		t.Error("an outsider can see the space's name")
	}
	for _, action := range []string{"rename", "settings", "leave", "invite", "code", "delete", "approve", "remove", "transfer", "revoke", "deny"} {
		w := kim.post("/spaces", "/spaces/"+id+"/"+action, url.Values{"name": {"Hijacked"}, "user": {e.idOf("sam")}, "do": {"rotate"}})
		if w.Code != 404 {
			t.Errorf("an outsider's %s: %d", action, w.Code)
		}
	}
	if w := kim.post("/spaces", "/spaces/"+id+"/restore", nil); w.Code != 404 {
		t.Errorf("an outsider's restore: %d", w.Code)
	}
	anon := e.client("192.168.1.151:1")
	if w := anon.do("GET", "/spaces/"+id, nil); w.Code != 303 || w.Header().Get("Location") != "/login" {
		t.Errorf("anonymous: %d", w.Code)
	}
	if strings.Contains(sam.do("GET", "/spaces/"+id, nil).Body.String(), "Hijacked") {
		t.Error("an outsider renamed the space")
	}
}

func TestInviteAnExistingPerson(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := adminReady(t, e)
	sam, kim := person(t, e, admin, "sam"), person(t, e, admin, "kim")
	id := startSpace(t, sam, "Place")
	code := codeFrom(t, sam.post("/spaces/"+id, "/spaces/"+id+"/invite", url.Values{"label": {"for Kim"}, "days": {"7"}}))

	// The code is not stored, and the owner sees the invite as open.
	if strings.Contains(sam.do("GET", "/spaces/"+id, nil).Body.String(), code) {
		t.Fatal("the invite code is shown again after the first time")
	}
	if body := sam.do("GET", "/spaces/"+id, nil).Body.String(); !strings.Contains(body, "for Kim") || !strings.Contains(body, "open") {
		t.Error("the owner cannot see the open invite")
	}
	// A wrong code and the right code, in different case and spacing.
	w := kim.post("/join", "/join/code", url.Values{"code": {"aaaa-aaaa-aaaa-aaaa"}})
	if w.Code != 400 || !strings.Contains(w.Body.String(), "did not work") {
		t.Fatalf("a wrong code: %d", w.Code)
	}
	w = kim.post("/join", "/join/code", url.Values{"code": {"  " + strings.ToUpper(code) + " "}})
	if w.Code != 200 || !strings.Contains(w.Body.String(), "What joining shares") || !strings.Contains(w.Body.String(), "Place") {
		t.Fatalf("the join screen: %d %.300s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"cannot be unseen", "pause sharing", "administrator can see everything", "asked to agree again"} {
		if !strings.Contains(body, want) {
			t.Errorf("the join screen does not say %q", want)
		}
	}
	// Without the tick nothing happens.
	if w := kim.post("/join", "/join/accept", url.Values{"code": {code}}); w.Code != 400 || !strings.Contains(w.Body.String(), "Tick the box") {
		t.Fatalf("no consent: %d", w.Code)
	}
	if _, ok := e.member(id, "kim"); ok {
		t.Fatal("joined without agreeing")
	}
	if w := kim.post("/join", "/join/accept", url.Values{"code": {code}, "consent": {"1"}}); w.Code != 303 || w.Header().Get("Location") != "/spaces/"+id {
		t.Fatalf("join: %d %s", w.Code, w.Header().Get("Location"))
	}
	if role, ok := e.member(id, "kim"); !ok || role != spaces.RoleMember {
		t.Fatalf("not a member after joining: %q %v", role, ok)
	}
	if body := kim.do("GET", "/spaces/"+id, nil).Body.String(); !strings.Contains(body, "Place") || !strings.Contains(body, "@sam") {
		t.Error("the new member cannot see the space")
	}
	// Single use.
	third := person(t, e, admin, "lee")
	if w := third.post("/join", "/join/code", url.Values{"code": {code}}); w.Code != 400 {
		t.Errorf("a spent invite was accepted again: %d", w.Code)
	}
	if body := sam.do("GET", "/spaces/"+id, nil).Body.String(); !strings.Contains(body, "used") {
		t.Error("the owner is not told the invite was used")
	}
}

func TestInviteANewPersonCreatesAnAccountOnce(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := adminReady(t, e)
	sam := person(t, e, admin, "sam")
	id := startSpace(t, sam, "Place")
	code := codeFrom(t, sam.post("/spaces/"+id, "/spaces/"+id+"/invite", url.Values{"days": {"1"}}))
	before := e.accounts()

	anon := e.client("192.168.1.160:1")
	w := anon.post("/join", "/join/code", url.Values{"code": {code}})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `name="username"`) || !strings.Contains(w.Body.String(), "consenting adults") {
		t.Fatalf("the sign-up screen: %d", w.Code)
	}
	form := func(mod func(url.Values)) url.Values {
		v := url.Values{"code": {code}, "username": {"newbie"}, "display": {"New Person"}, "password": {"a good long passphrase"}, "confirm": {"a good long passphrase"}, "consent": {"1"}}
		mod(v)
		return v
	}
	for label, v := range map[string]url.Values{
		"no consent":    form(func(v url.Values) { v.Del("consent") }),
		"weak password": form(func(v url.Values) { v.Set("password", "short"); v.Set("confirm", "short") }),
		"mismatch":      form(func(v url.Values) { v.Set("confirm", "something else entirely") }),
		"bad username":  form(func(v url.Values) { v.Set("username", "x y") }),
		"taken":         form(func(v url.Values) { v.Set("username", "SAM") }),
	} {
		if w := anon.post("/join", "/join/accept", v); w.Code < 400 {
			t.Errorf("%s: accepted (%d)", label, w.Code)
		}
		if e.accounts() != before {
			t.Fatalf("%s: an account was created", label)
		}
	}
	// None of those spent the invite.
	w = anon.post("/join", "/join/accept", form(func(url.Values) {}))
	if w.Code != 303 || w.Header().Get("Location") != "/spaces/"+id {
		t.Fatalf("a valid redemption: %d %s", w.Code, w.Body.String())
	}
	if e.accounts() != before+1 {
		t.Fatalf("accounts %d -> %d", before, e.accounts())
	}
	if u, err := e.userByName("newbie"); err != nil || u.Role != "member" || u.MustChange {
		t.Fatalf("the new account: %+v %v", u, err)
	}
	if role, ok := e.member(id, "newbie"); !ok || role != spaces.RoleMember {
		t.Fatal("the new person is not in the space")
	}
	if anon.cookies[sessionCookie] == "" {
		t.Fatal("the new person was not signed in")
	}
	if body := anon.do("GET", "/spaces/"+id, nil).Body.String(); !strings.Contains(body, "Place") {
		t.Error("the new person cannot open the space")
	}
	// The same code can never be used again, by the new person or anyone else.
	other := e.client("192.168.1.161:1")
	other.post("/join", "/join/code", url.Values{"code": {code}})
	if w := other.post("/join", "/join/accept", form(func(v url.Values) { v.Set("username", "second") })); w.Code != 400 {
		t.Errorf("a spent invite created a second account: %d", w.Code)
	}
	if e.accounts() != before+1 {
		t.Fatalf("a spent invite made another account: %d", e.accounts())
	}
}

// Constraint 11: the only unauthenticated way to an account is a live invite, and bad codes of every
// kind create nothing.
func TestNoBadCodeEverCreatesAnAccount(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := adminReady(t, e)
	sam := person(t, e, admin, "sam")
	id := startSpace(t, sam, "Place")
	before := e.accounts()

	expired := codeFrom(t, sam.post("/spaces/"+id, "/spaces/"+id+"/invite", url.Values{"days": {"1"}}))
	revoked := codeFrom(t, sam.post("/spaces/"+id, "/spaces/"+id+"/invite", url.Values{"days": {"7"}}))
	var revID string
	_ = e.st.View(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT id FROM invite WHERE code_hash = ?`, spaces.HashCode(revoked)).Scan(&revID)
	})
	if w := sam.post("/spaces/"+id, "/spaces/"+id+"/revoke", url.Values{"invite": {revID}}); w.Code != 303 {
		t.Fatalf("revoke: %d", w.Code)
	}
	spaceCode := codeFrom(t, sam.post("/spaces/"+id, "/spaces/"+id+"/code", url.Values{"do": {"rotate"}}))
	e.clock = e.clock.Add(2 * 24 * time.Hour) // the 1-day invite is now expired; sessions too

	bad := map[string]string{"expired": expired, "revoked": revoked, "a space code (needs an account)": spaceCode,
		"random": "abcd-efgh-jkmn-pqrs", "empty": "", "garbage": "<script>", "too long": strings.Repeat("a", 5000)}
	n := 0
	for label, code := range bad {
		n++
		anon := e.client(fmt.Sprintf("192.168.2.%d:1", n)) // a fresh address each time, so the rate limit cannot mask a guard
		v := url.Values{"code": {code}, "username": {"intruder"}, "password": {"intruder passphrase"}, "confirm": {"intruder passphrase"}, "consent": {"1"}}
		for _, path := range []string{"/join/code", "/join/accept"} {
			if w := anon.post("/join", path, v); w.Code < 400 {
				t.Errorf("%s via %s: %d", label, path, w.Code)
			}
		}
		if e.accounts() != before {
			t.Fatalf("%s created an account", label)
		}
	}
	if _, err := e.userByName("intruder"); err == nil {
		t.Fatal("an intruder account exists")
	}
}

func TestServerInviteFromTheAdmin(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := adminReady(t, e)
	before := e.accounts()
	if w := e.client("192.168.1.171:1").post("/join", "/admin/invites/create", url.Values{}); w.Code != 303 {
		t.Errorf("an anonymous server invite: %d", w.Code)
	}
	code := codeFrom(t, admin.post("/admin/users", "/admin/invites/create", url.Values{"label": {"for the neighbour"}, "days": {"7"}}))
	if !has(e.logActions(), "invite.create") {
		t.Error("creating a server invite is not in the admin log")
	}
	if strings.Contains(e.logs.String(), code) {
		t.Error("an invite code reached the log")
	}
	if body := admin.do("GET", "/admin/users", nil).Body.String(); !strings.Contains(body, "for the neighbour") {
		t.Error("the open server invite is not listed")
	}

	anon := e.client("192.168.1.172:1")
	w := anon.post("/join", "/join/code", url.Values{"code": {code}})
	if w.Code != 200 || strings.Contains(w.Body.String(), "What joining shares") || !strings.Contains(w.Body.String(), "Create your account") {
		t.Fatalf("a server invite's screen: %d", w.Code)
	}
	if w := anon.post("/join", "/join/accept", url.Values{"code": {code}, "username": {"guest"}, "password": {"guest passphrase here"}, "confirm": {"guest passphrase here"}}); w.Code != 303 || w.Header().Get("Location") != "/" {
		t.Fatalf("redeeming: %d %s", w.Code, w.Body.String())
	}
	if e.accounts() != before+1 {
		t.Fatal("no account was made")
	}
	if rows := e.count("SELECT COUNT(*) FROM membership WHERE user_id = ?", e.idOf("guest")); rows != 0 {
		t.Error("a server invite put someone in a space")
	}
	// Someone who already has an account cannot use one.
	code2 := codeFrom(t, admin.post("/admin/users", "/admin/invites/create", url.Values{}))
	if w := anon.post("/join", "/join/code", url.Values{"code": {code2}}); w.Code != 400 || !strings.Contains(w.Body.String(), "already have one") {
		t.Errorf("a signed-in person with a server invite: %d", w.Code)
	}
	// Revoked and expired invites do nothing.
	var id string
	_ = e.st.View(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT id FROM invite WHERE code_hash = ?`, spaces.HashCode(code2)).Scan(&id)
	})
	if w := admin.post("/admin/users", "/admin/invites/revoke", url.Values{"id": {id}}); w.Code != 303 {
		t.Fatalf("revoke: %d", w.Code)
	}
	c3 := e.client("192.168.1.173:1")
	if w := c3.post("/join", "/join/code", url.Values{"code": {code2}}); w.Code != 400 {
		t.Errorf("a revoked server invite: %d", w.Code)
	}
	if !has(e.logActions(), "invite.revoke") {
		t.Error("the revoke is not in the admin log")
	}
}

func TestAskingToJoinWithASpaceCode(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := adminReady(t, e)
	sam, kim, lee := person(t, e, admin, "sam"), person(t, e, admin, "kim"), person(t, e, admin, "lee")
	id := startSpace(t, sam, "Place")
	code := codeFrom(t, sam.post("/spaces/"+id, "/spaces/"+id+"/code", url.Values{"do": {"rotate"}}))

	// A signed-out person cannot use it, and no account is made.
	n := e.accounts()
	anon := e.client("192.168.1.180:1")
	if w := anon.post("/join", "/join/code", url.Values{"code": {code}}); w.Code != 400 || !strings.Contains(w.Body.String(), "Sign in first") {
		t.Fatalf("signed out with a space code: %d", w.Code)
	}
	if e.accounts() != n {
		t.Fatal("an account was made")
	}
	// A signed-in person asks; asking is not joining.
	w := kim.post("/join", "/join/code", url.Values{"code": {code}})
	if w.Code != 200 || !strings.Contains(w.Body.String(), "ask to join") || !strings.Contains(w.Body.String(), "Ask to join") {
		t.Fatalf("the ask screen: %d", w.Code)
	}
	if w := kim.post("/join", "/join/accept", url.Values{"code": {code}, "consent": {"1"}}); w.Code != 303 || w.Header().Get("Location") != "/spaces" {
		t.Fatalf("ask: %d %s", w.Code, w.Header().Get("Location"))
	}
	if _, ok := e.member(id, "kim"); ok {
		t.Fatal("asking let someone in")
	}
	if got := kim.do("GET", "/spaces/"+id, nil); got.Code != 404 {
		t.Errorf("a person who only asked can open the space: %d", got.Code)
	}
	if !strings.Contains(kim.do("GET", "/spaces", nil).Body.String(), "waiting for the owner") {
		t.Error("the person is not told their request is waiting")
	}
	// Only the owner sees and decides. A member who is not the owner cannot.
	page := sam.do("GET", "/spaces/"+id, nil).Body.String()
	if !strings.Contains(page, "Asking to join") || !strings.Contains(page, "@kim") {
		t.Fatalf("the owner does not see the request: %.400s", page)
	}
	if w := kim.post("/spaces", "/spaces/"+id+"/approve", url.Values{"user": {e.idOf("kim")}}); w.Code != 404 {
		t.Errorf("someone approving themselves: %d", w.Code)
	}
	if w := sam.post("/spaces/"+id, "/spaces/"+id+"/approve", url.Values{"user": {e.idOf("kim")}}); w.Code != 303 {
		t.Fatalf("approve: %d", w.Code)
	}
	if _, ok := e.member(id, "kim"); !ok {
		t.Fatal("an approved person is not in")
	}
	// A second asker is turned down and can ask again.
	lee.post("/join", "/join/code", url.Values{"code": {code}})
	lee.post("/join", "/join/accept", url.Values{"code": {code}, "consent": {"1"}})
	// A plain member cannot decide for the owner.
	if w := kim.post("/spaces/"+id, "/spaces/"+id+"/approve", url.Values{"user": {e.idOf("lee")}}); w.Code != 403 {
		t.Errorf("a member approving: %d", w.Code)
	}
	if w := sam.post("/spaces/"+id, "/spaces/"+id+"/deny", url.Values{"user": {e.idOf("lee")}}); w.Code != 303 {
		t.Fatalf("deny: %d", w.Code)
	}
	if _, ok := e.member(id, "lee"); ok {
		t.Fatal("a turned-down person is in")
	}
	// Rotating the code stops the old one; turning it off stops all.
	sam.post("/spaces/"+id, "/spaces/"+id+"/code", url.Values{"do": {"rotate"}})
	if w := lee.post("/join", "/join/code", url.Values{"code": {code}}); w.Code != 400 {
		t.Errorf("the old code after a rotate: %d", w.Code)
	}
	newCode := codeFrom(t, sam.post("/spaces/"+id, "/spaces/"+id+"/code", url.Values{"do": {"rotate"}}))
	sam.post("/spaces/"+id, "/spaces/"+id+"/code", url.Values{"do": {"disable"}})
	if w := lee.post("/join", "/join/code", url.Values{"code": {newCode}}); w.Code != 400 {
		t.Errorf("a disabled code: %d", w.Code)
	}
}

func TestLeavingRemovalHandOverAndDelete(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := adminReady(t, e)
	sam, kim, lee := person(t, e, admin, "sam"), person(t, e, admin, "kim"), person(t, e, admin, "lee")
	id := startSpace(t, sam, "Place")
	join := func(c *client) {
		code := codeFrom(t, sam.post("/spaces/"+id, "/spaces/"+id+"/invite", url.Values{}))
		c.post("/join", "/join/code", url.Values{"code": {code}})
		if w := c.post("/join", "/join/accept", url.Values{"code": {code}, "consent": {"1"}}); w.Code != 303 {
			t.Fatalf("join: %d", w.Code)
		}
	}
	join(kim)
	join(lee)

	// The owner cannot leave; a member can, and is out at once.
	if w := sam.post("/spaces/"+id, "/spaces/"+id+"/leave", nil); w.Code != 400 || !strings.Contains(w.Body.String(), "Hand the space") {
		t.Errorf("the owner leaving: %d", w.Code)
	}
	if w := kim.post("/spaces/"+id, "/spaces/"+id+"/leave", nil); w.Code != 303 || w.Header().Get("Location") != "/spaces" {
		t.Fatalf("leave: %d", w.Code)
	}
	if w := kim.do("GET", "/spaces/"+id, nil); w.Code != 404 {
		t.Errorf("a member who left still sees the space: %d", w.Code)
	}
	if body := kim.do("GET", "/spaces", nil).Body.String(); strings.Contains(body, "Place") && !strings.Contains(body, "not in any space") {
		t.Error("the list still shows a space the person left")
	}
	// A plain member cannot run the owner's actions.
	for _, a := range []string{"rename", "invite", "code", "remove", "transfer", "delete", "revoke"} {
		if w := lee.post("/spaces/"+id, "/spaces/"+id+"/"+a, url.Values{"name": {"x"}, "user": {e.idOf("sam")}}); w.Code != 403 {
			t.Errorf("a member's %s: %d", a, w.Code)
		}
	}
	if body := sam.do("GET", "/spaces/"+id, nil).Body.String(); strings.Contains(body, ">x<") {
		t.Error("a member renamed the space")
	}
	// Remove.
	if w := sam.post("/spaces/"+id, "/spaces/"+id+"/remove", url.Values{"user": {e.idOf("lee")}}); w.Code != 303 {
		t.Fatalf("remove: %d", w.Code)
	}
	if w := lee.do("GET", "/spaces/"+id, nil); w.Code != 404 {
		t.Errorf("a removed member sees the space: %d", w.Code)
	}
	if w := sam.post("/spaces/"+id, "/spaces/"+id+"/remove", url.Values{"user": {e.idOf("sam")}}); w.Code != 400 {
		t.Errorf("removing the owner: %d", w.Code)
	}
	// Hand-over and delete need the password again, and only the owner can.
	join(kim)
	if w := sam.post("/spaces/"+id, "/spaces/"+id+"/transfer", url.Values{"user": {e.idOf("kim")}}); w.Code != 303 || !strings.HasPrefix(w.Header().Get("Location"), "/stepup") {
		t.Fatalf("a hand-over without step-up: %d %s", w.Code, w.Header().Get("Location"))
	}
	if role, _ := e.member(id, "sam"); role != spaces.RoleOwner {
		t.Fatal("ownership moved without the password")
	}
	sam.post("/stepup", "/stepup", url.Values{"password": {"sam has a long password"}, "next": {"/spaces"}})
	if w := sam.post("/spaces/"+id, "/spaces/"+id+"/transfer", url.Values{"user": {e.idOf("kim")}}); w.Code != 303 {
		t.Fatalf("hand-over: %d", w.Code)
	}
	if role, _ := e.member(id, "kim"); role != spaces.RoleOwner {
		t.Fatal("kim is not the owner")
	}
	if w := sam.post("/spaces/"+id, "/spaces/"+id+"/leave", nil); w.Code != 303 {
		t.Errorf("the former owner leaving: %d", w.Code)
	}
	// Kim deletes (step-up first); the space vanishes for everyone, and kim can restore it.
	if w := kim.post("/spaces/"+id, "/spaces/"+id+"/delete", nil); w.Code != 303 || !strings.HasPrefix(w.Header().Get("Location"), "/stepup") {
		t.Fatalf("delete without step-up: %d", w.Code)
	}
	kim.post("/stepup", "/stepup", url.Values{"password": {"kim has a long password"}, "next": {"/spaces"}})
	if w := kim.post("/spaces/"+id, "/spaces/"+id+"/delete", nil); w.Code != 303 || w.Header().Get("Location") != "/spaces" {
		t.Fatalf("delete: %d", w.Code)
	}
	if w := kim.do("GET", "/spaces/"+id, nil); w.Code != 404 {
		t.Errorf("a deleted space still opens for its owner: %d", w.Code)
	}
	if body := kim.do("GET", "/spaces", nil).Body.String(); !strings.Contains(body, "Recently deleted spaces") {
		t.Error("the owner cannot see the deleted space to restore it")
	}
	if w := lee.post("/spaces", "/spaces/"+id+"/restore", nil); w.Code != 404 {
		t.Errorf("a stranger restoring: %d", w.Code)
	}
	if w := kim.post("/spaces", "/spaces/"+id+"/restore", nil); w.Code != 303 {
		t.Fatalf("restore: %d", w.Code)
	}
	if w := kim.do("GET", "/spaces/"+id, nil); w.Code != 200 {
		t.Errorf("a restored space does not open: %d", w.Code)
	}
	// Past the owner's window only the administrator can bring it back.
	kim.post("/stepup", "/stepup", url.Values{"password": {"kim has a long password"}})
	kim.post("/spaces/"+id, "/spaces/"+id+"/delete", nil)
	e.clock = e.clock.Add(11 * 24 * time.Hour)
	kim = e.reLogin("kim")
	if w := kim.post("/spaces", "/spaces/"+id+"/restore", nil); w.Code != 400 || !strings.Contains(w.Body.String(), "Ask the administrator") {
		t.Fatalf("restoring after the window: %d", w.Code)
	}
	admin = e.reAdmin()
	if body := admin.do("GET", "/admin/spaces", nil).Body.String(); !strings.Contains(body, "Place") || !strings.Contains(body, "deleted") {
		t.Fatalf("the admin's spaces page: %.400s", body)
	}
	if w := admin.post("/admin/spaces", "/admin/spaces/action", url.Values{"id": {id}, "do": {"restore"}}); w.Code != 303 {
		t.Fatalf("admin restore: %d", w.Code)
	}
	for _, want := range []string{"admin.spaces.view", "space.restore"} {
		if !has(e.logActions(), want) {
			t.Errorf("no admin-log entry for %s", want)
		}
	}
	if w := kim.do("GET", "/spaces/"+id, nil); w.Code != 200 {
		t.Errorf("the owner after an admin restore: %d", w.Code)
	}
}

func TestSettingsAreMineAlone(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := adminReady(t, e)
	sam, kim := person(t, e, admin, "sam"), person(t, e, admin, "kim")
	id := startSpace(t, sam, "Place")
	code := codeFrom(t, sam.post("/spaces/"+id, "/spaces/"+id+"/invite", url.Values{}))
	kim.post("/join", "/join/code", url.Values{"code": {code}})
	kim.post("/join", "/join/accept", url.Values{"code": {code}, "consent": {"1"}})

	if w := kim.post("/spaces/"+id, "/spaces/"+id+"/settings", url.Values{"partners": {"1"}, "paused": {"1"}}); w.Code != 303 {
		t.Fatalf("settings: %d", w.Code)
	}
	read := func(user string) (notes, photos, partners, paused bool) {
		uid := e.idOf(user)
		_ = e.st.View(context.Background(), func(tx *sql.Tx) error {
			var a, b, c, d int
			err := tx.QueryRow(`SELECT include_notes, include_photos, show_partner_names, paused FROM membership WHERE space_id = ? AND user_id = ?`, id, uid).Scan(&a, &b, &c, &d)
			notes, photos, partners, paused = a == 1, b == 1, c == 1, d == 1
			return err
		})
		return
	}
	if n, p, pn, pa := read("kim"); n || p || !pn || !pa {
		t.Errorf("kim's choices were not saved: %v %v %v %v", n, p, pn, pa)
	}
	if n, p, pn, pa := read("sam"); !n || !p || pn || pa {
		t.Errorf("another member's choices changed: %v %v %v %v", n, p, pn, pa)
	}
}

func TestJoinAttemptsAreRateLimited(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	e.setupAdmin()
	c := e.client("192.168.1.190:1")
	var last int
	for i := 0; i < 14; i++ {
		last = c.post("/join", "/join/code", url.Values{"code": {"wrong-code-" + string(rune('a'+i))}}).Code
	}
	if last != 429 {
		t.Fatalf("code guessing was not limited: %d", last)
	}
	if e.client("192.168.1.191:1").post("/join", "/join/code", url.Values{"code": {"x"}}).Code == 429 {
		t.Error("another address was limited")
	}
	if strings.Contains(e.logs.String(), "wrong-code") {
		t.Error("a typed code reached the log")
	}
}

func TestSpacePagesFitTheShellAndNeedNoScript(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := adminReady(t, e)
	sam := person(t, e, admin, "sam")
	id := startSpace(t, sam, "Place")
	for _, p := range []string{"/spaces", "/spaces/" + id, "/join"} {
		w := sam.do("GET", p, nil)
		body := w.Body.String()
		if w.Code != 200 || len(scriptRE.FindAllString(body, -1)) != 0 || strings.Contains(body, " style=") {
			t.Errorf("%s: %d scripts=%d", p, w.Code, len(scriptRE.FindAllString(body, -1)))
		}
	}
	if b := admin.do("GET", "/admin/spaces", nil); b.Code != 200 {
		t.Errorf("admin spaces: %d", b.Code)
	}
	// Members cannot open the admin's page or its action.
	if w := sam.do("GET", "/admin/spaces", nil); w.Code != 404 {
		t.Errorf("a member opened the admin's spaces: %d", w.Code)
	}
	if w := sam.post("/spaces", "/admin/spaces/action", url.Values{"id": {id}, "do": {"delete"}}); w.Code != 404 {
		t.Errorf("a member ran the admin's space action: %d", w.Code)
	}
}
