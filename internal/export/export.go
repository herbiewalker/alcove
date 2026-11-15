// Package export writes a person's current data back out as a fresh Tryst backup (D-43).
//
// The input is the upload the data started from (kept as it was: schema version, settings, unknown
// top-level keys, small entries, photo bytes) and the person's current rows, which are the upload's
// rows with every live edit replayed. The output is a new TRYSTBK1 file, never a change to the
// upload (constraint 1). Before the file is returned it is read back with the real reader, so a
// file that would not open is never offered.
package export

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/herbiewalker/alcove/internal/trystbk"
)

var (
	// ErrSchemaNewer means the upload comes from a Tryst newer than Alcove knows, so writing it back
	// could lose columns Alcove cannot see. Export is off for it, as editing is (D-29).
	ErrSchemaNewer = errors.New("export: the backup is from a newer Tryst than this server understands")
	// ErrNothing means there are no tables to write.
	ErrNothing = errors.New("export: there is nothing to export")
	// ErrCheck means the finished file did not read back as intended. Nothing is offered.
	ErrCheck = errors.New("export: the finished file did not pass its own check")
)

// Table is one table's current rows, each a JSON object with its columns in file order.
type Table struct {
	Name string
	Rows []string
}

// Plan is everything an export needs.
type Plan struct {
	Base     *trystbk.Backup // the upload; its photo bytes and untouched parts are reused
	Tables   []Table         // the current rows, in table order
	Password string          // what the new file is encrypted under
	// Iterations is the PBKDF2 count; zero means the container default. Tests use the minimum.
	Iterations int
}

// Result describes a finished export.
type Result struct {
	File    []byte
	Rows    map[string]int // rows written, per table
	Media   int            // photo blobs written
	Dropped int            // photo blobs left out because their only rows were deleted
}

// mediaRefs are the places Tryst's export spec says a photo blob can be referenced from (table, column).
var mediaRefs = [][2]string{{"media", "id"}, {"partners", "photoMediaId"}, {"profile", "photoMediaId"}, {"person_photo", "mediaBlobId"}}

// Build writes the export and proves it reads back.
func Build(p Plan) (Result, error) {
	if p.Base == nil || len(p.Tables) == 0 {
		return Result{}, ErrNothing
	}
	if p.Base.SchemaStatus() == trystbk.SchemaNewer {
		return Result{}, ErrSchemaNewer
	}

	data, err := dataJSON(p)
	if err != nil {
		return Result{}, err
	}
	keep, dropped := mediaToKeep(p)
	body, err := zipBody(p.Base, data, keep)
	if err != nil {
		return Result{}, err
	}
	file, err := trystbk.Seal(p.Password, body, trystbk.SealOptions{Version: p.Base.Header.Version, Iterations: p.Iterations})
	if err != nil {
		return Result{}, err
	}
	res := Result{File: file, Rows: map[string]int{}, Media: len(keep), Dropped: dropped}
	for _, t := range p.Tables {
		res.Rows[t.Name] = len(t.Rows)
	}
	if err := check(p, file, keep); err != nil {
		return Result{}, err
	}
	return res, nil
}

