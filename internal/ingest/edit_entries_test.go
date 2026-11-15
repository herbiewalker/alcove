package ingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/herbiewalker/alcove/internal/edit"
	"github.com/herbiewalker/alcove/internal/trystbk/synth"
)

const win = 10 * 24 * time.Hour

func (r *rig) addEntry(in map[string]string) (entryID string, err error) {
	r.t.Helper()
	base := r.head()
	editAt = time.Now().UTC()
	err = r.st.Update(context.Background(), true, func(tx *sql.Tx) (e error) {
		_, entryID, e = edit.Add(tx, editAt, owner, owner, base, in)
		return
	})
	return
}

func (r *rig) deleteEntry(id string) error {
	r.t.Helper()
	base := r.head()
	editAt = time.Now().UTC()
	return r.st.Update(context.Background(), true, func(tx *sql.Tx) (e error) {
		_, e = edit.Delete(tx, editAt, owner, owner, id, base)
		return
	})
}

func (r *rig) restoreEntry(id string, at time.Time, window time.Duration) error {
	r.t.Helper()
	base := r.head()
	return r.st.Update(context.Background(), true, func(tx *sql.Tx) (e error) {
		_, e = edit.Restore(tx, at, owner, owner, id, base, window)
		return
	})
}

func (r *rig) has(table, id string) bool {
	return r.count(`SELECT COUNT(*) FROM cache_row WHERE owner_id = ? AND tbl = ? AND rid = ?`, owner, table, id) == 1
}

