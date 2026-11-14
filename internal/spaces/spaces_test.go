package spaces

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/herbiewalker/alcove/internal/keyfile"
	"github.com/herbiewalker/alcove/internal/store"
)

var t0 = time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)

type rig struct {
	t  *testing.T
	st *store.Store
}

func newRig(t *testing.T, users ...string) *rig {
	t.Helper()
	k, _, err := keyfile.Generate()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	st, err := store.Open(store.Config{DataDir: filepath.Join(root, "d"), TmpDir: filepath.Join(root, "t"), Key: k, AllowNonTmpfsTmp: true, SealEvery: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	r := &rig{t: t, st: st}
	for _, u := range users {
		r.exec(`INSERT INTO user(id, username, display_name, role, status, pw_hash, created_at) VALUES (?,?,?,?,?,?,?)`, "u"+u, u, strings.ToUpper(u[:1])+u[1:], "member", "active", "h", t0.Unix())
	}
	return r
}

func (r *rig) exec(q string, args ...any) {
	r.t.Helper()
	if err := r.st.Update(context.Background(), false, func(tx *sql.Tx) error { _, err := tx.Exec(q, args...); return err }); err != nil {
		r.t.Fatalf("%s: %v", q, err)
	}
}

func (r *rig) do(fn func(tx *sql.Tx) error) error {
	r.t.Helper()
	return r.st.Update(context.Background(), false, fn)
}

func (r *rig) must(fn func(tx *sql.Tx) error) {
	r.t.Helper()
	if err := r.do(fn); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) role(space, user string) (string, bool) {
	r.t.Helper()
	var role string
	var ok bool
	_ = r.st.View(context.Background(), func(tx *sql.Tx) error { role, ok = Role(tx, space, user); return nil })
	return role, ok
}

func (r *rig) newSpace(owner, name string) Space {
	r.t.Helper()
	var s Space
	r.must(func(tx *sql.Tx) (err error) { s, err = Create(tx, t0, name, owner); return })
	return s
}

func (r *rig) addMember(space, user string) {
	r.t.Helper()
	r.must(func(tx *sql.Tx) error {
		code, _, err := NewCode()
		_ = code
		if err != nil {
			return err
		}
		return JoinViaInvite(tx, t0, space, user, true)
	})
}

// N1 at the level where it is decided: the gate answers yes only to an active member of THIS space.
func TestTheGateIsolatesSpacesAndOutsiders(t *testing.T) {
	r := newRig(t, "alice", "bob", "carol", "dave")
	a := r.newSpace("ualice", "Alice and Bob")
	b := r.newSpace("ucarol", "Carol's space")
	r.addMember(a.ID, "ubob")

	want := map[[2]string]string{
		{a.ID, "ualice"}: RoleOwner, {a.ID, "ubob"}: RoleMember, {b.ID, "ucarol"}: RoleOwner,
	}
	for _, sp := range []string{a.ID, b.ID, "spnonexistent"} {
		for _, u := range []string{"ualice", "ubob", "ucarol", "udave", "unobody"} {
			role, ok := r.role(sp, u)
			if w, yes := want[[2]string{sp, u}]; yes {
				if !ok || role != w {
					t.Errorf("%s in %s: %q %v, want %q", u, sp, role, ok, w)
				}
			} else if ok {
				t.Errorf("%s must NOT pass the gate for %s", u, sp)
			}
		}
	}
	// Each person's list holds only their own spaces.
	for u, wantN := range map[string]int{"ualice": 1, "ubob": 1, "ucarol": 1, "udave": 0} {
		var list []Mine
		_ = r.st.View(context.Background(), func(tx *sql.Tx) (err error) { list, err = ForUser(tx, u); return })
		if len(list) != wantN {
			t.Errorf("%s sees %d spaces, want %d", u, len(list), wantN)
		}
		for _, m := range list {
			if _, ok := r.role(m.ID, u); !ok {
				t.Errorf("%s was listed a space they cannot enter", u)
			}
		}
	}
}

func TestOnlyActiveMembersOfLiveSpacesWithLiveAccountsPass(t *testing.T) {
	r := newRig(t, "alice", "bob", "carol", "dave", "erin")
	s := r.newSpace("ualice", "S")
	// requested
	r.must(func(tx *sql.Tx) error { return RequestJoin(tx, t0, s.ID, "ubob", true) })
	if _, ok := r.role(s.ID, "ubob"); ok {
		t.Error("a person who only asked passes the gate")
	}
	// left and removed
	r.addMember(s.ID, "ucarol")
	r.addMember(s.ID, "udave")
	r.must(func(tx *sql.Tx) error { return Leave(tx, t0, s.ID, "ucarol") })
	r.must(func(tx *sql.Tx) error { return Remove(tx, t0, s.ID, "udave") })
	for _, u := range []string{"ucarol", "udave"} {
		if _, ok := r.role(s.ID, u); ok {
			t.Errorf("%s passes the gate after leaving or being removed", u)
		}
	}
	// a disabled and a deleted account
	r.addMember(s.ID, "uerin")
	r.exec(`UPDATE user SET status = 'disabled' WHERE id = 'uerin'`)
	if _, ok := r.role(s.ID, "uerin"); ok {
		t.Error("a disabled account passes the gate")
	}
	r.exec(`UPDATE user SET status = 'active', deleted_at = 1 WHERE id = 'uerin'`)
	if _, ok := r.role(s.ID, "uerin"); ok {
		t.Error("a deleted account passes the gate")
	}
	// a deleted space shuts everyone out, and a restore opens it again
	if _, ok := r.role(s.ID, "ualice"); !ok {
		t.Fatal("setup")
	}
	r.must(func(tx *sql.Tx) error { return Delete(tx, t0, s.ID) })
	if _, ok := r.role(s.ID, "ualice"); ok {
		t.Error("the owner passes the gate of a deleted space")
	}
	r.must(func(tx *sql.Tx) error { return Restore(tx, t0.Add(time.Hour), s.ID, 10*24*time.Hour) })
	if _, ok := r.role(s.ID, "ualice"); !ok {
		t.Error("a restored space stayed closed")
	}
}

func TestCodesAreRandomHashedAndNormalized(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 300; i++ {
		c, h, err := NewCode()
		if err != nil {
			t.Fatal(err)
		}
		if len(c) != 19 || strings.Count(c, "-") != 3 || seen[c] || h != HashCode(c) || strings.Contains(h, c) || len(h) != 64 {
			t.Fatalf("bad code %q hash %q", c, h)
		}
		if strings.ContainsAny(c, "ilo01") {
			t.Fatalf("look-alikes in %q", c)
		}
		seen[c] = true
	}
	c, _, _ := NewCode()
	for _, variant := range []string{strings.ToUpper(c), strings.ReplaceAll(c, "-", " "), "  " + c + "\n", strings.ReplaceAll(c, "-", "")} {
		if HashCode(variant) != HashCode(c) {
			t.Errorf("%q does not match the code it was typed from", variant)
		}
	}
}

func TestInviteLifecycle(t *testing.T) {
	r := newRig(t, "alice", "bob")
	s := r.newSpace("ualice", "S")
	var inv Invite
	var code string
	r.must(func(tx *sql.Tx) (err error) {
		inv, code, err = CreateInvite(tx, t0, KindSpace, s.ID, "ualice", "for Bob", 0)
		return
	})
	if inv.ExpiresAt.Sub(t0) != InviteTTL {
		t.Errorf("default lifetime: %v", inv.ExpiresAt.Sub(t0))
	}
	// Only the hash is stored.
	var n int
	_ = r.st.View(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COUNT(*) FROM invite WHERE code_hash = ?`, code).Scan(&n)
	})
	if n != 0 {
		t.Fatal("the invite code is stored in the clear")
	}
	look := func(c string, at time.Time) (Found, error) {
		var f Found
		var err error
		_ = r.st.View(context.Background(), func(tx *sql.Tx) error { f, err = Lookup(tx, at, c); return nil })
		return f, err
	}
	if f, err := look(strings.ToUpper(code), t0.Add(time.Hour)); err != nil || f.Kind != "invite" || f.SpaceID != s.ID || f.Name != "S" {
		t.Fatalf("lookup: %+v %v", f, err)
	}
	// Bad, empty, expired: all the same answer.
	for _, bad := range []string{"", "   ", "nonsense-code", "aaaa-aaaa-aaaa-aaaa", code[:10]} {
		if _, err := look(bad, t0); !errors.Is(err, ErrBadCode) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if _, err := look(code, t0.Add(InviteTTL+time.Second)); !errors.Is(err, ErrBadCode) {
		t.Errorf("an expired invite: %v", err)
	}
	// Spend once; the second spend and a later lookup both fail.
	r.must(func(tx *sql.Tx) error { return Spend(tx, t0.Add(time.Hour), inv.ID, "ubob") })
	if err := r.do(func(tx *sql.Tx) error { return Spend(tx, t0.Add(time.Hour), inv.ID, "ualice") }); !errors.Is(err, ErrBadCode) {
		t.Errorf("spending twice: %v", err)
	}
	if _, err := look(code, t0.Add(time.Hour)); !errors.Is(err, ErrBadCode) {
		t.Errorf("a used invite: %v", err)
	}
	// Revoked; and a revoke is scoped to the right space.
	var inv2 Invite
	var code2 string
	r.must(func(tx *sql.Tx) (err error) {
		inv2, code2, err = CreateInvite(tx, t0, KindSpace, s.ID, "ualice", "", time.Hour)
		return
	})
	other := r.newSpace("ubob", "Other")
	if err := r.do(func(tx *sql.Tx) error { return RevokeInvite(tx, inv2.ID, other.ID) }); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoking another space's invite: %v", err)
	}
	r.must(func(tx *sql.Tx) error { return RevokeInvite(tx, inv2.ID, s.ID) })
	if _, err := look(code2, t0); !errors.Is(err, ErrBadCode) {
		t.Errorf("a revoked invite: %v", err)
	}
	// Server invites carry no space; the combinations are checked.
	if err := r.do(func(tx *sql.Tx) error {
		_, _, err := CreateInvite(tx, t0, KindServer, s.ID, "ualice", "", 0)
		return err
	}); err == nil {
		t.Error("a server invite with a space was accepted")
	}
	if err := r.do(func(tx *sql.Tx) error { _, _, err := CreateInvite(tx, t0, KindSpace, "", "ualice", "", 0); return err }); err == nil {
		t.Error("a space invite with no space was accepted")
	}
	var sv Invite
	var svCode string
	r.must(func(tx *sql.Tx) (err error) {
		sv, svCode, err = CreateInvite(tx, t0, KindServer, "", "ualice", "", 0)
		return
	})
	if f, err := look(svCode, t0); err != nil || f.SpaceID != "" || f.Invite.ID != sv.ID {
		t.Errorf("server invite: %+v %v", f, err)
	}
	// An invite to a deleted space does not resolve.
	r.must(func(tx *sql.Tx) error { return Delete(tx, t0, s.ID) })
	var inv3 Invite
	var code3 string
	r.exec(`UPDATE space SET deleted_at = NULL WHERE id = ?`, s.ID)
	r.must(func(tx *sql.Tx) (err error) {
		inv3, code3, err = CreateInvite(tx, t0, KindSpace, s.ID, "ualice", "", 0)
		return
	})
	_ = inv3
	r.must(func(tx *sql.Tx) error { return Delete(tx, t0, s.ID) })
	if _, err := look(code3, t0); !errors.Is(err, ErrBadCode) {
		t.Errorf("an invite to a deleted space: %v", err)
	}
}

func TestOneInviteCannotBeSpentTwiceUnderRace(t *testing.T) {
	r := newRig(t, "alice", "bob", "carol", "dave")
	s := r.newSpace("ualice", "S")
	var inv Invite
	r.must(func(tx *sql.Tx) (err error) {
		inv, _, err = CreateInvite(tx, t0, KindSpace, s.ID, "ualice", "", 0)
		return
	})
	var wg sync.WaitGroup
	var mu sync.Mutex
	won := 0
	for _, u := range []string{"ubob", "ucarol", "udave", "ubob", "ucarol"} {
		wg.Add(1)
		go func(u string) {
			defer wg.Done()
			err := r.do(func(tx *sql.Tx) error { return Spend(tx, t0.Add(time.Minute), inv.ID, u) })
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				won++
			}
		}(u)
	}
	wg.Wait()
	if won != 1 {
		t.Fatalf("%d people spent one invite", won)
	}
}

func TestSpaceCode(t *testing.T) {
	r := newRig(t, "alice", "bob")
	s := r.newSpace("ualice", "S")
	look := func(c string) (Found, error) {
		var f Found
		var err error
		_ = r.st.View(context.Background(), func(tx *sql.Tx) error { f, err = Lookup(tx, t0, c); return nil })
		return f, err
	}
	var c1, c2 string
	r.must(func(tx *sql.Tx) (err error) { c1, err = RotateCode(tx, t0, s.ID); return })
	if f, err := look(c1); err != nil || f.Kind != "code" || f.SpaceID != s.ID || f.Invite != nil {
		t.Fatalf("lookup: %+v %v", f, err)
	}
	r.must(func(tx *sql.Tx) (err error) { c2, err = RotateCode(tx, t0, s.ID); return })
	if _, err := look(c1); !errors.Is(err, ErrBadCode) {
		t.Errorf("the old code still works after a rotate: %v", err)
	}
	if _, err := look(c2); err != nil {
		t.Errorf("the new code: %v", err)
	}
	var stored string
	_ = r.st.View(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT join_code_hash FROM space WHERE id = ?`, s.ID).Scan(&stored)
	})
	if stored == c2 || stored != HashCode(c2) {
		t.Error("the space code must be stored hashed")
	}
	r.must(func(tx *sql.Tx) error { return DisableCode(tx, s.ID) })
	if _, err := look(c2); !errors.Is(err, ErrBadCode) {
		t.Errorf("a disabled code: %v", err)
	}
	r.must(func(tx *sql.Tx) (err error) { c2, err = RotateCode(tx, t0, s.ID); return })
	r.must(func(tx *sql.Tx) error { return Delete(tx, t0, s.ID) })
	if _, err := look(c2); !errors.Is(err, ErrBadCode) {
		t.Errorf("a code of a deleted space: %v", err)
	}
}

