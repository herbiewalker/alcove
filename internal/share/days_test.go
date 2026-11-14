package share

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/herbiewalker/alcove/internal/spaces"
)

func (r *rig) days(space, viewer string, from, to time.Time) ([]Day, error) {
	r.t.Helper()
	var out []Day
	err := r.st.View(context.Background(), func(tx *sql.Tx) (err error) {
		out, err = Days(tx, space, "u"+viewer, spaces.ConsentVersion, from, to)
		return
	})
	return out, err
}

func sumDays(ds []Day) int {
	n := 0
	for _, d := range ds {
		for _, c := range d.ByUser {
			n += c
		}
	}
	return n
}

func TestDaysCountPerMemberPerDayInUTC(t *testing.T) {
	r := newRig(t, "ann", "bob", "cy")
	sp := r.space("ann", "bob")
	day := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	add := func(owner, id string, at time.Time) {
		r.exec(`INSERT INTO cache_row(owner_id, tbl, rid, pos, data) VALUES (?,?,?,0,?)`, "u"+owner, "encounters", id,
			fmt.Sprintf(`{"id":%q,"startAt":%d}`, id, at.UnixMilli()))
	}
	add("ann", "a1", day.Add(23*time.Hour+59*time.Minute)) // last minute of the 10th UTC
	add("ann", "a2", day.Add(24*time.Hour))                // first moment of the 11th
	add("ann", "a3", day.Add(26*time.Hour))
	add("bob", "b1", day.Add(2*time.Hour))
	add("cy", "c1", day.Add(2*time.Hour)) // not a member
	ds, err := r.days(sp, "ann", day.AddDate(0, 0, -1), day.AddDate(0, 0, 5))
	if err != nil || len(ds) != 2 {
		t.Fatalf("%v %+v", err, ds)
	}
	if ds[0].Date != "2026-11-10" || ds[0].ByUser["uann"] != 1 || ds[0].ByUser["ubob"] != 1 || len(ds[0].ByUser) != 2 {
		t.Errorf("the 10th: %+v", ds[0])
	}
	if ds[1].Date != "2026-11-11" || ds[1].ByUser["uann"] != 2 || len(ds[1].ByUser) != 1 {
		t.Errorf("the 11th: %+v", ds[1])
	}
	// The range is half open: [from, to).
	ds, _ = r.days(sp, "ann", day.Add(24*time.Hour), day.Add(25*time.Hour))
	if len(ds) != 1 || ds[0].ByUser["uann"] != 1 {
		t.Errorf("half-open range: %+v", ds)
	}
}

// Days must never show a day that Entries would not: every way of hiding an entry hides it here too.
func TestDaysObeyTheSameGateAsEntries(t *testing.T) {
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
			r.exec(`INSERT INTO entry_flag SELECT owner_id, rid, 1, 0 FROM cache_row WHERE owner_id = 'ubob'`)
		},
		"cache emptied": func(r *rig, sp string) { r.exec(`DELETE FROM cache_row WHERE owner_id = 'ubob'`) },
	}
	wide := func() (time.Time, time.Time) { return t0.AddDate(-1, 0, 0), t0.AddDate(1, 0, 0) }
	for name, do := range hide {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, "ann", "bob")
			sp := r.space("ann", "bob")
			r.entries("ann", 2)
			r.entries("bob", 3)
			from, to := wide()
			before, _ := r.days(sp, "ann", from, to)
			if sumDays(before) != 5 {
				t.Fatalf("setup: %d", sumDays(before))
			}
			do(r, sp)
			after, err := r.days(sp, "ann", from, to)
			es, total, _ := r.see(sp, "ann", Query{From: from, To: to})
			if err != nil || sumDays(after) != 2 || total != 2 || len(es) != 2 {
				t.Errorf("days %d, entries %d: not hidden or not in step", sumDays(after), total)
			}
			for _, d := range after {
				if d.ByUser["ubob"] != 0 {
					t.Errorf("bob still on %s", d.Date)
				}
			}
		})
	}
}

func TestDaysNeedMembershipAndConsent(t *testing.T) {
	r := newRig(t, "ann", "bob", "cy")
	sp := r.space("ann", "bob")
	r.entries("bob", 1)
	from, to := t0.AddDate(-1, 0, 0), t0.AddDate(1, 0, 0)
	if _, err := r.days(sp, "cy", from, to); !errors.Is(err, ErrNotMember) {
		t.Errorf("outsider: %v", err)
	}
	r.exec(`UPDATE membership SET consent_version = 1 WHERE space_id = ? AND user_id = 'uann'`, sp)
	if _, err := r.days(sp, "ann", from, to); !errors.Is(err, ErrNeedConsent) {
		t.Errorf("older wording: %v", err)
	}
	r.exec(`UPDATE space SET deleted_at = 5 WHERE id = ?`, sp)
	if _, err := r.days(sp, "bob", from, to); !errors.Is(err, ErrNotMember) {
		t.Errorf("deleted space: %v", err)
	}
}
