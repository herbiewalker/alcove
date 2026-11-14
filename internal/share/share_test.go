package share

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/herbiewalker/alcove/internal/keyfile"
	"github.com/herbiewalker/alcove/internal/spaces"
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

// space makes a space owned by the first user with the others as active members who agreed to the current wording.
func (r *rig) space(owner string, members ...string) string {
	r.t.Helper()
	var id string
	if err := r.st.Update(context.Background(), false, func(tx *sql.Tx) error {
		sp, err := spaces.Create(tx, t0, "Test", "u"+owner)
		id = sp.ID
		return err
	}); err != nil {
		r.t.Fatal(err)
	}
	for _, m := range members {
		r.exec(`INSERT INTO membership(space_id, user_id, role, state, created_at, joined_at, consent_version, consent_at) VALUES (?,?,?,?,?,?,?,?)`,
			id, "u"+m, "member", "active", t0.Unix(), t0.Unix(), spaces.ConsentVersion, t0.Unix())
	}
	return id
}

// entries gives a person n entries, ids "<owner>-1".., each with a distinctive note.
func (r *rig) entries(owner string, n int) {
	r.t.Helper()
	for i := 1; i <= n; i++ {
		id := fmt.Sprintf("%s-%d", owner, i)
		at := t0.Add(-time.Duration(i) * 24 * time.Hour).UnixMilli()
		r.exec(`INSERT INTO cache_row(owner_id, tbl, rid, pos, data) VALUES (?,?,?,?,?)`, "u"+owner, "encounters", id, i,
			fmt.Sprintf(`{"id":%q,"startAt":%d,"durationMin":30,"satisfactionRating":4,"note":"note of %s"}`, id, at, id))
	}
}

func (r *rig) see(space, viewer string, q Query) ([]Entry, int, error) {
	r.t.Helper()
	var out []Entry
	var total int
	err := r.st.View(context.Background(), func(tx *sql.Tx) (err error) {
		out, total, err = Entries(tx, space, "u"+viewer, spaces.ConsentVersion, q)
		return
	})
	return out, total, err
}

func owners(es []Entry) string {
	seen := map[string]bool{}
	var out []string
	for _, e := range es {
		if !seen[e.OwnerID] {
			seen[e.OwnerID] = true
			out = append(out, e.OwnerID)
		}
	}
	return strings.Join(out, ",")
}

func TestMembersSeeEachOther(t *testing.T) {
	r := newRig(t, "ann", "bob", "cy")
	sp := r.space("ann", "bob")
	r.entries("ann", 2)
	r.entries("bob", 3)
	r.entries("cy", 4) // not a member
	es, total, err := r.see(sp, "ann", Query{})
	if err != nil || total != 5 || len(es) != 5 {
		t.Fatalf("ann sees %d of %d, %v", len(es), total, err)
	}
	if strings.Contains(owners(es), "ucy") {
		t.Fatal("a non-member's entries are visible in the space")
	}
	mine := 0
	for _, e := range es {
		if e.Mine {
			mine++
		}
		if e.Note == "" || e.OwnerName == "" || e.Duration == nil || e.Rating == nil {
			t.Errorf("entry incomplete: %+v", e)
		}
	}
	if mine != 2 {
		t.Errorf("Mine flagged on %d entries, want 2", mine)
	}
	for i := 1; i < len(es); i++ {
		if es[i].Start.After(es[i-1].Start) {
			t.Fatal("not newest first")
		}
	}
}

// Each case removes bob's entries from what ann sees. If a case fails to hide them, the matching
// condition in the gate is not load-bearing.
func TestWhatHidesAnEntry(t *testing.T) {
	cases := []struct {
		name string
		do   func(r *rig, sp string)
	}{
		{"paused", func(r *rig, sp string) {
			r.exec(`UPDATE membership SET paused = 1 WHERE space_id = ? AND user_id = 'ubob'`, sp)
		}},
		{"left", func(r *rig, sp string) {
			r.exec(`UPDATE membership SET state = 'left' WHERE space_id = ? AND user_id = 'ubob'`, sp)
		}},
		{"removed", func(r *rig, sp string) {
			r.exec(`UPDATE membership SET state = 'removed' WHERE space_id = ? AND user_id = 'ubob'`, sp)
		}},
		{"asked but not admitted", func(r *rig, sp string) {
			r.exec(`UPDATE membership SET state = 'requested' WHERE space_id = ? AND user_id = 'ubob'`, sp)
		}},
		{"older consent wording", func(r *rig, sp string) {
			r.exec(`UPDATE membership SET consent_version = ? WHERE space_id = ? AND user_id = 'ubob'`, spaces.ConsentVersion-1, sp)
		}},
		{"account disabled", func(r *rig, sp string) { r.exec(`UPDATE user SET status = 'disabled' WHERE id = 'ubob'`) }},
		{"account deleted", func(r *rig, sp string) { r.exec(`UPDATE user SET deleted_at = ? WHERE id = 'ubob'`, t0.Unix()) }},
		{"every entry private", func(r *rig, sp string) {
			for i := 1; i <= 3; i++ {
				r.exec(`INSERT INTO entry_flag(owner_id, entry_id, private, updated_at) VALUES ('ubob', ?, 1, 0)`, fmt.Sprintf("bob-%d", i))
			}
		}},
		{"entries gone from the cache", func(r *rig, sp string) { r.exec(`DELETE FROM cache_row WHERE owner_id = 'ubob'`) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, "ann", "bob")
			sp := r.space("ann", "bob")
			r.entries("ann", 1)
			r.entries("bob", 3)
			if es, _, _ := r.see(sp, "ann", Query{}); len(es) != 4 {
				t.Fatalf("setup: ann sees %d, want 4", len(es))
			}
			c.do(r, sp)
			es, total, err := r.see(sp, "ann", Query{})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(owners(es), "ubob") || total != 1 {
				t.Errorf("bob's entries still visible: %d shown, total %d", len(es), total)
			}
		})
	}
}

