package server

import (
	"context"
	"database/sql"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/herbiewalker/alcove/internal/netsec"
	"github.com/herbiewalker/alcove/internal/spaces"
	"github.com/herbiewalker/alcove/internal/trystbk/synth"
)

var entryIDRE = regexp.MustCompile(`name="id" value="([^"]+)"`)

// sharing sets up sam (7 entries), kim (5 entries) and lou (4 entries, outside), with sam and kim in one space.
type sharing struct {
	e              *env
	sam, kim, lou  *client
	space, otherSp string
}

func (e *env) addToSpace(spaceID, user string) {
	e.t.Helper()
	uid := e.idOf(user)
	err := e.st.Update(context.Background(), false, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO membership(space_id, user_id, role, state, created_at, joined_at, consent_version, consent_at)
			VALUES (?,?,?,?,?,?,?,?)`, spaceID, uid, "member", "active", 1, 1, spaces.ConsentVersion, 1)
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

func newSharing(t *testing.T) *sharing {
	t.Helper()
	e := newEnv(t, netsec.Env{})
	admin := adminReady(t, e)
	s := &sharing{e: e, sam: person(t, e, admin, "sam"), kim: person(t, e, admin, "kim"), lou: person(t, e, admin, "lou")}
	for i, p := range []struct {
		c *client
		n int
	}{{s.sam, 7}, {s.kim, 5}, {s.lou, 4}} {
		res := synthFile(t, synth.Options{Seed: uint64(60 + i), Encounters: p.n, Password: "Backup-Password-" + string(rune('A'+i))})
		if w := p.c.upload(res.File, "b.tryst", res.Password, true, false); w.Code != 303 {
			t.Fatalf("upload %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	s.space = startSpace(t, s.sam, "Ours")
	e.addToSpace(s.space, "kim")
	s.otherSp = startSpace(t, s.lou, "Elsewhere")
	return s
}

func (s *sharing) list(c *client, space string) (int, string) {
	w := c.do("GET", "/spaces/"+space+"/entries", nil)
	return w.Code, w.Body.String()
}

func (s *sharing) rows(c *client, space string) int {
	_, body := s.list(c, space)
	return strings.Count(body, "<tr><td>")
}

func TestMembersSeeEachOthersEntriesAndOutsidersNothing(t *testing.T) {
	s := newSharing(t)
	for _, c := range []*client{s.sam, s.kim} {
		code, body := s.list(c, s.space)
		if code != 200 || !strings.Contains(body, "12 in all") || strings.Count(body, "<tr><td>") != 12 {
			t.Fatalf("a member sees %d: %.300s", code, body)
		}
		if !strings.Contains(body, "Person sam") || !strings.Contains(body, "Person kim") || !strings.Contains(body, "(you)") {
			t.Errorf("the list should name both members and mark your own: %.600s", body[strings.Index(body, "<tbody>"):])
		}
	}
	// Lou is in another space: this one does not exist for him, and his own list shows only his.
	if code, body := s.list(s.lou, s.space); code != 404 || strings.Contains(body, "Synthetic") {
		t.Fatalf("an outsider got %d", code)
	}
	if n := s.rows(s.lou, s.otherSp); n != 4 {
		t.Errorf("lou's own space shows %d, want 4", n)
	}
	// A space that does not exist looks identical.
	w1 := s.lou.do("GET", "/spaces/"+s.space+"/entries", nil)
	w2 := s.lou.do("GET", "/spaces/sp00000000000000000000000000000000/entries", nil)
	if w1.Code != w2.Code || w1.Body.String() != w2.Body.String() {
		t.Error("an outsider can tell a real space from a missing one")
	}
	// Nothing is signed-out visible.
	if w := s.e.client("192.168.1.99:1").do("GET", "/spaces/"+s.space+"/entries", nil); w.Code == 200 {
		t.Error("a signed-out visitor got the page")
	}
}

func TestPrivateEntryAndWhoCanFlagWhat(t *testing.T) {
	s := newSharing(t)
	page := s.sam.do("GET", "/entries", nil).Body.String()
	ids := entryIDRE.FindAllStringSubmatch(page, -1)
	if len(ids) != 7 || !strings.Contains(page, "7 from your latest upload") {
		t.Fatalf("sam's own list: %d ids", len(ids))
	}
	one := ids[2][1]
	if w := s.sam.post("/entries", "/entries/private", url.Values{"id": {one}, "private": {"1"}}); w.Code != 303 {
		t.Fatalf("mark private: %d", w.Code)
	}
	if n := s.rows(s.kim, s.space); n != 11 {
		t.Errorf("kim sees %d after one private entry, want 11", n)
	}
	if !strings.Contains(s.sam.do("GET", "/entries", nil).Body.String(), "Private: share it") {
		t.Error("the owner's list does not show the flag")
	}
	// Kim cannot flag Sam's entry, and a made-up id is refused; nothing changes.
	kimPage := s.kim.do("GET", "/entries", nil).Body.String()
	kimOne := entryIDRE.FindStringSubmatch(kimPage)[1]
	for _, id := range []string{ids[0][1], "nope"} {
		if w := s.kim.post("/entries", "/entries/private", url.Values{"id": {id}, "private": {"1"}}); w.Code != 404 {
			t.Errorf("flagging %q: %d, want 404", id, w.Code)
		}
	}
	if n := s.e.count(`SELECT COUNT(*) FROM entry_flag`); n != 1 {
		t.Errorf("%d flags, want 1", n)
	}
	if n := s.rows(s.sam, s.space); n != 11 {
		t.Errorf("sam sees %d, want 11 (his own private one is hidden from the space too)", n)
	}
	// Share it again.
	s.sam.post("/entries", "/entries/private", url.Values{"id": {one}, "private": {"0"}})
	if n := s.rows(s.kim, s.space); n != 12 {
		t.Errorf("after sharing again kim sees %d, want 12", n)
	}
	_ = kimOne
	// Signed out and without a token the flag cannot be set.
	if w := s.e.client("192.168.1.99:2").do("POST", "/entries/private", url.Values{"id": {one}, "private": {"1"}}); w.Code == 303 && s.e.count(`SELECT COUNT(*) FROM entry_flag WHERE private = 1`) > 0 {
		t.Error("an unauthenticated request set a flag")
	}
}

func TestPauseLeaveRemoveAndNotesTakeEffectAtOnce(t *testing.T) {
	s := newSharing(t)
	// Notes off: sam's rows stay, his note text goes (the synthetic data leaves some notes empty).
	_, before := s.list(s.kim, s.space)
	s.sam.post("/spaces/"+s.space, "/spaces/"+s.space+"/settings", url.Values{"photos": {"1"}})
	_, after := s.list(s.kim, s.space)
	if s.rows(s.kim, s.space) != 12 || strings.Count(after, "Synthetic entry") >= strings.Count(before, "Synthetic entry") {
		t.Errorf("with sam's notes off kim should see all 12 rows and fewer notes: %d then %d", strings.Count(before, "Synthetic entry"), strings.Count(after, "Synthetic entry"))
	}
	// Pause: sam's entries vanish from kim's view, and sam sees none of his own in the space.
	s.sam.post("/spaces/"+s.space, "/spaces/"+s.space+"/settings", url.Values{"notes": {"1"}, "photos": {"1"}, "paused": {"1"}})
	if n := s.rows(s.kim, s.space); n != 5 {
		t.Errorf("kim sees %d with sam paused, want 5", n)
	}
	if n := s.rows(s.sam, s.space); n != 5 {
		t.Errorf("sam sees %d while paused, want kim's 5 only", n)
	}
	s.sam.post("/spaces/"+s.space, "/spaces/"+s.space+"/settings", url.Values{"notes": {"1"}, "photos": {"1"}})
	if n := s.rows(s.kim, s.space); n != 12 {
		t.Errorf("after unpausing kim sees %d", n)
	}
	// Leaving: kim sees nothing and gets the not-found page; sam no longer sees kim.
	if w := s.kim.post("/spaces/"+s.space, "/spaces/"+s.space+"/leave", url.Values{}); w.Code != 303 {
		t.Fatalf("leave: %d", w.Code)
	}
	if code, _ := s.list(s.kim, s.space); code != 404 {
		t.Errorf("a member who left got %d", code)
	}
	if n := s.rows(s.sam, s.space); n != 7 {
		t.Errorf("sam sees %d after kim left, want his own 7", n)
	}
	// Back in, then removed by the owner.
	s.e.exec(`UPDATE membership SET state = 'active' WHERE space_id = ? AND user_id = ?`, s.space, s.e.idOf("kim"))
	if w := s.sam.post("/spaces/"+s.space, "/spaces/"+s.space+"/remove", url.Values{"user": {s.e.idOf("kim")}}); w.Code != 303 {
		t.Fatalf("remove: %d", w.Code)
	}
	if code, _ := s.list(s.kim, s.space); code != 404 {
		t.Errorf("a removed member got %d", code)
	}
	if n := s.rows(s.sam, s.space); n != 7 {
		t.Errorf("sam sees %d after removing kim", n)
	}
}

func TestOlderWordingHidesBothWaysUntilAgreed(t *testing.T) {
	s := newSharing(t)
	s.e.exec(`UPDATE membership SET consent_version = 1 WHERE space_id = ? AND user_id = ?`, s.space, s.e.idOf("kim"))
	// Sam no longer sees kim's entries.
	if n := s.rows(s.sam, s.space); n != 7 {
		t.Errorf("sam sees %d, want 7 (kim has not agreed to the new wording)", n)
	}
	// Kim sees nobody's, is sent to read again, and the space page asks.
	w := s.kim.do("GET", "/spaces/"+s.space+"/entries", nil)
	if w.Code != 303 || w.Header().Get("Location") != "/spaces/"+s.space {
		t.Fatalf("kim got %d %s", w.Code, w.Header().Get("Location"))
	}
	page := s.kim.do("GET", "/spaces/"+s.space, nil).Body.String()
	if !strings.Contains(page, "Please read this again") || strings.Contains(page, "See the shared entries") {
		t.Error("the space page should ask to agree and not offer the entries")
	}
	// Agreeing needs the tick.
	if w := s.kim.post("/spaces/"+s.space, "/spaces/"+s.space+"/agree", url.Values{}); w.Code != 400 {
		t.Errorf("agree without the tick: %d", w.Code)
	}
	if n := s.rows(s.sam, s.space); n != 7 {
		t.Error("an unticked agree changed what is shown")
	}
	if w := s.kim.post("/spaces/"+s.space, "/spaces/"+s.space+"/agree", url.Values{"agree": {"1"}}); w.Code != 303 {
		t.Fatalf("agree: %d", w.Code)
	}
	if n := s.rows(s.kim, s.space); n != 12 {
		t.Errorf("after agreeing kim sees %d, want 12", n)
	}
	// An outsider cannot agree on someone's behalf into a space.
	if w := s.lou.post("/spaces", "/spaces/"+s.space+"/agree", url.Values{"agree": {"1"}}); w.Code != 404 {
		t.Errorf("an outsider's agree: %d", w.Code)
	}
}

func TestDeletedUploadLeavesTheSpace(t *testing.T) {
	s := newSharing(t)
	rev := s.e.revisionOf("kim")
	if w := s.kim.post("/uploads", "/uploads/delete", url.Values{"id": {rev}}); w.Code != 303 || !strings.HasPrefix(w.Header().Get("Location"), "/stepup") {
		t.Fatalf("delete without step-up: %d", w.Code)
	}
	s.kim.post("/stepup", "/stepup", url.Values{"password": {"kim has a long password"}, "next": {"/uploads"}})
	if w := s.kim.post("/uploads", "/uploads/delete", url.Values{"id": {rev}}); w.Code != 303 {
		t.Fatalf("delete: %d", w.Code)
	}
	if n := s.rows(s.sam, s.space); n != 7 {
		t.Errorf("after kim deleted her only upload sam still sees %d rows, want his own 7", n)
	}
}

func (e *env) exec(q string, args ...any) {
	e.t.Helper()
	if err := e.st.Update(context.Background(), false, func(tx *sql.Tx) error { _, err := tx.Exec(q, args...); return err }); err != nil {
		e.t.Fatal(err)
	}
}
