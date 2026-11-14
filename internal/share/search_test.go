package share

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/herbiewalker/alcove/internal/spaces"
)

// note gives a person an entry with a note, filed in the search index the way an upload files it.
func (r *rig) note(owner, id string, daysAgo int, text string) {
	r.t.Helper()
	at := t0.Add(-time.Duration(daysAgo) * 24 * time.Hour).UnixMilli()
	r.exec(`INSERT INTO cache_row(owner_id, tbl, rid, pos, data) VALUES (?,?,?,?,?)`, "u"+owner, "encounters", id, daysAgo,
		fmt.Sprintf(`{"id":%q,"startAt":%d,"note":%q}`, id, at, text))
	r.exec(`INSERT INTO search(owner_id, kind, ref, body) VALUES (?,?,?,?)`, "u"+owner, "encounter", id, text)
}

func (r *rig) partner(owner, pid, name, note string, entries ...string) {
	r.t.Helper()
	r.exec(`INSERT INTO cache_row(owner_id, tbl, rid, pos, data) VALUES (?,?,?,0,?)`, "u"+owner, "partners", pid, fmt.Sprintf(`{"id":%q,"displayName":%q}`, pid, name))
	r.exec(`INSERT INTO search(owner_id, kind, ref, body) VALUES (?,?,?,?)`, "u"+owner, "partner", pid, name+"\n"+note)
	for i, e := range entries {
		r.exec(`INSERT INTO cache_row(owner_id, tbl, rid, pos, data) VALUES (?,?,?,?,?)`, "u"+owner, "encounter_partner", fmt.Sprintf("#%s%d", pid, i), i,
			fmt.Sprintf(`{"encounterId":%q,"partnerId":%q}`, e, pid))
	}
}

func (r *rig) own(owner, q string) ([]Found, error) {
	r.t.Helper()
	var out []Found
	err := r.st.View(context.Background(), func(tx *sql.Tx) (err error) { out, err = SearchOwn(tx, "u"+owner, q, 50); return })
	return out, err
}

func (r *rig) inSpace(space, viewer, q string) ([]Found, error) {
	r.t.Helper()
	var out []Found
	err := r.st.View(context.Background(), func(tx *sql.Tx) (err error) {
		out, err = SearchSpace(tx, space, "u"+viewer, spaces.ConsentVersion, q, 50)
		return
	})
	return out, err
}

func ids(fs []Found) string {
	var out []string
	for _, f := range fs {
		out = append(out, f.OwnerID+":"+f.EntryID)
	}
	return strings.Join(out, ",")
}