// dataJSON writes { "schemaVersion": N, "tables": {...}, <unknown keys> } with every row exactly as given.
func dataJSON(p Plan) ([]byte, error) {
	var b bytes.Buffer
	fmt.Fprintf(&b, `{"schemaVersion":%d,"tables":{`, p.Base.SchemaVersion)
	for i, t := range p.Tables {
		if i > 0 {
			b.WriteByte(',')
		}
		name, _ := json.Marshal(t.Name)
		b.Write(name)
		b.WriteString(":[")
		for j, r := range t.Rows {
			if j > 0 {
				b.WriteByte(',')
			}
			if !json.Valid([]byte(r)) {
				return nil, fmt.Errorf("export: a row of %q is not valid JSON", t.Name)
			}
			b.WriteString(r)
		}
		b.WriteByte(']')
	}
	b.WriteByte('}')
	for _, x := range p.Base.Extra {
		k, _ := json.Marshal(x.Key)
		b.WriteByte(',')
		b.Write(k)
		b.WriteByte(':')
		b.Write(x.Value)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// refIDs lists the photo blob ids the rows refer to. rows are JSON objects.
func refIDs(table string, rows []string) map[string]bool {
	var col string
	for _, r := range mediaRefs {
		if r[0] == table {
			col = r[1]
		}
	}
	out := map[string]bool{}
	if col == "" {
		return out
	}
	for _, r := range rows {
		var m map[string]json.RawMessage
		if json.Unmarshal([]byte(r), &m) != nil {
			continue
		}
		var s string
		if json.Unmarshal(m[col], &s) == nil && s != "" {
			out[s] = true
		}
	}
	return out
}

// mediaToKeep decides which photo blobs go in the file: every blob of the upload, except one that
// the upload's rows referred to and no current row does (its entry was deleted). A wipe-first
// restore writes every blob in the file, so a deleted photo left in would come back to the phone.
// A blob the upload never referred to passes through untouched.
func mediaToKeep(p Plan) (keep []string, dropped int) {
	before, after := map[string]bool{}, map[string]bool{}
	for _, t := range p.Base.Tables {
		rows := make([]string, 0, len(t.Rows))
		for _, r := range t.Rows {
			rows = append(rows, rowText(r))
		}
		for id := range refIDs(t.Name, rows) {
			before[id] = true
		}
	}
	for _, t := range p.Tables {
		for id := range refIDs(t.Name, t.Rows) {
			after[id] = true
		}
	}
	for _, m := range p.Base.Media() {
		id := m.Name[len(trystbk.EntryMediaPrefix):]
		if before[id] && !after[id] {
			dropped++
			continue
		}
		keep = append(keep, m.Name)
	}
	return keep, dropped
}

func rowText(r trystbk.Row) string {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range r.Keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		b.Write(kb)
		b.WriteByte(':')
		b.Write(r.Values[k])
	}
	b.WriteByte('}')
	return b.String()
}

// zipBody builds the plaintext archive the way Tryst reads it: every entry DEFLATED, data.json first
// (D-15 addendum 3), then settings and other small entries, then the photos.
func zipBody(base *trystbk.Backup, data []byte, keep []string) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name string, body []byte) error {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		if err != nil {
			return err
		}
		_, err = w.Write(body)
		return err
	}
	if err := add(trystbk.EntryDataJSON, data); err != nil {
		return nil, err
	}
	if base.Settings != nil {
		if err := add(trystbk.EntrySettingsJSON, base.Settings); err != nil {
			return nil, err
		}
	}
	for _, e := range base.Entries {
		if err := add(e.Name, e.Data); err != nil {
			return nil, err
		}
	}
	for _, name := range keep {
		rc, err := base.OpenMedia(name)
		if err != nil {
			return nil, fmt.Errorf("export: photo %q: %w", name, err)
		}
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		if err != nil {
			rc.Close()
			return nil, err
		}
		_, err = io.Copy(w, rc)
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("export: photo %q: %w", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// check reads the finished file back with the real reader and compares it to what was meant.
func check(p Plan, file []byte, keep []string) error {
	got, err := trystbk.Read(bytes.NewReader(file), p.Password)
	if err != nil {
		return fmt.Errorf("%w: it does not open", ErrCheck)
	}
	defer got.Close()
	switch {
	case got.SchemaVersion != p.Base.SchemaVersion:
		return fmt.Errorf("%w: schema version", ErrCheck)
	case got.Header.Version != p.Base.Header.Version:
		return fmt.Errorf("%w: container version", ErrCheck)
	case len(got.Tables) != len(p.Tables):
		return fmt.Errorf("%w: table list", ErrCheck)
	case !bytes.Equal(got.Settings, p.Base.Settings):
		return fmt.Errorf("%w: settings", ErrCheck)
	case len(got.Extra) != len(p.Base.Extra) || len(got.Entries) != len(p.Base.Entries):
		return fmt.Errorf("%w: carried entries", ErrCheck)
	}
	for i, t := range p.Tables {
		g := got.Tables[i]
		if g.Name != t.Name || len(g.Rows) != len(t.Rows) {
			return fmt.Errorf("%w: table %q", ErrCheck, t.Name)
		}
		for j, r := range g.Rows {
			if !sameRow(r, t.Rows[j]) {
				return fmt.Errorf("%w: a row of %q", ErrCheck, t.Name)
			}
		}
	}
	var have []string
	for _, m := range got.Media() {
		have = append(have, m.Name)
	}
	want := append([]string(nil), keep...)
	sort.Strings(have)
	sort.Strings(want)
	if len(have) != len(want) {
		return fmt.Errorf("%w: photos", ErrCheck)
	}
	for i := range have {
		if have[i] != want[i] {
			return fmt.Errorf("%w: photos", ErrCheck)
		}
	}
	return nil
}

// sameRow compares a row read back with the row that was written, by column and value (the raw
// JSON of each value, not its spacing).
func sameRow(r trystbk.Row, want string) bool {
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(want), &m) != nil || len(m) != len(r.Keys) {
		return false
	}
	for _, k := range r.Keys {
		w, ok := m[k]
		if !ok {
			return false
		}
		if !bytes.Equal(compact(w), compact(r.Values[k])) {
			return false
		}
	}
	return true
}

func compact(b []byte) []byte {
	var out bytes.Buffer
	if json.Compact(&out, b) != nil {
		return b
	}
	return out.Bytes()
}
