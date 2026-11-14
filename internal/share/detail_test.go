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

// fullEntry gives a person one rich entry "e1" plus the catalogs and partners it refers to.
func (r *rig) fullEntry(owner string) {
	r.t.Helper()
	at := t0.Add(-48 * time.Hour).UnixMilli()
	row := func(tbl, rid string, pos int, data string) {
		r.exec(`INSERT INTO cache_row(owner_id, tbl, rid, pos, data) VALUES (?,?,?,?,?)`, "u"+owner, tbl, rid, pos, data)
	}
	row("encounters", "e1", 1, fmt.Sprintf(`{"id":"e1","startAt":%d,"durationMin":45,"satisfactionRating":5,"note":"SECRET-NOTE","mood":"RELAXED","initiator":"MUTUAL",`+
		`"protectionUsed":"CONDOM,OTHER_THING","orgasmCountSelf":2,"practicesPerformed":"custom:a1,custom:gone","practicesReceived":"custom:a2",`+
		`"positions":"custom:p1","toys":"custom:t1","occasions":"custom:o1","contexts":"HOME,OUTDOORS","kinks":null,`+
		`"ejaculationLocations":"1=custom:x1|custom:x2,2=custom:x1"}`, at))
	row("acts", "a1", 0, `{"id":"a1","label":"Kissing"}`)
	row("acts", "a2", 1, `{"id":"a2","label":"Cuddling"}`)
	row("positions", "p1", 0, `{"id":"p1","label":"Spooning"}`)
	row("toys", "t1", 0, `{"id":"t1","label":"Silk scarf"}`)
	row("occasions", "o1", 0, `{"id":"o1","label":"Anniversary"}`)
	row("ejaculation_locations", "x1", 0, `{"id":"x1","label":"Chest"}`)
	row("ejaculation_locations", "x2", 1, `{"id":"x2","label":"Back"}`)
	row("partners", "pa1", 0, `{"id":"pa1","displayName":"Pat Partner","isAnonymous":0}`)
	row("partners", "pa2", 1, `{"id":"pa2","displayName":null,"isAnonymous":1}`)
	row("partners", "pa3", 2, `{"id":"pa3","displayName":"Not In This Entry","isAnonymous":0}`)
	row("encounter_partner", "#0", 0, `{"encounterId":"e1","partnerId":"pa1"}`)
	row("encounter_partner", "#1", 1, `{"encounterId":"e1","partnerId":"pa2"}`)
	row("encounter_partner", "#2", 2, `{"encounterId":"other","partnerId":"pa3"}`)
}

func (r *rig) detail(space, viewer, owner, id string) (Detail, error) {
	r.t.Helper()
	var d Detail
	err := r.st.View(context.Background(), func(tx *sql.Tx) (err error) {
		d, err = EntryDetail(tx, space, "u"+viewer, spaces.ConsentVersion, "u"+owner, id)
		return
	})
	return d, err
}

func (d Detail) text() string {
	var b strings.Builder
	for _, f := range d.Fields {
		b.WriteString(f.Label + "=" + f.Value + ";")
	}
	for _, g := range d.Groups {
		b.WriteString(g.Label + "=" + strings.Join(g.Items, "+") + ";")
	}
	b.WriteString("partners=" + strings.Join(d.Partner, "+") + ";")
	b.WriteString("note=" + d.Note)
	return b.String()
}

