package adminlog

import (
	"database/sql"
	"testing"
	"time"
)

// About must return only lines that name the person as target and were made by someone else, and
// a person whose id is the start of another's must not see the other's lines (D-51).
func TestAboutIsOnlyThatPersonsLinesByOthers(t *testing.T) {
	s := openStore(t)
	type row struct{ actor, action, typ, target string }
	rows := []row{
		{"adm", "data.view", "user", "u1:"},               // about u1
		{"adm", "data.edit", "encounters", "u1:e7"},       // about u1
		{"adm", "data.edit", "encounters", "u10:e7"},      // about u10, not u1
		{"adm", "user.password.reset", "user", "u1"},      // about u1
		{"adm", "user.password.reset", "user", "u10"},     // about u10
		{"u1", "data.view", "user", "u1:"},                // u1's own act
		{System, "user.totp.reset.cli", "user", "u1"},     // about u1, by the system
		{"adm", "settings.tls.change", "settings", "tls"}, // not about anyone
		{"adm", "session.login", "user", "u1"},            // not one of the asked actions
		{"adm", "data.view", "user", "x1:u1"},             // someone else's, with u1 inside
	}
	if err := update(t, s, func(tx *sql.Tx) error {
		for i, r := range rows {
			if _, err := Append(tx, t0.Add(time.Duration(i)*time.Minute), r.actor, r.action, r.typ, r.target, "f"); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	actions := []string{"data.view", "data.edit", "user.password.reset", "user.totp.reset.cli"}
	var got []Entry
	var total int
	if err := view(t, s, func(tx *sql.Tx) (err error) {
		got, total, err = About(tx, "u1", actions, 50, 0)
		return
	}); err != nil {
		t.Fatal(err)
	}
	if total != 4 || len(got) != 4 {
		t.Fatalf("u1: total %d, listed %d, want 4: %+v", total, len(got), got)
	}
	if got[0].Action != "user.totp.reset.cli" || got[3].Action != "data.view" {
		t.Errorf("not newest first: %+v", got)
	}
	for _, e := range got {
		if e.Actor == "u1" {
			t.Errorf("the person's own act is listed: %+v", e)
		}
	}
	// Paging and the empty cases.
	if err := view(t, s, func(tx *sql.Tx) (err error) {
		got, total, err = About(tx, "u1", actions, 2, 2)
		return
	}); err != nil || total != 4 || len(got) != 2 {
		t.Errorf("page 2: %d %d %v", total, len(got), err)
	}
	if err := view(t, s, func(tx *sql.Tx) (err error) {
		if got, total, err = About(tx, "", actions, 50, 0); err != nil || total != 0 || len(got) != 0 {
			t.Errorf("no person: %v", err)
		}
		if got, total, err = About(tx, "u1", nil, 50, 0); err != nil || total != 0 || len(got) != 0 {
			t.Errorf("no actions: %v", err)
		}
		got, total, err = About(tx, "u2", actions, 50, 0)
		return
	}); err != nil || total != 0 {
		t.Errorf("a stranger: %d %v", total, err)
	}
}
