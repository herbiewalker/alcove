package export

import (
	"archive/zip"
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/herbiewalker/alcove/internal/trystbk"
	"github.com/herbiewalker/alcove/internal/trystbk/synth"
)

const pw = "export-test-password"

// plan reads a synthetic backup and returns an export plan holding its own rows, unchanged.
func plan(t *testing.T, o synth.Options) (Plan, *synth.Result) {
	t.Helper()
	o.Iterations = trystbk.MinIterations
	res, err := synth.Build(o)
	if err != nil {
		t.Fatal(err)
	}
	bk, err := trystbk.Read(bytes.NewReader(res.File), res.Password)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bk.Close)
	p := Plan{Base: bk, Password: pw, Iterations: trystbk.MinIterations}
	for _, tb := range bk.Tables {
		x := Table{Name: tb.Name}
		for _, r := range tb.Rows {
			x.Rows = append(x.Rows, rowText(r))
		}
		p.Tables = append(p.Tables, x)
	}
	return p, res
}

func read(t *testing.T, file []byte, password string) *trystbk.Backup {
	t.Helper()
	bk, err := trystbk.Read(bytes.NewReader(file), password)
	if err != nil {
		t.Fatalf("the export does not open: %v", err)
	}
	t.Cleanup(bk.Close)
	return bk
}

func table(p *Plan, name string) *Table {
	for i := range p.Tables {
		if p.Tables[i].Name == name {
			return &p.Tables[i]
		}
	}
	return nil
}

func TestAnUntouchedUploadReadsBackIdenticalRowForRow(t *testing.T) {
	p, res := plan(t, synth.Options{Seed: 1, Encounters: 9, Partners: 3, Photos: 3, IncludeSettings: true})
	out, err := Build(p)
	if err != nil {
		t.Fatal(err)
	}
	if out.Dropped != 0 || out.Media != 3 {
		t.Errorf("photos: kept %d dropped %d", out.Media, out.Dropped)
	}
	got := read(t, out.File, pw)
	if got.SchemaVersion != p.Base.SchemaVersion || got.Header.Version != p.Base.Header.Version {
		t.Errorf("versions changed: %d/%d", got.SchemaVersion, got.Header.Version)
	}
	for i, tb := range p.Base.Tables {
		g := got.Tables[i]
		if g.Name != tb.Name || len(g.Rows) != len(tb.Rows) {
			t.Fatalf("table %q: %d rows, want %d", tb.Name, len(g.Rows), len(tb.Rows))
		}
		for j := range tb.Rows {
			if rowText(g.Rows[j]) != rowText(tb.Rows[j]) {
				t.Errorf("%s row %d differs:\n got %s\nwant %s", tb.Name, j, rowText(g.Rows[j]), rowText(tb.Rows[j]))
			}
		}
	}
	if !bytes.Equal(got.Settings, p.Base.Settings) || len(got.Settings) == 0 {
		t.Error("settings.json was not carried")
	}
	// Every photo's bytes are the same.
	for _, id := range res.MediaIDs {
		a, b := mediaBytes(t, p.Base, id), mediaBytes(t, got, id)
		if len(a) == 0 || !bytes.Equal(a, b) {
			t.Errorf("photo %s changed", id)
		}
	}
	// The old password no longer opens it; the one it was sealed with does.
	if _, err := trystbk.Read(bytes.NewReader(out.File), res.Password); err == nil {
		t.Error("the upload's password opened an export sealed under another")
	}
}