func TestOwnDetailShowsEverythingWithNamesResolved(t *testing.T) {
	r := newRig(t, "ann")
	r.fullEntry("ann")
	var d Detail
	err := r.st.View(context.Background(), func(tx *sql.Tx) (err error) { d, err = OwnDetail(tx, "uann", "e1"); return })
	if err != nil {
		t.Fatal(err)
	}
	txt := d.text()
	for _, want := range []string{
		"Length=45 min;", "Rating=5 of 5;", "Mood=Relaxed;", "Who started it=Mutual;", "Protection=Condom, Other thing;", "Orgasms=2;",
		"Acts done=(a removed item)+Kissing;", "Acts received=Cuddling;", "Positions=Spooning;", "Toys=Silk scarf;", "Occasions=Anniversary;",
		"Settings=Home+Outdoors;", "Finish locations=Back+Chest;", "partners=Pat Partner+An anonymous partner;", "note=SECRET-NOTE",
	} {
		if !strings.Contains(txt, want) {
			t.Errorf("the page lacks %q\n  got: %s", want, txt)
		}
	}
	if strings.Contains(txt, "Not In This Entry") {
		t.Error("a partner of another entry is listed")
	}
	if d.PartnersHidden != 0 || d.NoteHidden || !d.Mine || d.Private {
		t.Errorf("flags: %+v", d)
	}
	// Private is reported.
	r.exec(`INSERT INTO entry_flag(owner_id, entry_id, private, updated_at) VALUES ('uann','e1',1,0)`)
	_ = r.st.View(context.Background(), func(tx *sql.Tx) (err error) { d, err = OwnDetail(tx, "uann", "e1"); return })
	if !d.Private {
		t.Error("the private flag is not shown to the owner")
	}
	// Another person cannot read it through OwnDetail, and a missing id is the same error.
	r2 := newRig(t, "ann", "bob")
	r2.fullEntry("ann")
	for _, c := range []struct{ owner, id string }{{"ubob", "e1"}, {"uann", "nope"}} {
		err := r2.st.View(context.Background(), func(tx *sql.Tx) error { _, e := OwnDetail(tx, c.owner, c.id); return e })
		if !errors.Is(err, ErrNoEntry) {
			t.Errorf("%v: %v", c, err)
		}
	}
}

func TestSharedDetailFollowsTheOwnersChoices(t *testing.T) {
	r := newRig(t, "ann", "bob")
	sp := r.space("ann", "bob")
	r.fullEntry("bob")
	// Defaults: notes on, partner names off.
	d, err := r.detail(sp, "ann", "bob", "e1")
	if err != nil {
		t.Fatal(err)
	}
	txt := d.text()
	if !strings.Contains(txt, "note=SECRET-NOTE") || d.Note != "SECRET-NOTE" {
		t.Error("the note should be shown by default")
	}
	for _, leak := range []string{"Pat Partner", "anonymous partner"} {
		if strings.Contains(txt, leak) {
			t.Errorf("a partner is named to another member: %q", leak)
		}
	}
	if d.PartnersHidden != 2 || len(d.Partner) != 0 || d.Mine {
		t.Errorf("partners hidden=%d named=%d mine=%v", d.PartnersHidden, len(d.Partner), d.Mine)
	}
	if !strings.Contains(txt, "Acts done=(a removed item)+Kissing;") || !strings.Contains(txt, "Finish locations=Back+Chest;") {
		t.Errorf("the labels are not resolved: %s", txt)
	}
	// The owner turns partner names on and notes off.
	r.exec(`UPDATE membership SET show_partner_names = 1, include_notes = 0 WHERE space_id = ? AND user_id = 'ubob'`, sp)
	d, _ = r.detail(sp, "ann", "bob", "e1")
	if strings.Contains(d.text(), "SECRET-NOTE") || d.Note != "" || !d.NoteHidden {
		t.Errorf("the note is shown although the owner left notes out: %q", d.Note)
	}
	if !strings.Contains(d.text(), "partners=Pat Partner+An anonymous partner;") || d.PartnersHidden != 0 {
		t.Errorf("the owner allowed names: %s", d.text())
	}
	// A member sees their own entry through the same page.
	if d, err = r.detail(sp, "bob", "bob", "e1"); err != nil || !d.Mine {
		t.Errorf("own entry via the space: %v %v", err, d.Mine)
	}
}

