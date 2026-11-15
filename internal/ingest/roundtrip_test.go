package ingest

// The emulator round trip (N8, D-43 slice 3) needs real files: a base backup, a phone-only
// backup and an edited export, all synthetic. These two tests produce them and compare what
// Tryst wrote back. They do nothing unless ALCOVE_ROUNDTRIP_DIR is set, so a normal test run
// never writes anything outside its own temp folder.
//
//	ALCOVE_ROUNDTRIP_DIR=<dir> go test ./internal/ingest -run TestRoundTripEmit -v
//	ALCOVE_ROUNDTRIP_DIR=<dir> ALCOVE_ROUNDTRIP_CHECK=<file>:<password>:<label> go test ./internal/ingest -run TestRoundTripCompare -v

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/herbiewalker/alcove/internal/trystbk"
	"github.com/herbiewalker/alcove/internal/trystbk/synth"
)

const (
	rtBasePW   = "round-trip-base-1"
	rtPhonePW  = "round-trip-phone-1"
	rtExportPW = "round-trip-export-1"
)

type rtManifest struct {
	EditedID, EditedNote string
	AddedID, AddedNote   string
	DeletedID            string
	DeletedPhotoIDs      []string
	RenamedPartnerID     string
	RenamedPartnerName   string
	ExportRows           map[string]int
	ExportPhotos         int
	ExportDroppedPhotos  int
	BaseRows, PhoneRows  map[string]int
	PhoneEntryIDs        []string
	BasePartnerLinkRows  int
	PhonePartnerLinkRows int
}

func rowCounts(bk *trystbk.Backup) map[string]int {
	m := map[string]int{}
	for _, t := range bk.Tables {
		m[t.Name] = len(t.Rows)
	}
	return m
}