func mediaBytes(t *testing.T, bk *trystbk.Backup, id string) []byte {
	t.Helper()
	rc, err := bk.OpenMedia(trystbk.EntryMediaPrefix + id)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	var b bytes.Buffer
	if _, err := b.ReadFrom(rc); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestEditsAdditionsAndDeletionsAreWrittenAndADeletedPhotoIsLeftOut(t *testing.T) {
	p, res := plan(t, synth.Options{Seed: 2, Encounters: 6, Partners: 2, Photos: 4})
	enc := table(p0(&p), "encounters")
	media := table(p0(&p), "media")
	if len(media.Rows) != 4 {
		t.Fatalf("expected 4 photos, got %d", len(media.Rows))
	}
	// Delete the entry that owns the first photo, together with every photo row of it.
	gone := jsonField(t, media.Rows[0], "encounterId")
	deletedPhotos := map[string]bool{}
	var keepRows []string
	for _, r := range media.Rows {
		if jsonField(t, r, "encounterId") == gone {
			deletedPhotos[jsonField(t, r, "id")] = true
		} else {
			keepRows = append(keepRows, r)
		}
	}
	media.Rows = keepRows
	var encRows []string
	for _, r := range enc.Rows {
		if jsonField(t, r, "id") != gone {
			encRows = append(encRows, r)
		}
	}
	// Edit one remaining entry's note, and add a new row that copies an existing one with a new id.
	encRows[0] = strings.Replace(encRows[0], `"note":`, `"note":"EDITED NOTE","oldNote":`, 1)
	added := strings.Replace(encRows[1], jsonField(t, encRows[1], "id"), "11111111-2222-4333-8444-555555555555", 1)
	enc.Rows = append(encRows, added)
	// Rename a partner.
	pr := table(&p, "partners")
	pr.Rows[0] = strings.Replace(pr.Rows[0], jsonField(t, pr.Rows[0], "displayName"), "Renamed Partner", 1)

	out, err := Build(p)
	if err != nil {
		t.Fatal(err)
	}
	if out.Dropped != len(deletedPhotos) || out.Media != 4-len(deletedPhotos) {
		t.Errorf("photos kept %d dropped %d, deleted %d", out.Media, out.Dropped, len(deletedPhotos))
	}
	got := read(t, out.File, pw)
	if n := got.RowCount("encounters"); n != 6 { // 6 - 1 deleted + 1 added
		t.Errorf("%d entries, want 6", n)
	}
	for _, r := range got.Table("encounters").Rows {
		if id, _ := r.String("id"); id == gone {
			t.Error("a deleted entry is in the export")
		}
	}
	names := map[string]bool{}
	for _, m := range got.Media() {
		names[m.Name] = true
	}
	for _, id := range res.MediaIDs {
		in := names[trystbk.EntryMediaPrefix+id]
		if deletedPhotos[id] == in {
			t.Errorf("photo %s: in file %v, deleted %v", id, in, deletedPhotos[id])
		}
	}
	if !strings.Contains(rowText(got.Table("encounters").Rows[0]), "EDITED NOTE") {
		t.Error("the edited note is missing")
	}
	if !strings.Contains(rowText(got.Table("partners").Rows[0]), "Renamed Partner") {
		t.Error("the partner rename is missing")
	}
	found := false
	for _, r := range got.Table("encounters").Rows {
		if id, _ := r.String("id"); id == "11111111-2222-4333-8444-555555555555" {
			found = true
		}
	}
	if !found {
		t.Error("the added entry is missing")
	}
}

// p0 lets a test take a pointer to a plan it already holds by value.
func p0(p *Plan) *Plan { return p }

func jsonField(t *testing.T, row, col string) string {
	t.Helper()
	i := strings.Index(row, `"`+col+`":"`)
	if i < 0 {
		t.Fatalf("%s not in %.120s", col, row)
	}
	rest := row[i+len(col)+4:]
	return rest[:strings.Index(rest, `"`)]
}

// A backup carrying things Alcove has never heard of: they must go back out untouched.
func crafted(t *testing.T, schema string) (Plan, []byte) {
	t.Helper()
	data := `{"schemaVersion":` + schema + `,"tables":{"encounters":[{"id":"e1","startAt":1,"newColumnFromTheFuture":{"a":[1,2,3]},"note":"x"}],` +
		`"gadgets":[{"id":"g1","size":"é\/odd"}],"media":[{"id":"m1","encounterId":"e1"}]},"futureKey":{"k":true}}`
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range []struct{ name, body string }{
		{"data.json", data}, {"settings.json", `{"prefs":{"a":{"t":"string","v":"b"}}}`}, {"notes.txt", "kept"},
		{"media/m1", "referenced"}, {"media/orphan", "never referenced by any row"},
	} {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: f.name, Method: zip.Deflate})
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(f.body))
	}
	zw.Close()
	file, err := trystbk.Seal(pw, buf.Bytes(), trystbk.SealOptions{Iterations: trystbk.MinIterations})
	if err != nil {
		t.Fatal(err)
	}
	bk, err := trystbk.Read(bytes.NewReader(file), pw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bk.Close)
	p := Plan{Base: bk, Password: "another-password", Iterations: trystbk.MinIterations}
	for _, tb := range bk.Tables {
		x := Table{Name: tb.Name}
		for _, r := range tb.Rows {
			x.Rows = append(x.Rows, rowText(r))
		}
		p.Tables = append(p.Tables, x)
	}
	return p, file
}