// Every way the list hides an entry must hide its page too, and a wrong owner or id is the same refusal.
func TestSharedDetailIsClosedWhereTheListIsClosed(t *testing.T) {
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
			r.exec(`INSERT INTO entry_flag(owner_id, entry_id, private, updated_at) VALUES ('ubob','e1',1,0)`)
		},
		"cache emptied": func(r *rig, sp string) { r.exec(`DELETE FROM cache_row WHERE owner_id = 'ubob'`) },
	}
	for name, do := range hide {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, "ann", "bob")
			sp := r.space("ann", "bob")
			r.fullEntry("bob")
			if _, err := r.detail(sp, "ann", "bob", "e1"); err != nil {
				t.Fatalf("setup: %v", err)
			}
			do(r, sp)
			if _, err := r.detail(sp, "ann", "bob", "e1"); !errors.Is(err, ErrNoEntry) {
				t.Errorf("the page is still open: %v", err)
			}
		})
	}
	r := newRig(t, "ann", "bob", "cy")
	sp := r.space("ann", "bob")
	r.fullEntry("bob")
	r.fullEntry("cy")
	cases := []struct {
		name          string
		viewer, owner string
		id            string
		want          error
	}{
		{"an outsider asking", "cy", "bob", "e1", ErrNotMember},
		{"an owner who is not in the space", "ann", "cy", "e1", ErrNoEntry},
		{"a missing entry", "ann", "bob", "nope", ErrNoEntry},
		{"someone else's id under my owner", "ann", "ann", "e1", ErrNoEntry},
	}
	for _, c := range cases {
		if _, err := r.detail(sp, c.viewer, c.owner, c.id); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
	r.exec(`UPDATE membership SET consent_version = 1 WHERE space_id = ? AND user_id = 'uann'`, sp)
	if _, err := r.detail(sp, "ann", "bob", "e1"); !errors.Is(err, ErrNeedConsent) {
		t.Errorf("viewer on older wording: %v", err)
	}
	r.exec(`UPDATE space SET deleted_at = 5 WHERE id = ?`, sp)
	if _, err := r.detail(sp, "bob", "bob", "e1"); !errors.Is(err, ErrNotMember) {
		t.Errorf("deleted space: %v", err)
	}
}

func TestCatalogLabelsComeFromTheEntrysOwnerOnly(t *testing.T) {
	r := newRig(t, "ann", "bob")
	sp := r.space("ann", "bob")
	r.fullEntry("bob")
	// Ann has an act with the same id and a different label: it must never be used for bob's entry.
	r.exec(`INSERT INTO cache_row(owner_id, tbl, rid, pos, data) VALUES ('uann','acts','a1',0,'{"id":"a1","label":"ANN-OWN-LABEL"}')`)
	d, _ := r.detail(sp, "ann", "bob", "e1")
	if strings.Contains(d.text(), "ANN-OWN-LABEL") || !strings.Contains(d.text(), "Kissing") {
		t.Errorf("labels crossed owners: %s", d.text())
	}
	// And the other way round, so the result does not depend on which owner sorts last.
	r2 := newRig(t, "ann", "bob")
	sp2 := r2.space("ann", "bob")
	r2.fullEntry("ann")
	r2.exec(`INSERT INTO cache_row(owner_id, tbl, rid, pos, data) VALUES ('ubob','acts','a1',0,'{"id":"a1","label":"BOB-OWN-LABEL"}')`)
	d2, _ := r2.detail(sp2, "bob", "ann", "e1")
	if strings.Contains(d2.text(), "BOB-OWN-LABEL") || !strings.Contains(d2.text(), "Kissing") {
		t.Errorf("labels crossed owners (reverse): %s", d2.text())
	}
}

func TestDetailCarriesNoRawRow(t *testing.T) {
	r := newRig(t, "ann", "bob")
	sp := r.space("ann", "bob")
	r.fullEntry("bob")
	r.exec(`UPDATE membership SET include_notes = 0 WHERE space_id = ? AND user_id = 'ubob'`, sp)
	d, _ := r.detail(sp, "ann", "bob", "e1")
	if d.Sets != nil || d.Note != "" {
		t.Errorf("raw data travels on the detail: sets=%v note=%q", d.Sets, d.Note)
	}
	if got := fmt.Sprintf("%+v", d); strings.Contains(got, "SECRET-NOTE") || strings.Contains(got, "Pat Partner") {
		t.Errorf("the struct holds hidden content: %s", got)
	}
}