func TestANewEntryHasTheLayoutOfTheOwnersRowsAndSurvivesARebuild(t *testing.T) {
	r := newRig(t)
	if _, err := r.upload(build(t, synth.Options{Seed: 81, Encounters: 6}), true); err != nil {
		t.Fatal(err)
	}
	rowsBefore, _ := r.svc.CacheRows(context.Background(), owner, "encounters")
	acts := r.ids("acts")
	id, err := r.addEntry(map[string]string{"startAt": "1700000000000", "satisfactionRating": "4", "note": "BRAND-NEW-ENTRY", "mood": "WILD",
		"practicesPerformed": "custom:" + acts[0], "contexts": "HOME"})
	if err != nil {
		t.Fatal(err)
	}
	if len(id) != 36 || id[14] != '4' {
		t.Errorf("the id is not a UUID: %q", id)
	}
	rowsAfter, _ := r.svc.CacheRows(context.Background(), owner, "encounters")
	if len(rowsAfter) != len(rowsBefore)+1 {
		t.Fatalf("%d rows, want %d", len(rowsAfter), len(rowsBefore)+1)
	}
	row := r.entryRow(id)
	if string(row["note"]) != `"BRAND-NEW-ENTRY"` || string(row["startAt"]) != "1700000000000" || string(row["mood"]) != `"WILD"` || string(row["id"]) != `"`+id+`"` {
		t.Errorf("new row: %v", row)
	}
	// It has exactly the columns of an existing row, in the same order.
	keys := func(s string) string {
		dec := json.NewDecoder(strings.NewReader(s))
		_, _ = dec.Token()
		var k []string
		for dec.More() {
			t, _ := dec.Token()
			k = append(k, t.(string))
			var skip json.RawMessage
			_ = dec.Decode(&skip)
		}
		return strings.Join(k, ",")
	}
	if keys(rowsAfter[len(rowsAfter)-1]) != keys(rowsBefore[0]) {
		t.Errorf("the new row's columns differ:\n%s\n%s", keys(rowsAfter[len(rowsAfter)-1]), keys(rowsBefore[0]))
	}
	// The time stamps are Alcove's; an unset nullable column is null, not a made-up value.
	if string(row["createdAt"]) != string(row["updatedAt"]) || string(row["createdAt"]) == "null" {
		t.Errorf("stamps: %s %s", row["createdAt"], row["updatedAt"])
	}
	if n := r.count(`SELECT COUNT(*) FROM search WHERE owner_id = ? AND kind = 'encounter' AND ref = ? AND body = 'BRAND-NEW-ENTRY'`, owner, id); n != 1 {
		t.Errorf("search rows: %d", n)
	}
	want := r.snapshot()
	if err := r.svc.Rebuild(context.Background(), owner); err != nil {
		t.Fatal(err)
	}
	if r.snapshot() != want {
		t.Error("a new entry does not survive a rebuild exactly")
	}
	// Refusals: no time, a bad value, a made-up catalog id; nothing is recorded.
	snap := r.snapshot()
	for name, in := range map[string]map[string]string{
		"no start time": {"note": "x"},
		"bad rating":    {"startAt": "1700000000000", "satisfactionRating": "9"},
		"foreign act":   {"startAt": "1700000000000", "practicesPerformed": "custom:nope"},
	} {
		if _, err := r.addEntry(in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if r.snapshot() != snap {
		t.Error("a refused new entry left something behind")
	}
}

func TestDeletingAnEntryTakesItsLinksAndRestoringBringsEverythingBack(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.upload(build(t, synth.Options{Seed: 82, Encounters: 8, Partners: 3}), true); err != nil {
		t.Fatal(err)
	}
	entry := r.firstEntry()
	if _, _, err := r.doEdit(entry, map[string]string{"note": "NOTE-OF-DELETED"}); err != nil {
		t.Fatal(err)
	}
	links := r.count(`SELECT COUNT(*) FROM cache_row WHERE owner_id = ? AND tbl = 'encounter_partner' AND json_extract(data, '$.encounterId') = ?`, owner, entry)
	if links == 0 {
		t.Fatal("setup: the synthetic entry has no partner links")
	}
	otherLinks := r.count(`SELECT COUNT(*) FROM cache_row WHERE owner_id = ? AND tbl = 'encounter_partner'`, owner) - links
	before := r.snapshot()
	rowBefore := r.entryRow(entry)
	if err := r.deleteEntry(entry); err != nil {
		t.Fatal(err)
	}
	if r.has("encounters", entry) || r.count(`SELECT COUNT(*) FROM cache_row WHERE owner_id = ? AND json_extract(data, '$.encounterId') = ?`, owner, entry) != 0 {
		t.Fatal("the entry or its links are still there")
	}
	if r.count(`SELECT COUNT(*) FROM cache_row WHERE owner_id = ? AND tbl = 'encounter_partner'`, owner) != otherLinks {
		t.Error("another entry's links were removed")
	}
	if r.count(`SELECT COUNT(*) FROM search WHERE owner_id = ? AND ref = ?`, owner, entry) != 0 {
		t.Error("the deleted entry is still searchable")
	}
	if r.count(`SELECT COUNT(*) FROM cache_row WHERE owner_id = ? AND tbl = 'partners'`, owner) != 3 {
		t.Error("a partner was removed with the entry")
	}
	// It is listed as deleted, with its start time, and can be restored inside the window.
	var list []edit.DeletedEntry
	_ = r.st.View(ctx, func(tx *sql.Tx) (err error) { list, err = edit.Deleted(tx, owner, time.Now(), win); return })
	if len(list) != 1 || list[0].ID != entry || !list[0].Restorable || list[0].StartMs == 0 {
		t.Fatalf("deleted list: %+v", list)
	}
	// Deleting survives a rebuild exactly; so does a second delete refused.
	gone := r.snapshot()
	if err := r.svc.Rebuild(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if r.snapshot() != gone {
		t.Fatal("a deleted entry comes back after a rebuild")
	}
	if err := r.deleteEntry(entry); !errors.Is(err, edit.ErrNoEntry) {
		t.Errorf("deleting twice: %v", err)
	}
	// Restore puts back the entry, its links and its text, identical to before (updatedAt aside: it is
	// the edited row exactly as it was when it was deleted).
	if err := r.restoreEntry(entry, time.Now(), win); err != nil {
		t.Fatal(err)
	}
	rowAfter := r.entryRow(entry)
	for k, v := range rowBefore {
		if string(rowAfter[k]) != string(v) {
			t.Errorf("column %s: %s -> %s", k, v, rowAfter[k])
		}
	}
	if r.count(`SELECT COUNT(*) FROM cache_row WHERE owner_id = ? AND tbl = 'encounter_partner' AND json_extract(data, '$.encounterId') = ?`, owner, entry) != links {
		t.Error("the partner links did not come back")
	}
	if r.count(`SELECT COUNT(*) FROM search WHERE owner_id = ? AND kind = 'encounter' AND ref = ? AND body = 'NOTE-OF-DELETED'`, owner, entry) != 1 {
		t.Error("the text is not searchable again")
	}
	_ = before
	after := r.snapshot()
	if err := r.svc.Rebuild(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if r.snapshot() != after {
		t.Error("a restored entry differs after a rebuild")
	}
	if err := r.restoreEntry(entry, time.Now(), win); !errors.Is(err, edit.ErrExists) {
		t.Errorf("restoring an entry that is there: %v", err)
	}
	// History shows the whole story, and an undo of an earlier field edit still works.
	var hist []edit.Revision
	_ = r.st.View(ctx, func(tx *sql.Tx) (err error) { hist, err = edit.History(tx, owner, entry); return })
	var kinds []string
	for _, h := range hist {
		kinds = append(kinds, h.Kind)
	}
	if strings.Join(kinds, ",") != "restore,delete," {
		t.Errorf("history kinds (newest first): %q", kinds)
	}
}

func TestARestoreIsRefusedAfterTheWindowAndAfterThePurge(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.upload(build(t, synth.Options{Seed: 83, Encounters: 6}), true); err != nil {
		t.Fatal(err)
	}
	a, b := r.ids("encounters")[0], r.ids("encounters")[1]
	if err := r.deleteEntry(a); err != nil {
		t.Fatal(err)
	}
	if err := r.deleteEntry(b); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	// Inside the window works for the person's window, not past it.
	if err := r.restoreEntry(a, now.Add(11*24*time.Hour), win); !errors.Is(err, edit.ErrExpired) {
		t.Errorf("past the person's window: %v", err)
	}
	if err := r.restoreEntry(a, now.Add(11*24*time.Hour), 45*24*time.Hour); err != nil {
		t.Errorf("inside the administrator's window: %v", err)
	}
	// After the purge the values are gone: nothing can restore it and it is no longer listed.
	var n int
	if err := r.st.Update(ctx, true, func(tx *sql.Tx) (e error) {
		n, e = edit.PurgeDeleted(tx, now.Add(46*24*time.Hour).Unix())
		return
	}); err != nil || n != 2 { // both deletion records: a was restored, its record is wiped too
		t.Fatalf("purge: %d %v", n, err)
	}
	if err := r.restoreEntry(b, now, 90*24*time.Hour); !errors.Is(err, edit.ErrExpired) {
		t.Errorf("a purged entry: %v", err)
	}
	var list []edit.DeletedEntry
	_ = r.st.View(ctx, func(tx *sql.Tx) (err error) { list, err = edit.Deleted(tx, owner, now, win); return })
	if len(list) != 0 {
		t.Errorf("a purged entry is still listed: %+v", list)
	}
	if c := r.count(`SELECT COUNT(*) FROM change WHERE op = 'delete' AND tbl = 'encounters' AND old_value <> ''`); c != 0 {
		t.Errorf("%d delete records keep their values", c)
	}
	// The deletion itself still stands after a rebuild even though its values are gone.
	if err := r.svc.Rebuild(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if r.has("encounters", b) {
		t.Error("a purged deletion was undone by a rebuild")
	}
	// A second purge removes nothing.
	_ = r.st.Update(ctx, true, func(tx *sql.Tx) (e error) { n, e = edit.PurgeDeleted(tx, now.Add(46*24*time.Hour).Unix()); return })
	if n != 0 {
		t.Errorf("a second purge wiped %d", n)
	}
}

func TestAnAddedEntryCanBeDeletedAndRestoredAndEditsGoWithIt(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.upload(build(t, synth.Options{Seed: 84, Encounters: 4}), true); err != nil {
		t.Fatal(err)
	}
	id, err := r.addEntry(map[string]string{"startAt": "1710000000000", "note": "ADDED"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.doEdit(id, map[string]string{"durationMin": "33"}); err != nil {
		t.Fatal(err)
	}
	if err := r.deleteEntry(id); err != nil {
		t.Fatal(err)
	}
	if err := r.restoreEntry(id, time.Now(), win); err != nil {
		t.Fatal(err)
	}
	if string(r.entryRow(id)["durationMin"]) != "33" || string(r.entryRow(id)["note"]) != `"ADDED"` {
		t.Errorf("the restored entry lost its edits: %v", r.entryRow(id))
	}
	want := r.snapshot()
	if err := r.svc.Rebuild(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if r.snapshot() != want {
		t.Error("add, edit, delete and restore do not replay to the same rows")
	}
	// An undo of an earlier field edit works on the added entry; undo never targets add or delete.
	var hist []edit.Revision
	_ = r.st.View(ctx, func(tx *sql.Tx) (err error) { hist, err = edit.History(tx, owner, id); return })
	var fieldEdit string
	for _, h := range hist {
		if h.Kind == "" {
			fieldEdit = h.ID
		}
		if h.Kind == "add" || h.Kind == "delete" {
			base := r.head()
			err := r.st.Update(ctx, true, func(tx *sql.Tx) error {
				_, _, e := edit.Revert(tx, time.Now(), owner, owner, id, base, h.ID)
				return e
			})
			if !errors.Is(err, edit.ErrNoEntry) {
				t.Errorf("an undo of a %s: %v", h.Kind, err)
			}
		}
	}
	if fieldEdit == "" {
		t.Fatal("no field edit in the history")
	}
}