func TestJoinNeedsConsentAndRequestsNeedApproval(t *testing.T) {
	r := newRig(t, "alice", "bob", "carol")
	s := r.newSpace("ualice", "S")
	if err := r.do(func(tx *sql.Tx) error { return JoinViaInvite(tx, t0, s.ID, "ubob", false) }); !errors.Is(err, ErrNeedConsent) {
		t.Fatalf("joining without consent: %v", err)
	}
	if err := r.do(func(tx *sql.Tx) error { return RequestJoin(tx, t0, s.ID, "ubob", false) }); !errors.Is(err, ErrNeedConsent) {
		t.Fatalf("asking without consent: %v", err)
	}
	if _, ok := r.role(s.ID, "ubob"); ok {
		t.Fatal("a refused join left a membership")
	}
	r.must(func(tx *sql.Tx) error { return RequestJoin(tx, t0, s.ID, "ubob", true) })
	r.must(func(tx *sql.Tx) error { return RequestJoin(tx, t0, s.ID, "ubob", true) }) // twice is harmless
	var reqs []Member
	_ = r.st.View(context.Background(), func(tx *sql.Tx) (err error) { reqs, err = Members(tx, s.ID, StateRequested); return })
	if len(reqs) != 1 || reqs[0].ConsentVersion != ConsentVersion {
		t.Fatalf("requests: %+v", reqs)
	}
	if _, ok := r.role(s.ID, "ubob"); ok {
		t.Fatal("asking is not joining")
	}
	r.must(func(tx *sql.Tx) error { return Approve(tx, t0.Add(time.Hour), s.ID, "ubob") })
	if role, ok := r.role(s.ID, "ubob"); !ok || role != RoleMember {
		t.Fatal("an approved person is not a member")
	}
	if err := r.do(func(tx *sql.Tx) error { return JoinViaInvite(tx, t0, s.ID, "ubob", true) }); !errors.Is(err, ErrAlreadyIn) {
		t.Errorf("joining twice: %v", err)
	}
	// Deny, then they may ask again.
	r.must(func(tx *sql.Tx) error { return RequestJoin(tx, t0, s.ID, "ucarol", true) })
	r.must(func(tx *sql.Tx) error { return Deny(tx, t0, s.ID, "ucarol") })
	if _, ok := r.role(s.ID, "ucarol"); ok {
		t.Error("a denied person is in")
	}
	r.must(func(tx *sql.Tx) error { return RequestJoin(tx, t0, s.ID, "ucarol", true) })
	// A request given under an older version of the join text cannot be approved.
	r.exec(`UPDATE membership SET consent_version = 0 WHERE space_id = ? AND user_id = 'ucarol'`, s.ID)
	if err := r.do(func(tx *sql.Tx) error { return Approve(tx, t0, s.ID, "ucarol") }); !errors.Is(err, ErrNeedConsent) {
		t.Errorf("approving a stale consent: %v", err)
	}
	if err := r.do(func(tx *sql.Tx) error { return Approve(tx, t0, s.ID, "unobody") }); !errors.Is(err, ErrNotMember) {
		t.Errorf("approving nobody: %v", err)
	}
}