func TestRoundTripEmit(t *testing.T) {
	dir := os.Getenv("ALCOVE_ROUNDTRIP_DIR")
	if dir == "" {
		t.Skip("set ALCOVE_ROUNDTRIP_DIR to write the round-trip files")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	r := newRig(t)
	ctx := context.Background()

	base := build(t, synth.Options{Seed: 700, Encounters: 10, Partners: 3, Photos: 4, IncludeSettings: true, Password: rtBasePW})
	phone := build(t, synth.Options{Seed: 701, Encounters: 6, Partners: 2, Photos: 2, IncludeSettings: true, Password: rtPhonePW})
	if _, err := r.upload(base, true); err != nil {
		t.Fatal(err)
	}

	// The edits: a note, a new entry, a deleted entry that owns a photo, a renamed partner.
	mediaRows, _ := r.svc.CacheRows(ctx, owner, "media")
	var m0 map[string]json.RawMessage
	_ = json.Unmarshal([]byte(mediaRows[0]), &m0)
	var delID string
	_ = json.Unmarshal(m0["encounterId"], &delID)
	var delPhotos []string
	for _, mr := range mediaRows {
		var m map[string]json.RawMessage
		_ = json.Unmarshal([]byte(mr), &m)
		var enc, id string
		_ = json.Unmarshal(m["encounterId"], &enc)
		_ = json.Unmarshal(m["id"], &id)
		if enc == delID {
			delPhotos = append(delPhotos, id)
		}
	}
	editID := ""
	for _, id := range r.ids("encounters") {
		if id != delID {
			editID = id
			break
		}
	}
	man := rtManifest{EditedID: editID, EditedNote: "Edited on the server: round trip note", AddedNote: "Added on the server: round trip entry",
		DeletedID: delID, DeletedPhotoIDs: delPhotos, RenamedPartnerID: r.ids("partners")[0], RenamedPartnerName: "Renamed On Server"}
	if _, _, err := r.doEdit(editID, map[string]string{"note": man.EditedNote, "satisfactionRating": "5"}); err != nil {
		t.Fatal(err)
	}
	var err error
	if man.AddedID, err = r.addEntry(map[string]string{"startAt": "1760000000000", "note": man.AddedNote, "satisfactionRating": "4", "mood": "TENDER"}); err != nil {
		t.Fatal(err)
	}
	if err := r.deleteEntry(delID); err != nil {
		t.Fatal(err)
	}
	if err := r.partnerEdit(man.RenamedPartnerID, map[string]string{"displayName": man.RenamedPartnerName, "note": ""}); err != nil {
		t.Fatal(err)
	}
	out, err := r.svc.Export(ctx, owner, "", rtExportPW)
	if err != nil {
		t.Fatal(err)
	}
	man.ExportRows, man.ExportPhotos, man.ExportDroppedPhotos = out.Rows, out.Media, out.Dropped

	for name, file := range map[string][]byte{"base.tryst": base.File, "phone.tryst": phone.File, "export.tryst": out.File} {
		if err := os.WriteFile(filepath.Join(dir, name), file, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []struct {
		res   *synth.Result
		dst   *map[string]int
		ids   *[]string
		links *int
	}{{base, &man.BaseRows, nil, &man.BasePartnerLinkRows}, {phone, &man.PhoneRows, &man.PhoneEntryIDs, &man.PhonePartnerLinkRows}} {
		bk, err := trystbk.Read(bytes.NewReader(f.res.File), f.res.Password)
		if err != nil {
			t.Fatal(err)
		}
		*f.dst = rowCounts(bk)
		*f.links = bk.RowCount("encounter_partner")
		if f.ids != nil {
			for _, row := range bk.Table("encounters").Rows {
				id, _ := row.String("id")
				*f.ids = append(*f.ids, id)
			}
		}
		bk.Close()
	}
	b, _ := json.MarshalIndent(man, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "expected.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote base.tryst (%s), phone.tryst (%s), export.tryst (%s) and expected.json to %s", rtBasePW, rtPhonePW, rtExportPW, dir)
}

// normalise makes a value comparable across a Tryst restore and re-export: the device rewrites
// media file paths and re-escapes strings (D-15 addendum 4), neither of which is a data change.
func normalise(col string, raw json.RawMessage) string {
	if col == "encFilePath" {
		return "<path>"
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func rowsByKey(tb *trystbk.Table) (map[string]map[string]string, []string) {
	out := map[string]map[string]string{}
	var order []string
	for i, r := range tb.Rows {
		key, _ := r.String("id")
		if key == "" {
			var parts []string
			for _, k := range r.Keys {
				parts = append(parts, normalise(k, r.Values[k]))
			}
			key = fmt.Sprintf("#%d:%s", i, strings.Join(parts, "|"))
			if len(key) > 200 {
				key = key[:200]
			}
		}
		cols := map[string]string{}
		for _, k := range r.Keys {
			cols[k] = normalise(k, r.Values[k])
		}
		out[key] = cols
		order = append(order, key)
	}
	return out, order
}

// TestRoundTripCompare reads what the app exported after a restore and compares it with what was
// restored, table by table and column by column.
func TestRoundTripCompare(t *testing.T) {
	dir := os.Getenv("ALCOVE_ROUNDTRIP_DIR")
	spec := os.Getenv("ALCOVE_ROUNDTRIP_CHECK") // <restored file>:<its password>:<app's re-export>:<its password>
	if dir == "" || spec == "" {
		t.Skip("set ALCOVE_ROUNDTRIP_DIR and ALCOVE_ROUNDTRIP_CHECK")
	}
	parts := strings.Split(spec, ":")
	if len(parts) != 4 {
		t.Fatal("ALCOVE_ROUNDTRIP_CHECK must be <restored>:<password>:<reexport>:<password>")
	}
	open := func(file, pw string) *trystbk.Backup {
		b, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			t.Fatal(err)
		}
		bk, err := trystbk.Read(bytes.NewReader(b), pw)
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		return bk
	}
	want, got := open(parts[0], parts[1]), open(parts[2], parts[3])
	defer want.Close()
	defer got.Close()
	t.Logf("schema: restored %d, re-exported %d", want.SchemaVersion, got.SchemaVersion)
	diffs := 0
	seen := map[string]bool{}
	for _, wt := range want.Tables {
		seen[wt.Name] = true
		gt := got.Table(wt.Name)
		if gt == nil {
			if len(wt.Rows) > 0 {
				t.Errorf("table %q (%d rows) is missing from the re-export", wt.Name, len(wt.Rows))
				diffs++
			}
			continue
		}
		w, _ := rowsByKey(&wt)
		g, _ := rowsByKey(gt)
		for k, wc := range w {
			gc, ok := g[k]
			if !ok {
				t.Errorf("%s: row %.60s is missing after the restore", wt.Name, k)
				diffs++
				continue
			}
			for col, wv := range wc {
				if gv, ok := gc[col]; !ok || gv != wv {
					t.Errorf("%s %.40s column %s: restored %.80s, app wrote %.80s", wt.Name, k, col, wv, gv)
					diffs++
				}
			}
		}
		for k := range g {
			if _, ok := w[k]; !ok {
				t.Errorf("%s: row %.60s appeared after the restore", wt.Name, k)
				diffs++
			}
		}
		t.Logf("%-20s restored %3d  app re-export %3d", wt.Name, len(wt.Rows), len(gt.Rows))
	}
	var extra []string
	for _, gt := range got.Tables {
		if !seen[gt.Name] && len(gt.Rows) > 0 {
			extra = append(extra, gt.Name)
		}
	}
	sort.Strings(extra)
	if len(extra) > 0 {
		t.Errorf("tables with rows only in the re-export: %v", extra)
		diffs++
	}
	wm, gm := mediaNames(want), mediaNames(got)
	if strings.Join(wm, ",") != strings.Join(gm, ",") {
		t.Errorf("photos differ: restored %v, app wrote %v", wm, gm)
		diffs++
	}
	t.Logf("%d differences; photos restored %d, re-exported %d", diffs, len(wm), len(gm))
}

func mediaNames(bk *trystbk.Backup) []string {
	var out []string
	for _, m := range bk.Media() {
		out = append(out, m.Name)
	}
	sort.Strings(out)
	return out
}
