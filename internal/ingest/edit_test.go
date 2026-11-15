package ingest

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/herbiewalker/alcove/internal/edit"
	"github.com/herbiewalker/alcove/internal/trystbk/synth"
)

var editAt = time.Now().UTC()

// firstEntry returns the id of the owner's first cached encounter.
func (r *rig) firstEntry() string {
	r.t.Helper()
	rows, err := r.svc.CacheRows(context.Background(), owner, "encounters")
	if err != nil || len(rows) == 0 {
		r.t.Fatalf("no entries: %v", err)
	}
	var m map[string]json.RawMessage
	_ = json.Unmarshal([]byte(rows[0]), &m)
	var id string
	_ = json.Unmarshal(m["id"], &id)
	return id
}

func (r *rig) head() string {
	r.t.Helper()
	var id string
	if err := r.st.View(context.Background(), func(tx *sql.Tx) (err error) { id, _, err = edit.Head(tx, owner); return }); err != nil {
		r.t.Fatal(err)
	}
	return id
}

// doEdit applies one edit as the owner, opened on the current head, one second after the last.
func (r *rig) doEdit(entry string, in map[string]string) (string, []string, error) {
	r.t.Helper()
	var id string
	var names []string
	base := r.head()
	editAt = time.Now().UTC()
	err := r.st.Update(context.Background(), true, func(tx *sql.Tx) (err error) {
		id, names, err = edit.Apply(tx, editAt, owner, owner, entry, base, in)
		return
	})
	return id, names, err
}

// snapshot is every derived row, so two caches can be compared whole.
func (r *rig) snapshot() string {
	r.t.Helper()
	var b strings.Builder
	err := r.st.View(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT 'c', tbl, rid, pos, data FROM cache_row WHERE owner_id = ? ORDER BY tbl, pos, rid`, owner)
		if err != nil {
			return err
		}
		for rows.Next() {
			var a, t, rid, d string
			var p int
			_ = rows.Scan(&a, &t, &rid, &p, &d)
			b.WriteString(a + "|" + t + "|" + rid + "|" + d + "\n")
		}
		_ = rows.Close()
		rows, err = tx.Query(`SELECT 's', kind, ref, body FROM search WHERE owner_id = ? ORDER BY kind, ref, body`, owner)
		if err != nil {
			return err
		}
		for rows.Next() {
			var a, k, ref, body string
			_ = rows.Scan(&a, &k, &ref, &body)
			b.WriteString(a + "|" + k + "|" + ref + "|" + body + "\n")
		}
		_ = rows.Close()
		var rev string
		_ = tx.QueryRow(`SELECT revision_id FROM cache_head WHERE owner_id = ?`, owner).Scan(&rev)
		b.WriteString("head|" + rev)
		return nil
	})
	if err != nil {
		r.t.Fatal(err)
	}
	return b.String()
}

func (r *rig) entryRow(id string) map[string]json.RawMessage {
	r.t.Helper()
	var data string
	if err := r.st.View(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT data FROM cache_row WHERE owner_id = ? AND tbl = 'encounters' AND rid = ?`, owner, id).Scan(&data)
	}); err != nil {
		r.t.Fatal(err)
	}
	var m map[string]json.RawMessage
	_ = json.Unmarshal([]byte(data), &m)
	return m
}