func TestOneEntryPrivateOthersStay(t *testing.T) {
	r := newRig(t, "ann", "bob")
	sp := r.space("ann", "bob")
	r.entries("bob", 3)
	r.exec(`INSERT INTO entry_flag(owner_id, entry_id, private, updated_at) VALUES ('ubob','bob-2',1,0)`)
	es, _, _ := r.see(sp, "ann", Query{})
	if len(es) != 2 {
		t.Fatalf("saw %d, want 2", len(es))
	}
	for _, e := range es {
		if e.ID == "bob-2" {
			t.Fatal("the private entry is visible")
		}
	}
	// Turning the flag off shares it again.
	r.exec(`UPDATE entry_flag SET private = 0`)
	if es, _, _ = r.see(sp, "ann", Query{}); len(es) != 3 {
		t.Fatalf("unflagged entry not back: %d", len(es))
	}
}

func TestSpaceDeletedAndOutsiders(t *testing.T) {
	r := newRig(t, "ann", "bob", "cy")
	sp := r.space("ann", "bob")
	other := r.space("cy")
	r.entries("ann", 1)
	r.entries("bob", 1)
	r.entries("cy", 1)
	// An outsider asking for the space gets ErrNotMember, never an empty list that proves it exists.
	if _, _, err := r.see(sp, "cy", Query{}); !errors.Is(err, ErrNotMember) {
		t.Errorf("outsider: %v", err)
	}
	// Another space does not leak: ann is not in cy's space, and cy's entries are not in ann's.
	if _, _, err := r.see(other, "ann", Query{}); !errors.Is(err, ErrNotMember) {
		t.Errorf("other space: %v", err)
	}
	if es, _, _ := r.see(other, "cy", Query{}); len(es) != 1 || es[0].OwnerID != "ucy" {
		t.Errorf("cy's own space: %+v", es)
	}
	// A member who left cannot read any more.
	r.exec(`UPDATE membership SET state = 'left' WHERE space_id = ? AND user_id = 'ubob'`, sp)
	if _, _, err := r.see(sp, "bob", Query{}); !errors.Is(err, ErrNotMember) {
		t.Errorf("left member: %v", err)
	}
	// A deleted space shows nothing to anyone.
	r.exec(`UPDATE space SET deleted_at = ? WHERE id = ?`, t0.Unix(), sp)
	if _, _, err := r.see(sp, "ann", Query{}); !errors.Is(err, ErrNotMember) {
		t.Errorf("deleted space: %v", err)
	}
}

func TestDisabledViewerCannotRead(t *testing.T) {
	r := newRig(t, "ann", "bob")
	sp := r.space("ann", "bob")
	r.entries("bob", 1)
	r.exec(`UPDATE user SET status = 'disabled' WHERE id = 'uann'`)
	if _, _, err := r.see(sp, "ann", Query{}); !errors.Is(err, ErrNotMember) {
		t.Errorf("disabled viewer: %v", err)
	}
}

func TestIncludeNotesAndOwnMirror(t *testing.T) {
	r := newRig(t, "ann", "bob")
	sp := r.space("ann", "bob")
	r.entries("ann", 1)
	r.entries("bob", 1)
	r.exec(`UPDATE membership SET include_notes = 0 WHERE space_id = ? AND user_id = 'ubob'`, sp)
	es, _, _ := r.see(sp, "ann", Query{})
	for _, e := range es {
		switch e.OwnerID {
		case "ubob":
			if e.Note != "" {
				t.Error("bob's note is shown although he left notes out")
			}
		case "uann":
			if e.Note == "" {
				t.Error("ann's note missing")
			}
		}
	}
	// The mirror: when ann pauses, ann sees none of her own entries in the space either.
	r.exec(`UPDATE membership SET paused = 1 WHERE space_id = ? AND user_id = 'uann'`, sp)
	es, _, _ = r.see(sp, "ann", Query{})
	if strings.Contains(owners(es), "uann") {
		t.Error("a paused member's own entries are still listed")
	}
}

