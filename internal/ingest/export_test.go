package ingest

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/herbiewalker/alcove/internal/edit"
	"github.com/herbiewalker/alcove/internal/trystbk"
	"github.com/herbiewalker/alcove/internal/trystbk/synth"
)

const exportPW = "a-different-export-password"

func (r *rig) tableNames() []string {
	r.t.Helper()
	var order string
	if err := r.st.View(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT table_order FROM cache_head WHERE owner_id = ?`, owner).Scan(&order)
	}); err != nil {
		r.t.Fatal(err)
	}
	var names []string
	_ = json.Unmarshal([]byte(order), &names)
	return names
}

func TestAnExportIsTheCurrentDataAndOpensAsAnUploadThatMatchesTheCache(t *testing.T) {
	r := newRig(t)
	res := build(t, synth.Options{Seed: 90, Encounters: 8, Partners: 2, Photos: 3, IncludeSettings: true})
	if _, err := r.upload(res, true); err != nil {
		t.Fatal(err)
	}
	filesBefore := r.uploadFiles()

	// An edit, an added entry, a deleted entry (with a photo if it has one), and a renamed partner.
	media, _ := r.svc.CacheRows(context.Background(), owner, "media")
	var m0 map[string]json.RawMessage
	_ = json.Unmarshal([]byte(media[0]), &m0)
	var photoEntry, photoID string
	_ = json.Unmarshal(m0["encounterId"], &photoEntry)
	_ = json.Unmarshal(m0["id"], &photoID)
	other := ""
	for _, id := range r.ids("encounters") {
		if id != photoEntry {
			other = id
			break
		}
	}
	if _, _, err := r.doEdit(other, map[string]string{"note": "EXPORTED NOTE"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.addEntry(map[string]string{"startAt": "1700000000000", "note": "EXPORTED NEW ENTRY"}); err != nil {
		t.Fatal(err)
	}
	if err := r.deleteEntry(photoEntry); err != nil {
		t.Fatal(err)
	}
	if err := r.partnerEdit(r.ids("partners")[0], map[string]string{"displayName": "Exported Name", "note": ""}); err != nil {
		t.Fatal(err)
	}

	out, err := r.svc.Export(context.Background(), owner, "", exportPW)
	if err != nil {
		t.Fatal(err)
	}
	if out.Dropped < 1 {
		t.Errorf("the deleted entry's photo stayed in the file (dropped %d)", out.Dropped)
	}
	bk, err := trystbk.Read(bytes.NewReader(out.File), exportPW)
	if err != nil {
		t.Fatalf("the export does not open: %v", err)
	}
	defer bk.Close()
	for _, m := range bk.Media() {
		if m.Name == trystbk.EntryMediaPrefix+photoID {
			t.Error("the deleted entry's photo is in the export")
		}
	}
	if bk.Settings == nil {
		t.Error("settings were not carried")
	}

	// The upload on disk is exactly as it was.
	if !reflect.DeepEqual(r.uploadFiles(), filesBefore) {
		t.Error("the stored upload changed")
	}

	// Feeding the export back in as a fresh upload gives the same rows as the cache, table by table.
	const bob = "ubob"
	if _, err := r.svc.Upload(context.Background(), Params{Owner: bob, CreatedBy: bob, Filename: "x.tryst", Password: exportPW, Remember: true},
		bytes.NewReader(out.File)); err != nil {
		t.Fatal(err)
	}
	for _, name := range r.tableNames() {
		want, _ := r.svc.CacheRows(context.Background(), owner, name)
		got, _ := r.svc.CacheRows(context.Background(), bob, name)
		if len(want) != len(got) {
			t.Fatalf("%s: %d rows, want %d", name, len(got), len(want))
		}
		for i := range want {
			if !sameJSON(want[i], got[i]) {
				t.Errorf("%s row %d differs:\n got %s\nwant %s", name, i, got[i], want[i])
			}
		}
	}
	if !contains(r.cacheText(owner, "encounters"), "EXPORTED NEW ENTRY") || !contains(r.cacheText(owner, "encounters"), "EXPORTED NOTE") {
		t.Error("the cache lacks the edits the test made")
	}
	if contains(r.cacheText(bob, "encounters"), photoEntry) {
		t.Error("the deleted entry came back through the export")
	}
}

func sameJSON(a, b string) bool {
	var x, y any
	if json.Unmarshal([]byte(a), &x) != nil || json.Unmarshal([]byte(b), &y) != nil {
		return a == b
	}
	return reflect.DeepEqual(x, y)
}

func contains(s, sub string) bool { return bytes.Contains([]byte(s), []byte(sub)) }

func (r *rig) cacheText(who, table string) string {
	rows, _ := r.svc.CacheRows(context.Background(), who, table)
	var b bytes.Buffer
	for _, x := range rows {
		b.WriteString(x)
	}
	return b.String()
}

func TestExportNeedsTheUploadPasswordAndAFreshCache(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.svc.Export(ctx, owner, "", exportPW); !errors.Is(err, ErrNoRevision) {
		t.Errorf("nothing uploaded: %v", err)
	}
	res := build(t, synth.Options{Seed: 91, Encounters: 4})
	if _, err := r.upload(res, false); err != nil { // password not remembered
		t.Fatal(err)
	}
	if _, err := r.svc.Export(ctx, owner, "", exportPW); !errors.Is(err, ErrNoPassword) {
		t.Errorf("no remembered password: %v", err)
	}
	if _, err := r.svc.Export(ctx, owner, "wrong", exportPW); !errors.Is(err, ErrWrongPassword) {
		t.Errorf("wrong password: %v", err)
	}
	out, err := r.svc.Export(ctx, owner, res.Password, exportPW)
	if err != nil {
		t.Fatalf("the right password: %v", err)
	}
	if bk, err := trystbk.Read(bytes.NewReader(out.File), exportPW); err != nil {
		t.Errorf("the export does not open: %v", err)
	} else {
		bk.Close()
	}
	r.exec(t, `DELETE FROM cache_head WHERE owner_id = ?`, owner)
	if _, err := r.svc.Export(ctx, owner, res.Password, exportPW); !errors.Is(err, ErrCacheStale) {
		t.Errorf("a missing cache: %v", err)
	}
}

func (r *rig) summary() edit.Summary {
	r.t.Helper()
	var s edit.Summary
	if err := r.st.View(context.Background(), func(tx *sql.Tx) (err error) { s, err = edit.Summarize(tx, owner); return }); err != nil {
		r.t.Fatal(err)
	}
	return s
}

func TestTheSummaryCountsEntriesAndPartnersByTheirNetEffect(t *testing.T) {
	r := newRig(t)
	if _, err := r.upload(build(t, synth.Options{Seed: 92, Encounters: 6, Partners: 2}), true); err != nil {
		t.Fatal(err)
	}
	if s := r.summary(); !reflect.DeepEqual(s, edit.Summary{}) {
		t.Fatalf("an untouched upload: %+v", s)
	}
	ids := r.ids("encounters")
	// An edit (and an edit of the same entry again) counts the entry once.
	if _, _, err := r.doEdit(ids[0], map[string]string{"note": "one"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.doEdit(ids[0], map[string]string{"note": "two"}); err != nil {
		t.Fatal(err)
	}
	// An entry added and then deleted again leaves nothing; one added and kept counts as added.
	gone, err := r.addEntry(map[string]string{"startAt": "1700000000000"})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.deleteEntry(gone); err != nil {
		t.Fatal(err)
	}
	if _, err := r.addEntry(map[string]string{"startAt": "1700000100000"}); err != nil {
		t.Fatal(err)
	}
	// An uploaded entry deleted counts once, however many rows hang on it.
	if err := r.deleteEntry(ids[1]); err != nil {
		t.Fatal(err)
	}
	if err := r.partnerEdit(r.ids("partners")[0], map[string]string{"displayName": "New Name", "note": ""}); err != nil {
		t.Fatal(err)
	}
	s := r.summary()
	if s.Edited != 1 || s.Added != 1 || s.Deleted != 1 || s.PartnersEdited != 1 || s.Edits != 7 {
		t.Errorf("summary: %+v", s)
	}
	if len(s.Fields) == 0 || s.Fields[0] > s.Fields[len(s.Fields)-1] {
		t.Errorf("fields: %v", s.Fields)
	}
	// Restoring the deleted one takes it out of the count.
	if err := r.restoreEntry(ids[1], time.Now().UTC(), win); err != nil {
		t.Fatal(err)
	}
	if s := r.summary(); s.Deleted != 0 {
		t.Errorf("after a restore: %+v", s)
	}
}