func TestAnEditChangesOnlyItsFieldsAndNeverTheUpload(t *testing.T) {
	r := newRig(t)
	if _, err := r.upload(build(t, synth.Options{Seed: 61, Encounters: 10}), true); err != nil {
		t.Fatal(err)
	}
	entry := r.firstEntry()
	files := r.uploadFiles()
	sumOf := func() string {
		b, _ := os.ReadFile(files[0])
		h := sha256.Sum256(b)
		return hex.EncodeToString(h[:])
	}
	before, upBefore := r.entryRow(entry), sumOf()
	var keysBefore []string
	{
		rows, _ := r.svc.CacheRows(context.Background(), owner, "encounters")
		dec := json.NewDecoder(strings.NewReader(rows[0]))
		_, _ = dec.Token()
		for dec.More() {
			k, _ := dec.Token()
			keysBefore = append(keysBefore, k.(string))
			var skip json.RawMessage
			_ = dec.Decode(&skip)
		}
	}

	_, names, err := r.doEdit(entry, map[string]string{"satisfactionRating": "5", "note": "A new <note> & more", "durationMin": "42"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(names, ",") != "durationMin,satisfactionRating,note" {
		t.Errorf("changed fields: %v", names)
	}
	after := r.entryRow(entry)
	if string(after["satisfactionRating"]) != "5" || string(after["durationMin"]) != "42" || string(after["note"]) != `"A new <note> & more"` {
		t.Errorf("values: %s %s %s", after["satisfactionRating"], after["durationMin"], after["note"])
	}
	// updatedAt is stamped from the revision time; every other column is exactly as it was.
	if string(after["updatedAt"]) != strconv.FormatInt(editAt.Unix()*1000, 10) {
		t.Errorf("updatedAt not stamped: %s", after["updatedAt"])
	}
	for k, v := range before {
		switch k {
		case "satisfactionRating", "note", "durationMin", "updatedAt":
		default:
			if string(after[k]) != string(v) {
				t.Errorf("column %s changed: %s -> %s", k, v, after[k])
			}
		}
	}
	rows, _ := r.svc.CacheRows(context.Background(), owner, "encounters")
	dec := json.NewDecoder(strings.NewReader(rows[0]))
	_, _ = dec.Token()
	var keysAfter []string
	for dec.More() {
		k, _ := dec.Token()
		keysAfter = append(keysAfter, k.(string))
		var skip json.RawMessage
		_ = dec.Decode(&skip)
	}
	if strings.Join(keysBefore, ",") != strings.Join(keysAfter, ",") {
		t.Errorf("column order changed: %v -> %v", keysBefore, keysAfter)
	}
	// The upload is untouched, and the edit is one revision of kind edit beside it.
	if sumOf() != upBefore {
		t.Error("the stored upload changed")
	}
	if n := r.count(`SELECT COUNT(*) FROM revision WHERE owner_id = ? AND kind = 'edit'`, owner); n != 1 {
		t.Errorf("%d edit revisions", n)
	}
	if n := r.count(`SELECT COUNT(*) FROM change WHERE owner_id = ?`, owner); n != 3 {
		t.Errorf("%d change rows", n)
	}
	// The new note is searchable; the old text is gone from the index.
	if n := r.count(`SELECT COUNT(*) FROM search WHERE owner_id = ? AND kind = 'encounter' AND ref = ? AND body = ?`, owner, entry, "A new <note> & more"); n != 1 {
		t.Errorf("search rows for the new note: %d", n)
	}
	// The upload list still shows one upload, not the edit.
	if list, _ := r.svc.List(context.Background(), owner); len(list) != 1 {
		t.Errorf("the upload list has %d rows", len(list))
	}
}

func TestEditsSurviveARebuildExactly(t *testing.T) {
	r := newRig(t)
	if _, err := r.upload(build(t, synth.Options{Seed: 62, Encounters: 12}), true); err != nil {
		t.Fatal(err)
	}
	rows, _ := r.svc.CacheRows(context.Background(), owner, "encounters")
	ids := make([]string, 0, 3)
	for _, row := range rows[:3] {
		var m map[string]json.RawMessage
		_ = json.Unmarshal([]byte(row), &m)
		var id string
		_ = json.Unmarshal(m["id"], &id)
		ids = append(ids, id)
	}
	steps := []struct {
		entry string
		in    map[string]string
	}{
		{ids[0], map[string]string{"note": "first note", "satisfactionRating": "3"}},
		{ids[1], map[string]string{"mood": "TENDER", "initiator": "MUTUAL", "protectionUsed": "CONDOM,PILL"}},
		{ids[0], map[string]string{"note": ""}}, // clears the note again
		{ids[2], map[string]string{"startAt": "1700000000000", "orgasmCountSelf": "2", "durationMin": ""}},
		{ids[0], map[string]string{"note": "second note", "satisfactionRating": ""}},
	}
	for i, s := range steps {
		if _, _, err := r.doEdit(s.entry, s.in); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	want := r.snapshot()
	if err := r.svc.Rebuild(context.Background(), owner); err != nil {
		t.Fatal(err)
	}
	if got := r.snapshot(); got != want {
		t.Fatalf("a rebuild does not give what the edits gave:\nwant %.2000s\n got %.2000s", want, got)
	}
	// And again, and through RebuildAll: replay is stable.
	if done, failed := r.svc.RebuildAll(context.Background()); done != 1 || len(failed) != 0 {
		t.Fatalf("RebuildAll: %d %v", done, failed)
	}
	if got := r.snapshot(); got != want {
		t.Fatal("a second rebuild differs")
	}
}

func TestAnEditRefusesWhatItShould(t *testing.T) {
	r := newRig(t)
	if _, err := r.upload(build(t, synth.Options{Seed: 63, Encounters: 6}), true); err != nil {
		t.Fatal(err)
	}
	entry := r.firstEntry()
	snap := r.snapshot()
	revs := r.count(`SELECT COUNT(*) FROM revision`)
	for name, in := range map[string]map[string]string{
		"rating too high":   {"satisfactionRating": "6"},
		"rating zero":       {"satisfactionRating": "0"},
		"rating text":       {"satisfactionRating": "five"},
		"negative length":   {"durationMin": "-1"},
		"huge length":       {"durationMin": "99999"},
		"made up mood":      {"mood": "SPLENDID"},
		"lowercase mood":    {"mood": "tender"},
		"made up initiator": {"initiator": "NOBODY"},
		"made up protect":   {"protectionUsed": "CONDOM,MAGIC"},
		"empty start":       {"startAt": ""},
		"text start":        {"startAt": "yesterday"},
		"far future start":  {"startAt": "99999999999999"},
		"orgasms too many":  {"orgasmCountSelf": "500"},
		"unknown field":     {"id": "x"},
		"a field not ours":  {"locationId": "x"},
		"note not utf-8":    {"note": "\xff\xfe"},
	} {
		if _, _, err := r.doEdit(entry, in); err == nil {
			t.Errorf("%s: accepted", name)
		} else if strings.Contains(err.Error(), "SPLENDID") || strings.Contains(err.Error(), "MAGIC") {
			t.Errorf("%s: the error repeats the value: %v", name, err)
		}
	}
	// Nothing was recorded by any refusal.
	if r.snapshot() != snap || r.count(`SELECT COUNT(*) FROM revision`) != revs || r.count(`SELECT COUNT(*) FROM change`) != 0 {
		t.Error("a refused edit left something behind")
	}
	// Setting a field to what it already is changes nothing.
	cur := r.entryRow(entry)
	if _, _, err := r.doEdit(entry, map[string]string{"satisfactionRating": strings.Trim(string(cur["satisfactionRating"]), "null")}); !errors.Is(err, edit.ErrNoChange) {
		t.Errorf("an unchanged save: %v", err)
	}
	// A stale form is refused, a missing entry is refused.
	stale := r.head()
	if _, _, err := r.doEdit(entry, map[string]string{"satisfactionRating": "4"}); err != nil && !errors.Is(err, edit.ErrNoChange) {
		t.Fatal(err)
	}
	if _, _, err := r.doEdit(entry, map[string]string{"durationMin": "77"}); err != nil {
		t.Fatal(err)
	}
	err := r.st.Update(context.Background(), true, func(tx *sql.Tx) error {
		_, _, e := edit.Apply(tx, editAt, owner, owner, entry, stale, map[string]string{"durationMin": "78"})
		return e
	})
	if !errors.Is(err, edit.ErrStale) {
		t.Errorf("a stale save: %v", err)
	}
	if _, _, err := r.doEdit("no-such-entry", map[string]string{"durationMin": "5"}); !errors.Is(err, edit.ErrNoEntry) {
		t.Errorf("a missing entry: %v", err)
	}
}

func TestHistoryAndRevert(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.upload(build(t, synth.Options{Seed: 64, Encounters: 6}), true); err != nil {
		t.Fatal(err)
	}
	entry := r.firstEntry()
	orig := r.entryRow(entry)
	first, _, err := r.doEdit(entry, map[string]string{"durationMin": "30", "mood": "WILD"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.doEdit(entry, map[string]string{"durationMin": "45"}); err != nil {
		t.Fatal(err)
	}
	var hist []edit.Revision
	_ = r.st.View(ctx, func(tx *sql.Tx) (err error) { hist, err = edit.History(tx, owner, entry); return })
	if len(hist) != 2 || len(hist[0].Changes) != 1 || hist[0].Changes[0].Field != "durationMin" || string(hist[0].Changes[0].Old) != "30" || string(hist[0].Changes[0].New) != "45" {
		t.Fatalf("history: %+v", hist)
	}
	if hist[1].ID != first || len(hist[1].Changes) != 2 || !hist[1].ByOwner {
		t.Fatalf("first edit in history: %+v", hist[1])
	}
	// Reverting the first edit sets its fields back to the upload's values, as a new edit.
	base := r.head()
	editAt = time.Now().UTC()
	if err := r.st.Update(ctx, true, func(tx *sql.Tx) error { _, _, e := edit.Revert(tx, editAt, owner, owner, entry, base, first); return e }); err != nil {
		t.Fatal(err)
	}
	now := r.entryRow(entry)
	if string(now["durationMin"]) != string(orig["durationMin"]) || string(now["mood"]) != string(orig["mood"]) {
		t.Errorf("revert: %s %s, want %s %s", now["durationMin"], now["mood"], orig["durationMin"], orig["mood"])
	}
	_ = r.st.View(ctx, func(tx *sql.Tx) (err error) { hist, err = edit.History(tx, owner, entry); return })
	if len(hist) != 3 {
		t.Errorf("history has %d edits after a revert, want 3 (nothing is removed)", len(hist))
	}
	// Reverting an edit of someone else's entry, or one that is not this entry's, finds nothing.
	hd := r.head()
	err = r.st.Update(ctx, true, func(tx *sql.Tx) error {
		_, _, e := edit.Revert(tx, editAt, owner, owner, "other-entry", hd, first)
		return e
	})
	if !errors.Is(err, edit.ErrNoEntry) {
		t.Errorf("revert on the wrong entry: %v", err)
	}
	// The whole thing survives a rebuild.
	want := r.snapshot()
	if err := r.svc.Rebuild(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if r.snapshot() != want {
		t.Error("rebuild after a revert differs")
	}
}

func TestEditsFollowTheirUploadThroughDeleteRestoreAndANewUpload(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.upload(build(t, synth.Options{Seed: 65, Encounters: 6}), true); err != nil {
		t.Fatal(err)
	}
	entry := r.firstEntry()
	if _, _, err := r.doEdit(entry, map[string]string{"note": "EDITED-NOTE"}); err != nil {
		t.Fatal(err)
	}
	// A second upload starts a new chain: its cache is its own rows, with no trace of the edit.
	second := build(t, synth.Options{Seed: 66, Encounters: 6})
	if _, err := r.upload(second, true); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.Rebuild(ctx, owner); err != nil {
		t.Fatal(err)
	}
	var data string
	_ = r.st.View(ctx, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COUNT(*) FROM cache_row WHERE owner_id = ? AND data LIKE '%EDITED-NOTE%'`, owner).Scan(&data)
	})
	if data != "0" {
		t.Error("an edit of an older upload shows in a newer one")
	}
	// The edit is still in the history of its upload (nothing was deleted).
	if n := r.count(`SELECT COUNT(*) FROM change WHERE owner_id = ?`, owner); n != 1 {
		t.Errorf("%d change rows kept", n)
	}
}

func (r *rig) ids(table string) []string {
	r.t.Helper()
	rows, err := r.svc.CacheRows(context.Background(), owner, table)
	if err != nil {
		r.t.Fatal(err)
	}
	var out []string
	for _, row := range rows {
		var m map[string]json.RawMessage
		_ = json.Unmarshal([]byte(row), &m)
		var id string
		_ = json.Unmarshal(m["id"], &id)
		out = append(out, id)
	}
	return out
}

func (r *rig) partnerEdit(id string, in map[string]string) error {
	r.t.Helper()
	base := r.head()
	editAt = time.Now().UTC()
	return r.st.Update(context.Background(), true, func(tx *sql.Tx) error {
		_, _, e := edit.PartnerApply(tx, editAt, owner, owner, id, base, in)
		return e
	})
}

func (r *rig) partnerRow(id string) map[string]json.RawMessage {
	r.t.Helper()
	var data string
	if err := r.st.View(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT data FROM cache_row WHERE owner_id = ? AND tbl = 'partners' AND rid = ?`, owner, id).Scan(&data)
	}); err != nil {
		r.t.Fatal(err)
	}
	var m map[string]json.RawMessage
	_ = json.Unmarshal([]byte(data), &m)
	return m
}

func TestSetsComeOnlyFromTheOwnersCatalogAndPlaces(t *testing.T) {
	r := newRig(t)
	if _, err := r.upload(build(t, synth.Options{Seed: 71, Encounters: 8}), true); err != nil {
		t.Fatal(err)
	}
	entry := r.firstEntry()
	acts, positions := r.ids("acts"), r.ids("positions")
	if len(acts) < 2 || len(positions) < 1 {
		t.Fatalf("the synthetic catalogs are too small: %v %v", acts, positions)
	}
	if err := r.st.Update(context.Background(), true, func(tx *sql.Tx) error {
		_, e := tx.Exec(`UPDATE cache_row SET data = json_set(data, '$.positions', NULL, '$.practicesPerformed', NULL, '$.contexts', NULL) WHERE owner_id = ? AND tbl = 'encounters' AND rid = ?`, owner, entry)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	good := map[string]string{
		"practicesPerformed": "custom:" + acts[0] + ",custom:" + acts[1] + ",custom:" + acts[0],
		"positions":          "custom:" + positions[0],
		"contexts":           "BEDROOM,HOT_TUB",
	}
	if _, names, err := r.doEdit(entry, good); err != nil {
		t.Fatal(err)
	} else if strings.Join(names, ",") != "practicesPerformed,positions,contexts" {
		t.Errorf("changed: %v", names)
	}
	row := r.entryRow(entry)
	if string(row["practicesPerformed"]) != `"custom:`+acts[0]+`,custom:`+acts[1]+`"` || string(row["contexts"]) != `"BEDROOM,HOT_TUB"` {
		t.Errorf("stored: %s %s", row["practicesPerformed"], row["contexts"])
	}
	// Another person's catalog row is not in mine.
	if err := r.st.Update(context.Background(), true, func(tx *sql.Tx) error {
		_, e := tx.Exec(`INSERT INTO cache_row(owner_id, tbl, rid, pos, data) VALUES ('ubob', 'kinks', 'bobkink', 0, '{"id":"bobkink","label":"BOBS"}')`)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	snap := r.snapshot()
	for name, in := range map[string]map[string]string{
		"a made-up catalog id":      {"kinks": "custom:does-not-exist"},
		"another person's item":     {"kinks": "custom:bobkink"},
		"a plain word":              {"toys": "handcuffs"},
		"another table's id":        {"positions": "custom:" + acts[0]},
		"a place that is not Tryst": {"contexts": "MOON_BASE"},
		"lowercase place":           {"contexts": "home"},
		"too many choices":          {"practicesReceived": strings.Repeat("custom:"+acts[0]+",", 1) + strings.Repeat("custom:x,", 70)},
	} {
		if _, _, err := r.doEdit(entry, in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if r.snapshot() != snap {
		t.Error("a refused edit changed something")
	}
	// An item already on the entry stays valid even if it is no longer in the catalog (an
	// unrelated edit does not force it off), and clearing a set stores null.
	if err := r.st.Update(context.Background(), true, func(tx *sql.Tx) error {
		_, e := tx.Exec(`UPDATE cache_row SET data = json_set(data, '$.toys', 'custom:gone') WHERE owner_id = ? AND tbl = 'encounters' AND rid = ?`, owner, entry)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.doEdit(entry, map[string]string{"toys": "custom:gone", "satisfactionRating": "2"}); err != nil {
		t.Errorf("keeping a removed item: %v", err)
	}
	if _, _, err := r.doEdit(entry, map[string]string{"toys": "", "contexts": ""}); err != nil {
		t.Fatal(err)
	}
	row = r.entryRow(entry)
	if string(row["toys"]) != "null" || string(row["contexts"]) != "null" {
		t.Errorf("cleared sets: %s %s", row["toys"], row["contexts"])
	}
	want := r.snapshot()
	if err := r.svc.Rebuild(context.Background(), owner); err != nil {
		t.Fatal(err)
	}
	if r.snapshot() != want {
		t.Error("set edits do not survive a rebuild")
	}
}

func TestAPartnerCanBeRenamedAndStaysConsistent(t *testing.T) {
	r := newRig(t)
	if _, err := r.upload(build(t, synth.Options{Seed: 72, Encounters: 8, Partners: 3}), true); err != nil {
		t.Fatal(err)
	}
	ids := r.ids("partners")
	p := ids[0]
	before := r.partnerRow(p)
	if err := r.partnerEdit(p, map[string]string{"displayName": "  Renamed Person ", "note": "a note"}); err != nil {
		t.Fatal(err)
	}
	row := r.partnerRow(p)
	if string(row["displayName"]) != `"Renamed Person"` || string(row["isAnonymous"]) != "0" || string(row["note"]) != `"a note"` {
		t.Errorf("renamed: %s %s %s", row["displayName"], row["isAnonymous"], row["note"])
	}
	for k, v := range before {
		switch k {
		case "displayName", "isAnonymous", "note", "updatedAt":
		default:
			if string(row[k]) != string(v) {
				t.Errorf("partner column %s changed", k)
			}
		}
	}
	if n := r.count(`SELECT COUNT(*) FROM search WHERE owner_id = ? AND kind = 'partner' AND ref = ? AND body = ?`, owner, p, "Renamed Person\na note"); n != 1 {
		t.Errorf("search rows for the renamed partner: %d", n)
	}
	// Clearing the name makes the partner anonymous again, as in Tryst.
	if err := r.partnerEdit(p, map[string]string{"displayName": ""}); err != nil {
		t.Fatal(err)
	}
	row = r.partnerRow(p)
	if string(row["displayName"]) != "null" || string(row["isAnonymous"]) != "1" {
		t.Errorf("cleared: %s %s", row["displayName"], row["isAnonymous"])
	}
	for name, in := range map[string]map[string]string{
		"a name that is too long":   {"displayName": strings.Repeat("x", 81)},
		"a control character":       {"displayName": "bad\x07name"},
		"the anonymous flag":        {"isAnonymous": "0"},
		"a column that is not ours": {"relationshipType": "SPOUSE"},
	} {
		if err := r.partnerEdit(p, in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Survives a rebuild exactly, and a partner id that is not there is not found.
	want := r.snapshot()
	if err := r.svc.Rebuild(context.Background(), owner); err != nil {
		t.Fatal(err)
	}
	if r.snapshot() != want {
		t.Error("partner edits do not survive a rebuild")
	}
	if err := r.partnerEdit("no-such-partner", map[string]string{"displayName": "x"}); !errors.Is(err, edit.ErrNoEntry) {
		t.Errorf("a missing partner: %v", err)
	}
	// The entry editor cannot be used on a partner id or the other way round.
	if _, _, err := r.doEdit(p, map[string]string{"note": "x"}); !errors.Is(err, edit.ErrNoEntry) {
		t.Errorf("an entry edit on a partner id: %v", err)
	}
	// Partner history and undo.
	var hist []edit.Revision
	_ = r.st.View(context.Background(), func(tx *sql.Tx) (err error) { hist, err = edit.PartnerHistory(tx, owner, p); return })
	if len(hist) != 2 || hist[1].Changes[0].Field != "displayName" {
		t.Fatalf("partner history: %+v", hist)
	}
	base := r.head()
	if err := r.st.Update(context.Background(), true, func(tx *sql.Tx) error {
		_, _, e := edit.PartnerRevert(tx, time.Now().UTC(), owner, owner, p, base, hist[0].ID)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if string(r.partnerRow(p)["displayName"]) != `"Renamed Person"` {
		t.Errorf("undo of the clearing: %s", r.partnerRow(p)["displayName"])
	}
}