func TestWhatAlcoveDoesNotKnowPassesThroughUntouched(t *testing.T) {
	p, _ := crafted(t, "19")
	out, err := Build(p)
	if err != nil {
		t.Fatal(err)
	}
	got := read(t, out.File, "another-password")
	if len(got.Extra) != 1 || got.Extra[0].Key != "futureKey" || string(got.Extra[0].Value) != `{"k":true}` {
		t.Errorf("unknown top-level key: %+v", got.Extra)
	}
	if string(got.Settings) != string(p.Base.Settings) {
		t.Errorf("settings: %s", got.Settings)
	}
	if len(got.Entries) != 1 || got.Entries[0].Name != "notes.txt" || string(got.Entries[0].Data) != "kept" {
		t.Errorf("small entries: %+v", got.Entries)
	}
	if got.Table("gadgets") == nil || got.RowCount("gadgets") != 1 {
		t.Error("an unknown table was dropped")
	}
	if r := rowText(got.Table("encounters").Rows[0]); !strings.Contains(r, `"newColumnFromTheFuture":{"a":[1,2,3]}`) {
		t.Errorf("an unknown column changed: %s", r)
	}
	names := map[string]bool{}
	for _, m := range got.Media() {
		names[m.Name] = true
	}
	if !names["media/m1"] || !names["media/orphan"] || out.Dropped != 0 {
		t.Errorf("media: %v dropped %d (a blob no row referred to must pass through)", names, out.Dropped)
	}
}

func TestANewerTrystIsRefusedAndNothingIsRefusedToo(t *testing.T) {
	p, _ := crafted(t, "999")
	if _, err := Build(p); !errors.Is(err, ErrSchemaNewer) {
		t.Errorf("a newer schema: %v", err)
	}
	if _, err := Build(Plan{Base: p.Base, Password: pw}); !errors.Is(err, ErrNothing) {
		t.Errorf("no tables: %v", err)
	}
	if _, err := Build(Plan{Tables: p.Tables, Password: pw}); !errors.Is(err, ErrNothing) {
		t.Errorf("no base: %v", err)
	}
}

func TestTheArchiveIsDeflatedWithDataJSONFirst(t *testing.T) {
	p, _ := plan(t, synth.Options{Seed: 3, Encounters: 4, Photos: 3, IncludeSettings: true})
	data, err := dataJSON(p)
	if err != nil {
		t.Fatal(err)
	}
	keep, _ := mediaToKeep(p)
	body, err := zipBody(p.Base, data, keep)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	if zr.File[0].Name != "data.json" {
		t.Errorf("first entry is %q", zr.File[0].Name)
	}
	for _, f := range zr.File {
		if f.Method != zip.Deflate {
			t.Errorf("%s is not DEFLATED", f.Name)
		}
	}
}

func TestAFileThatDoesNotMatchTheIntentIsCaught(t *testing.T) {
	p, _ := plan(t, synth.Options{Seed: 4, Encounters: 5, Photos: 2})
	out, err := Build(p)
	if err != nil {
		t.Fatal(err)
	}
	keep, _ := mediaToKeep(p)
	// Wrong row count, wrong value in a row, wrong password, a photo that should not be there.
	short := p
	short.Tables = append([]Table(nil), p.Tables...)
	short.Tables[0] = Table{Name: p.Tables[0].Name, Rows: p.Tables[0].Rows[:1]}
	if err := check(short, out.File, keep); !errors.Is(err, ErrCheck) {
		t.Errorf("a missing row went unnoticed: %v", err)
	}
	changed := p
	changed.Tables = append([]Table(nil), p.Tables...)
	enc := table(&changed, "encounters")
	rows := append([]string(nil), enc.Rows...)
	rows[0] = strings.Replace(rows[0], `"note":`, `"note":"CHANGED-AFTER","was":`, 1)
	changed.Tables[indexOf(&changed, "encounters")] = Table{Name: "encounters", Rows: rows}
	if err := check(changed, out.File, keep); !errors.Is(err, ErrCheck) {
		t.Errorf("a changed row went unnoticed: %v", err)
	}
	wrong := p
	wrong.Password = "not-the-password"
	if err := check(wrong, out.File, keep); !errors.Is(err, ErrCheck) {
		t.Errorf("a wrong password went unnoticed: %v", err)
	}
	if err := check(p, out.File, keep[:1]); !errors.Is(err, ErrCheck) {
		t.Errorf("a photo mismatch went unnoticed: %v", err)
	}
}

func indexOf(p *Plan, name string) int {
	for i := range p.Tables {
		if p.Tables[i].Name == name {
			return i
		}
	}
	return -1
}
