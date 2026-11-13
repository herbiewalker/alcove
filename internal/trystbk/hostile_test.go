package trystbk

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Hostile and malformed backups (THREAT_MODEL T6, requirement N9). Every case here is a
// forced failure: it builds the bad input, seals it with a valid key so the file reaches
// the stage under test, and asserts a clean, typed rejection with no Backup returned.

const testPassword = "hostile-test-password"

type ze struct {
	name   string
	body   []byte
	method uint16
}

func zipOf(t *testing.T, entries ...ze) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: e.name, Method: e.method})
		if err != nil {
			t.Fatalf("CreateHeader %q: %v", e.name, err)
		}
		if len(e.body) > 0 {
			if _, err := w.Write(e.body); err != nil {
				t.Fatalf("Write %q: %v", e.name, err)
			}
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func readZipBody(t *testing.T, zipBody []byte) (*Backup, error) {
	t.Helper()
	file, err := Seal(testPassword, zipBody, SealOptions{Iterations: MinIterations})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	return Read(bytes.NewReader(file), testPassword)
}

const minimalData = `{"schemaVersion":19,"tables":{"encounters":[{"id":"a"}]}}`

func good(t *testing.T) ze { return ze{"data.json", []byte(minimalData), zip.Deflate} }

func wantErr(t *testing.T, name string, b *Backup, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Errorf("%s: err = %v, want %v", name, err, want)
	}
	if b != nil {
		t.Errorf("%s: a Backup was returned for a rejected file", name)
	}
}

func TestHostile_ZipSlipNames(t *testing.T) {
	for _, name := range []string{"../evil", "/abs/path", `a\b`, "media/../../x", "a/../../x"} {
		b, err := readZipBody(t, zipOf(t, good(t), ze{name, []byte("x"), zip.Store}))
		wantErr(t, "zip-slip "+name, b, err, ErrZipSlip)
	}
}

func TestHostile_NotAZip(t *testing.T) {
	b, err := readZipBody(t, []byte("this is not a zip archive at all"))
	wantErr(t, "not a zip", b, err, ErrBadZip)
}

func TestHostile_MissingDataJSON(t *testing.T) {
	b, err := readZipBody(t, zipOf(t, ze{"media/x", []byte("x"), zip.Store}))
	wantErr(t, "missing data.json", b, err, ErrNoDataJSON)
}

func TestHostile_DuplicateDataJSON(t *testing.T) {
	b, err := readZipBody(t, zipOf(t, good(t), good(t)))
	wantErr(t, "duplicate data.json", b, err, ErrDataJSONMalformed)
}

func TestHostile_MalformedDataJSON(t *testing.T) {
	cases := map[string]string{
		"not json":                `this is not json`,
		"not an object":           `[1,2,3]`,
		"missing tables":          `{"schemaVersion":19}`,
		"missing schemaVersion":   `{"tables":{}}`,
		"schemaVersion string":    `{"schemaVersion":"19","tables":{}}`,
		"schemaVersion fraction":  `{"schemaVersion":19.5,"tables":{}}`,
		"duplicate schemaVersion": `{"schemaVersion":19,"schemaVersion":19,"tables":{}}`,
		"duplicate tables":        `{"schemaVersion":19,"tables":{},"tables":{}}`,
		"reserved table name":     `{"schemaVersion":19,"tables":{"__media__":[]}}`,
		"duplicate table":         `{"schemaVersion":19,"tables":{"a":[],"a":[]}}`,
		"table not an array":      `{"schemaVersion":19,"tables":{"a":{}}}`,
		"row not an object":       `{"schemaVersion":19,"tables":{"a":[1]}}`,
		"duplicate column":        `{"schemaVersion":19,"tables":{"a":[{"id":1,"id":2}]}}`,
		"truncated":               `{"schemaVersion":19,"tables":{"a":[{"id":`,
	}
	for name, body := range cases {
		b, err := readZipBody(t, zipOf(t, ze{"data.json", []byte(body), zip.Deflate}))
		wantErr(t, name, b, err, ErrDataJSONMalformed)
	}
}

func TestHostile_TooManyEntries(t *testing.T) {
	old := lim.zipEntries
	lim.zipEntries = 50
	t.Cleanup(func() { lim.zipEntries = old })
	entries := []ze{good(t)}
	for i := 0; i < 60; i++ {
		entries = append(entries, ze{fmt.Sprintf("media/%d", i), nil, zip.Store})
	}
	b, err := readZipBody(t, zipOf(t, entries...))
	wantErr(t, "too many entries", b, err, ErrTooManyEntries)
}

func TestHostile_DeclaredTotalSizeBomb(t *testing.T) {
	old := lim.totalUncompressed
	lim.totalUncompressed = 1000
	t.Cleanup(func() { lim.totalUncompressed = old })
	b, err := readZipBody(t, zipOf(t, good(t), ze{"media/big", bytes.Repeat([]byte{0}, 4000), zip.Deflate}))
	wantErr(t, "declared total size", b, err, ErrZipBomb)
}