func TestRangeAndPaging(t *testing.T) {
	r := newRig(t, "ann", "bob")
	sp := r.space("ann", "bob")
	r.entries("ann", 10)
	r.entries("bob", 10)
	es, total, _ := r.see(sp, "ann", Query{Limit: 7, Offset: 5})
	if total != 20 || len(es) != 7 {
		t.Fatalf("paging: %d of %d", len(es), total)
	}
	from, to := t0.Add(-5*24*time.Hour-time.Hour), t0.Add(-2*24*time.Hour+time.Hour)
	es, total, _ = r.see(sp, "ann", Query{From: from, To: to})
	if total != 8 || len(es) != 8 { // days 2..5, two people
		t.Fatalf("range: %d of %d", len(es), total)
	}
	for _, e := range es {
		if e.Start.Before(from) || !e.Start.Before(to) {
			t.Errorf("entry outside the range: %v", e.Start)
		}
	}
}

func TestBadRowsAreSkippedNotFatal(t *testing.T) {
	r := newRig(t, "ann")
	sp := r.space("ann")
	r.entries("ann", 1)
	r.exec(`INSERT INTO cache_row(owner_id, tbl, rid, pos, data) VALUES ('uann','encounters','x',9,'{"id":"x","startAt":"soon"}')`)
	r.exec(`INSERT INTO cache_row(owner_id, tbl, rid, pos, data) VALUES ('uann','encounters','y',10,'{"id":"y"}')`)
	es, _, err := r.see(sp, "ann", Query{})
	if err != nil || len(es) != 1 {
		t.Fatalf("%d entries, %v", len(es), err)
	}
}

func TestSetPrivateAndOwn(t *testing.T) {
	r := newRig(t, "ann", "bob")
	r.entries("ann", 3)
	r.entries("bob", 1)
	do := func(owner, id string, p bool) error {
		return r.st.Update(context.Background(), false, func(tx *sql.Tx) error { return SetPrivate(tx, t0, "u"+owner, id, p) })
	}
	if err := do("ann", "ann-2", true); err != nil {
		t.Fatal(err)
	}
	// Ann cannot flag bob's entry, nor an entry that does not exist, and nothing is recorded for them.
	if err := do("ann", "bob-1", true); !errors.Is(err, ErrNoEntry) {
		t.Errorf("flagging another person's entry: %v", err)
	}
	if err := do("ann", "nope", true); !errors.Is(err, ErrNoEntry) {
		t.Errorf("flagging a missing entry: %v", err)
	}
	var n int
	_ = r.st.View(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COUNT(*) FROM entry_flag`).Scan(&n)
	})
	if n != 1 {
		t.Errorf("%d flag rows, want 1", n)
	}
	var mine []Mine
	var total int
	_ = r.st.View(context.Background(), func(tx *sql.Tx) (err error) { mine, total, err = Own(tx, "uann", 10, 0); return })
	if total != 3 || len(mine) != 3 {
		t.Fatalf("own: %d of %d", len(mine), total)
	}
	for _, m := range mine {
		if m.OwnerID != "uann" || m.Private != (m.ID == "ann-2") {
			t.Errorf("own entry %s private=%v", m.ID, m.Private)
		}
	}
	// And back.
	if err := do("ann", "ann-2", false); err != nil {
		t.Fatal(err)
	}
	_ = r.st.View(context.Background(), func(tx *sql.Tx) (err error) { mine, _, err = Own(tx, "uann", 10, 0); return })
	for _, m := range mine {
		if m.Private {
			t.Error("still private")
		}
	}
}

func TestFlagSurvivesANewRevision(t *testing.T) {
	r := newRig(t, "ann", "bob")
	sp := r.space("ann", "bob")
	r.entries("bob", 2)
	r.exec(`INSERT INTO entry_flag(owner_id, entry_id, private, updated_at) VALUES ('ubob','bob-1',1,0)`)
	// A new upload rebuilds the cache: the rows are replaced, the flag stays.
	r.exec(`DELETE FROM cache_row WHERE owner_id = 'ubob'`)
	r.entries("bob", 2)
	es, _, _ := r.see(sp, "ann", Query{})
	if len(es) != 1 || es[0].ID != "bob-2" {
		t.Fatalf("after rebuild: %+v", es)
	}
}
