package adminlog

import (
	"database/sql"
	"testing"
	"time"
)

func TestFilterNarrowsTheLog(t *testing.T) {
	s := openStore(t)
	type row struct {
		actor, action, typ string
		at                 time.Duration
	}
	rows := []row{
		{"u1", "data.edit", "data", 0}, {"u1", "data.view", "data", time.Hour}, {"u2", "settings.tls.change", "settings", 2 * time.Hour},
		{System, "settings.networks.revert", "settings", 3 * time.Hour}, {"u1", "data.edit.x", "data", 4 * time.Hour}, {"u2", "user.create", "user", 24 * time.Hour},
	}
	if err := update(t, s, func(tx *sql.Tx) error {
		for _, r := range rows {
			if _, err := Append(tx, t0.Add(r.at), r.actor, r.action, r.typ, "id1", "f"); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	count := func(f Filter) (n, listed int) {
		t.Helper()
		if err := view(t, s, func(tx *sql.Tx) (err error) {
			if n, err = Count(tx, f); err != nil {
				return err
			}
			es, err := Find(tx, f, 0, 0)
			listed = len(es)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return
	}
	cases := []struct {
		name string
		f    Filter
		want int
	}{
		{"everything", Filter{}, 6},
		{"one actor", Filter{Actor: "u1"}, 3},
		{"the server itself", Filter{Actor: System}, 1},
		{"an action and what starts with it", Filter{Action: "data.edit"}, 2},
		{"a group", Filter{Action: "settings."}, 2},
		{"a type", Filter{TargetType: "user"}, 1},
		{"a day", Filter{From: t0, To: t0.Add(23 * time.Hour)}, 5},
		{"from", Filter{From: t0.Add(24 * time.Hour)}, 1},
		{"all together", Filter{Actor: "u1", Action: "data.", TargetType: "data", From: t0.Add(30 * time.Minute)}, 2},
		{"nothing matches", Filter{Actor: "u9"}, 0},
		{"a wildcard is plain text", Filter{Action: "%"}, 0},
		{"an underscore is plain text", Filter{Action: "data_edit"}, 0},
	}
	for _, c := range cases {
		if n, listed := count(c.f); n != c.want || listed != c.want {
			t.Errorf("%s: count %d, listed %d, want %d", c.name, n, listed, c.want)
		}
	}
	// Paging keeps newest first.
	if err := view(t, s, func(tx *sql.Tx) error {
		es, err := Find(tx, Filter{Actor: "u1"}, 2, 1)
		if err != nil || len(es) != 2 || es[0].Seq < es[1].Seq {
			t.Errorf("paging: %v %v", es, err)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if ValidFilterText("a b") || ValidFilterText("x'; DROP TABLE admin_log;--") || !ValidFilterText("data.edit") {
		t.Error("filter text validation is wrong")
	}
}