func TestHostile_OversizeBody(t *testing.T) {
	old := lim.body
	lim.body = 512
	t.Cleanup(func() { lim.body = old })
	b, err := readZipBody(t, zipOf(t, good(t), ze{"media/pad", bytes.Repeat([]byte{1}, 4096), zip.Store}))
	wantErr(t, "oversize body", b, err, ErrOversize)
}

func TestHostile_DataJSONTooLarge(t *testing.T) {
	old := lim.dataJSON
	lim.dataJSON = 20
	t.Cleanup(func() { lim.dataJSON = old })
	b, err := readZipBody(t, zipOf(t, good(t)))
	wantErr(t, "data.json declared too large", b, err, ErrEntryTooLarge)
}

func TestHostile_SmallEntryTooLarge(t *testing.T) {
	old := lim.smallEntry
	lim.smallEntry = 16
	t.Cleanup(func() { lim.smallEntry = old })
	b, err := readZipBody(t, zipOf(t, good(t), ze{"settings.json", bytes.Repeat([]byte("x"), 100), zip.Store}))
	wantErr(t, "settings.json too large", b, err, ErrEntryTooLarge)
}

func TestHostile_TooManyRows(t *testing.T) {
	old := lim.rows
	lim.rows = 3
	t.Cleanup(func() { lim.rows = old })
	body := `{"schemaVersion":19,"tables":{"a":[{"i":1},{"i":2},{"i":3},{"i":4}]}}`
	b, err := readZipBody(t, zipOf(t, ze{"data.json", []byte(body), zip.Deflate}))
	wantErr(t, "too many rows", b, err, ErrDataJSONMalformed)
}

func TestMedia_OnDemandAndBounded(t *testing.T) {
	data := bytes.Repeat([]byte("p"), 300)
	b, err := readZipBody(t, zipOf(t, good(t), ze{"media/p1", data, zip.Store}))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if m := b.Media(); len(m) != 1 || m[0].Name != "media/p1" || m[0].Size != 300 {
		t.Fatalf("Media() = %+v", m)
	}
	rc, err := b.OpenMedia("media/p1")
	if err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	if _, err := got.ReadFrom(rc); err != nil || !bytes.Equal(got.Bytes(), data) {
		t.Fatalf("media bytes differ: %v", err)
	}
	rc.Close()
	if _, err := b.OpenMedia("media/missing"); !errors.Is(err, ErrNoSuchMedia) {
		t.Errorf("missing media: %v", err)
	}
	if _, err := b.OpenMedia("data.json"); !errors.Is(err, ErrNoSuchMedia) {
		t.Errorf("a non-media entry must not open through OpenMedia: %v", err)
	}
	old := lim.mediaEntry
	lim.mediaEntry = 100
	t.Cleanup(func() { lim.mediaEntry = old })
	if _, err := b.OpenMedia("media/p1"); !errors.Is(err, ErrMediaTooLarge) {
		t.Errorf("oversize media: %v", err)
	}
}

func TestUnknownEntriesAndTopLevelKeysAreCarried(t *testing.T) {
	body := `{"schemaVersion":19,"futureKey":{"a":[1,2,3]},"tables":{"encounters":[{"id":"a"}]}}`
	b, err := readZipBody(t, zipOf(t,
		ze{"data.json", []byte(body), zip.Deflate},
		ze{"settings.json", []byte(`{"s":1}`), zip.Deflate},
		ze{"future.bin", []byte("abc"), zip.Store},
	))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if len(b.Extra) != 1 || b.Extra[0].Key != "futureKey" || string(b.Extra[0].Value) != `{"a":[1,2,3]}` {
		t.Errorf("Extra = %+v", b.Extra)
	}
	if string(b.Settings) != `{"s":1}` {
		t.Errorf("Settings = %q", b.Settings)
	}
	if len(b.Entries) != 1 || b.Entries[0].Name != "future.bin" || string(b.Entries[0].Data) != "abc" {
		t.Errorf("Entries = %+v", b.Entries)
	}
	if !strings.Contains(string(b.Table("encounters").Rows[0].Values["id"]), "a") {
		t.Error("row not read")
	}
}

func TestClose_ZeroesTheDecryptedBody(t *testing.T) {
	b, err := readZipBody(t, zipOf(t, good(t)))
	if err != nil {
		t.Fatal(err)
	}
	body := b.body // the same backing array Close must wipe
	if len(body) == 0 || bytes.Equal(body, make([]byte, len(body))) {
		t.Fatal("test setup: body should hold plaintext before Close")
	}
	b.Close()
	if !bytes.Equal(body, make([]byte, len(body))) {
		t.Fatal("Close left decrypted plaintext in memory")
	}
	b.Close() // closing twice must be safe
}