func TestMatchBuildsASafeExpression(t *testing.T) {
	for in, want := range map[string]string{
		"anniversary":            `body : "anniversary"*`,
		"  weekend away  ":       `body : "weekend" AND "away"*`,
		"a OR b":                 `body : "a" AND "OR" AND "b"*`,
		`say "hello"`:            `body : "say" AND "hello"*`,
		"body:secret":            `body : "body" AND "secret"*`,
		"NEAR(x y)":              `body : "NEAR" AND "x" AND "y"*`,
		"wild*card":              `body : "wild" AND "card"*`,
		"café déjà":              `body : "café" AND "déjà"*`,
		"1 2 3 4 5 6 7 8 9 10 x": `body : "1" AND "2" AND "3" AND "4" AND "5" AND "6" AND "7" AND "8"*`,
	} {
		got, err := Match(in)
		if err != nil || got != want {
			t.Errorf("Match(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, short := range []string{"", " ", "a", "\"\"", "**", "  - "} {
		if _, err := Match(short); !errors.Is(err, ErrQueryShort) {
			t.Errorf("Match(%q): %v, want too short", short, err)
		}
	}
	if _, err := Match(strings.Repeat("a", 101)); !errors.Is(err, ErrQueryLong) {
		t.Errorf("101 characters: %v", err)
	}
	if got, _ := Match(strings.Repeat("w", 90)); !strings.Contains(got, strings.Repeat("w", 40)+`"*`) || strings.Contains(got, strings.Repeat("w", 41)) {
		t.Errorf("a long word is not cut to 40: %q", got)
	}
}

func TestHostileSearchTextNeverBreaksTheEngine(t *testing.T) {
	r := newRig(t, "ann", "bob")
	sp := r.space("ann", "bob")
	r.note("ann", "a1", 1, "ordinary words here")
	for _, q := range []string{`"`, `""""`, `a" OR "b`, `body:`, `NEAR(`, `)(`, `*`, `^x`, `x*y`, `' OR 1=1 --`, `; DROP TABLE search`, "\x00\x01\x02ab", strings.Repeat("(", 90), `{"a":1}`, `-x`, `a AND`, `OR OR`} {
		if _, err := r.own("ann", q); err != nil && !errors.Is(err, ErrQueryShort) {
			t.Errorf("own %q: %v", q, err)
		}
		if _, err := r.inSpace(sp, "ann", q); err != nil && !errors.Is(err, ErrQueryShort) {
			t.Errorf("space %q: %v", q, err)
		}
	}
	if n := countRows(r, "search"); n != 1 {
		t.Errorf("the index changed: %d rows", n)
	}
}

func countRows(r *rig, table string) int {
	var n int
	_ = r.st.View(context.Background(), func(tx *sql.Tx) error { return tx.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n) })
	return n
}

func TestOwnSearchFindsNotesAndPartnersNewestFirst(t *testing.T) {
	r := newRig(t, "ann", "bob")
	r.note("ann", "a1", 30, "Our anniversary dinner")
	r.note("ann", "a2", 10, "Quiet weekend, no plans")
	r.note("ann", "a3", 5, "Another anniversary surprise")
	r.note("ann", "a4", 2, "Nothing to see")
	r.note("bob", "b1", 1, "Bob anniversary")
	r.partner("ann", "pa1", "Jordan Rivers", "met at the lake", "a2", "a4")
	r.partner("bob", "pb1", "Jordan Bobson", "", "b1")

	got, err := r.own("ann", "anniv")
	if err != nil || ids(got) != "uann:a3,uann:a1" {
		t.Fatalf("prefix match: %q %v", ids(got), err)
	}
	if !got[0].Mine || got[0].OwnerName == "" || got[0].Local.IsZero() {
		t.Errorf("result fields: %+v", got[0])
	}
	// A hit is highlighted as plain text segments.
	var hit string
	for _, s := range got[0].Segs {
		if s.Hit {
			hit += s.Text
		}
	}
	if !strings.EqualFold(hit, "anniversary") {
		t.Errorf("highlight = %q in %+v", hit, got[0].Segs)
	}
	// Two words are ANDed.
	if got, _ = r.own("ann", "anniversary dinner"); ids(got) != "uann:a1" {
		t.Errorf("AND: %q", ids(got))
	}
	// Partner names find the entries the partner was in, and only the searcher's own.
	got, _ = r.own("ann", "jordan")
	if ids(got) != "uann:a4,uann:a2" || !got[0].Partner {
		t.Errorf("partner search: %q", ids(got))
	}
	if got, _ = r.own("ann", "lake"); ids(got) != "uann:a4,uann:a2" {
		t.Errorf("partner note: %q", ids(got))
	}
	// An entry that matches by note and by partner appears once.
	r.partner("ann", "pa2", "Anniversary Pat", "", "a1")
	if got, _ = r.own("ann", "anniversary"); ids(got) != "uann:a3,uann:a1" {
		t.Errorf("deduplicated: %q", ids(got))
	}
	// Bob's data is never in Ann's results, and nothing for a word nobody wrote.
	if got, _ = r.own("ann", "bobson"); len(got) != 0 {
		t.Errorf("another person's partner: %q", ids(got))
	}
	if got, _ = r.own("ann", "zzzzzz"); len(got) != 0 {
		t.Errorf("no match: %q", ids(got))
	}
}

func TestOwnSearchHonoursTheLimitAndNeverMarksUpText(t *testing.T) {
	r := newRig(t, "ann")
	for i := 1; i <= 60; i++ {
		r.note("ann", fmt.Sprintf("n%d", i), i, "common word <b>bold</b> & more")
	}
	got, err := r.own("ann", "common")
	if err != nil || len(got) != 50 {
		t.Fatalf("limit: %d %v", len(got), err)
	}
	for _, f := range got {
		for _, s := range f.Segs {
			if strings.ContainsAny(s.Text, "\x01\x02") {
				t.Errorf("a marker character leaked into text: %q", s.Text)
			}
		}
	}
	// The markup in the note is plain text for the template to escape.
	joined := ""
	for _, s := range got[0].Segs {
		joined += s.Text
	}
	if !strings.Contains(joined, "<b>") {
		t.Errorf("the note text was altered: %q", joined)
	}
}

func TestSpaceSearchFindsOnlyWhatTheSpaceShows(t *testing.T) {
	r := newRig(t, "ann", "bob", "cy")
	sp := r.space("ann", "bob")
	r.note("ann", "a1", 3, "ann picnic by the river")
	r.note("bob", "b1", 2, "bob picnic in the park")
	r.note("cy", "c1", 1, "cy picnic elsewhere")
	r.partner("bob", "pb1", "Picnic Partner", "", "b1")

	got, err := r.inSpace(sp, "ann", "picnic")
	if err != nil || ids(got) != "ubob:b1,uann:a1" {
		t.Fatalf("space search: %q %v", ids(got), err)
	}
	for _, f := range got {
		if f.Partner {
			t.Error("a partner match appeared in a space search")
		}
	}
	// Partner names are not searchable in a space even though the owner's own search finds them.
	if got, _ = r.inSpace(sp, "ann", "partner"); len(got) != 0 {
		t.Errorf("a partner name was found by another member: %q", ids(got))
	}
	// An outsider cannot search the space; a member on older wording is told to read again.
	if _, err := r.inSpace(sp, "cy", "picnic"); !errors.Is(err, ErrNotMember) {
		t.Errorf("outsider: %v", err)
	}
	// Tryst words in an entry that is not a note are not indexed, so nothing else can be found.
	if got, _ = r.inSpace(sp, "ann", "park"); ids(got) != "ubob:b1" {
		t.Errorf("single result: %q", ids(got))
	}
}

// Every way the list hides an entry hides it from search too, plus the owner's note choice.
func TestSpaceSearchIsClosedWhereTheListIsClosed(t *testing.T) {
	hide := map[string]func(r *rig, sp string){
		"paused": func(r *rig, sp string) {
			r.exec(`UPDATE membership SET paused = 1 WHERE space_id = ? AND user_id = 'ubob'`, sp)
		},
		"left": func(r *rig, sp string) {
			r.exec(`UPDATE membership SET state = 'left' WHERE space_id = ? AND user_id = 'ubob'`, sp)
		},
		"removed": func(r *rig, sp string) {
			r.exec(`UPDATE membership SET state = 'removed' WHERE space_id = ? AND user_id = 'ubob'`, sp)
		},
		"requested": func(r *rig, sp string) {
			r.exec(`UPDATE membership SET state = 'requested' WHERE space_id = ? AND user_id = 'ubob'`, sp)
		},
		"older wording": func(r *rig, sp string) {
			r.exec(`UPDATE membership SET consent_version = 1 WHERE space_id = ? AND user_id = 'ubob'`, sp)
		},
		"disabled": func(r *rig, sp string) { r.exec(`UPDATE user SET status = 'disabled' WHERE id = 'ubob'`) },
		"deleted":  func(r *rig, sp string) { r.exec(`UPDATE user SET deleted_at = 5 WHERE id = 'ubob'`) },
		"private": func(r *rig, sp string) {
			r.exec(`INSERT INTO entry_flag(owner_id, entry_id, private, updated_at) VALUES ('ubob','b1',1,0)`)
		},
		"notes left out": func(r *rig, sp string) {
			r.exec(`UPDATE membership SET include_notes = 0 WHERE space_id = ? AND user_id = 'ubob'`, sp)
		},
		"cache emptied": func(r *rig, sp string) { r.exec(`DELETE FROM cache_row WHERE owner_id = 'ubob'`) },
	}
	for name, do := range hide {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, "ann", "bob")
			sp := r.space("ann", "bob")
			r.note("bob", "b1", 1, "lighthouse weekend")
			if got, err := r.inSpace(sp, "ann", "lighthouse"); err != nil || ids(got) != "ubob:b1" {
				t.Fatalf("setup: %q %v", ids(got), err)
			}
			do(r, sp)
			if got, err := r.inSpace(sp, "ann", "lighthouse"); err != nil || len(got) != 0 {
				t.Errorf("still found: %q %v", ids(got), err)
			}
		})
	}
	r := newRig(t, "ann", "bob")
	sp := r.space("ann", "bob")
	r.note("bob", "b1", 1, "lighthouse weekend")
	r.exec(`UPDATE membership SET consent_version = 1 WHERE space_id = ? AND user_id = 'uann'`, sp)
	if _, err := r.inSpace(sp, "ann", "lighthouse"); !errors.Is(err, ErrNeedConsent) {
		t.Errorf("viewer on older wording: %v", err)
	}
	r.exec(`UPDATE space SET deleted_at = 5 WHERE id = ?`, sp)
	if _, err := r.inSpace(sp, "bob", "lighthouse"); !errors.Is(err, ErrNotMember) {
		t.Errorf("deleted space: %v", err)
	}
}

func TestSearchResultsUseTheOwnersTimeZone(t *testing.T) {
	r := newRig(t, "ann", "bob")
	sp := r.space("ann", "bob")
	r.note("bob", "b1", 1, "midnight snack")
	r.exec(`INSERT INTO user_tz(user_id, tz) VALUES ('ubob', 'Pacific/Auckland')`)
	got, _ := r.inSpace(sp, "ann", "midnight")
	if len(got) != 1 || got[0].Local.Location().String() != "Pacific/Auckland" {
		t.Errorf("zone: %+v", got)
	}
}

// An entry whose id happens to equal a partner's id must not drag that partner's entries into the results.
func TestSearchKindsDoNotMix(t *testing.T) {
	r := newRig(t, "ann")
	r.note("ann", "pa1", 3, "zebra crossing")
	r.note("ann", "a2", 2, "something else")
	r.partner("ann", "pa1", "Pat", "", "a2")
	got, _ := r.own("ann", "zebra")
	if ids(got) != "uann:pa1" {
		t.Errorf("the note of an entry named like a partner pulled in the partner's entries: %q", ids(got))
	}
}