func TestLeaveRemoveAndHandOver(t *testing.T) {
	r := newRig(t, "alice", "bob", "carol")
	s := r.newSpace("ualice", "S")
	r.addMember(s.ID, "ubob")
	r.addMember(s.ID, "ucarol")

	if err := r.do(func(tx *sql.Tx) error { return Leave(tx, t0, s.ID, "ualice") }); !errors.Is(err, ErrLastOwner) {
		t.Fatalf("the owner leaving: %v", err)
	}
	if err := r.do(func(tx *sql.Tx) error { return Remove(tx, t0, s.ID, "ualice") }); !errors.Is(err, ErrLastOwner) {
		t.Fatalf("removing the owner: %v", err)
	}
	r.must(func(tx *sql.Tx) error { return Leave(tx, t0, s.ID, "ubob") })
	if _, ok := r.role(s.ID, "ubob"); ok {
		t.Fatal("a member who left is still in")
	}
	if err := r.do(func(tx *sql.Tx) error { return Leave(tx, t0, s.ID, "ubob") }); !errors.Is(err, ErrNotMember) {
		t.Errorf("leaving twice: %v", err)
	}
	// A person who left comes back only by joining again, as an ordinary member.
	r.addMember(s.ID, "ubob")
	if role, ok := r.role(s.ID, "ubob"); !ok || role != RoleMember {
		t.Errorf("rejoin: %q %v", role, ok)
	}
	r.must(func(tx *sql.Tx) error { return Remove(tx, t0, s.ID, "ucarol") })
	if err := r.do(func(tx *sql.Tx) error { return Remove(tx, t0, s.ID, "ucarol") }); !errors.Is(err, ErrNotMember) {
		t.Errorf("removing someone already gone: %v", err)
	}
	// Hand over: a non-member or a non-owner cannot be used, and exactly one owner remains.
	if err := r.do(func(tx *sql.Tx) error { return Transfer(tx, s.ID, "ualice", "ucarol") }); !errors.Is(err, ErrNotMember) {
		t.Errorf("handing to someone removed: %v", err)
	}
	if err := r.do(func(tx *sql.Tx) error { return Transfer(tx, s.ID, "ubob", "ualice") }); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("a member handing over: %v", err)
	}
	if err := r.do(func(tx *sql.Tx) error { return Transfer(tx, s.ID, "ualice", "ualice") }); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("handing to oneself: %v", err)
	}
	r.must(func(tx *sql.Tx) error { return Transfer(tx, s.ID, "ualice", "ubob") })
	if role, _ := r.role(s.ID, "ubob"); role != RoleOwner {
		t.Error("the new owner is not the owner")
	}
	if role, _ := r.role(s.ID, "ualice"); role != RoleMember {
		t.Error("the old owner kept ownership")
	}
	var owners int
	_ = r.st.View(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COUNT(*) FROM membership WHERE space_id = ? AND role = 'owner' AND state = 'active'`, s.ID).Scan(&owners)
	})
	if owners != 1 {
		t.Errorf("%d owners", owners)
	}
	r.must(func(tx *sql.Tx) error { return Leave(tx, t0, s.ID, "ualice") }) // now they can leave
}

func TestSettingsDefaultsAndChanges(t *testing.T) {
	r := newRig(t, "alice", "bob")
	s := r.newSpace("ualice", "S")
	r.addMember(s.ID, "ubob")
	var ms []Member
	_ = r.st.View(context.Background(), func(tx *sql.Tx) (err error) { ms, err = Members(tx, s.ID, StateActive); return })
	for _, m := range ms {
		if !m.IncludeNotes || !m.IncludePhotos || m.ShowPartnerNames || m.Paused {
			t.Errorf("D-21 defaults wrong for %s: %+v", m.Username, m)
		}
	}
	r.must(func(tx *sql.Tx) error {
		return SetSettings(tx, s.ID, "ubob", Settings{IncludeNotes: false, IncludePhotos: true, ShowPartnerNames: true, Paused: true})
	})
	_ = r.st.View(context.Background(), func(tx *sql.Tx) (err error) { ms, err = Members(tx, s.ID, StateActive); return })
	for _, m := range ms {
		if m.Username == "bob" && (m.IncludeNotes || !m.ShowPartnerNames || !m.Paused) {
			t.Errorf("settings not saved: %+v", m)
		}
		if m.Username == "alice" && (!m.IncludeNotes || m.Paused) {
			t.Errorf("another member's settings changed: %+v", m)
		}
	}
	if err := r.do(func(tx *sql.Tx) error { return SetSettings(tx, s.ID, "unobody", Settings{}) }); !errors.Is(err, ErrNotFound) {
		t.Errorf("settings for a non-member: %v", err)
	}
}

func TestNamesAndWindows(t *testing.T) {
	r := newRig(t, "alice")
	for _, bad := range []string{"", "   ", strings.Repeat("x", 61)} {
		if err := r.do(func(tx *sql.Tx) error { _, err := Create(tx, t0, bad, "ualice"); return err }); !errors.Is(err, ErrBadName) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	s := r.newSpace("ualice", "  Tidy\x00 name\n ")
	if s.Name != "Tidy name" {
		t.Errorf("name %q", s.Name)
	}
	// Two spaces may share a name (so a name cannot be probed).
	r.newSpace("ualice", "Tidy name")
	r.must(func(tx *sql.Tx) error { return Rename(tx, s.ID, "Renamed") })
	r.must(func(tx *sql.Tx) error { return Delete(tx, t0, s.ID) })
	if err := r.do(func(tx *sql.Tx) error { return Restore(tx, t0.Add(11*24*time.Hour), s.ID, 10*24*time.Hour) }); !errors.Is(err, ErrExpired) {
		t.Errorf("restoring after the window: %v", err)
	}
	r.must(func(tx *sql.Tx) error { return Restore(tx, t0.Add(9*24*time.Hour), s.ID, 10*24*time.Hour) })
	var del []Space
	r.must(func(tx *sql.Tx) (err error) { del, err = DeletedFor(tx, "ualice"); return })
	if len(del) != 0 {
		t.Errorf("a restored space is listed as deleted: %v", del)
	}
}
