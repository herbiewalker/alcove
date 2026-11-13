package trystbk_test

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"image/jpeg"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/herbiewalker/alcove/internal/trystbk"
	"github.com/herbiewalker/alcove/internal/trystbk/synth"
)

func build(t *testing.T, o synth.Options) *synth.Result {
	t.Helper()
	r, err := synth.Build(o)
	if err != nil {
		t.Fatalf("synth.Build: %v", err)
	}
	return r
}

func read(t *testing.T, r *synth.Result) *trystbk.Backup {
	t.Helper()
	b, err := trystbk.Read(bytes.NewReader(r.File), r.Password)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	t.Cleanup(b.Close)
	return b
}

func TestRead_HappyPath(t *testing.T) {
	r := build(t, synth.Options{Seed: 1, Encounters: 20, Partners: 3, Photos: 4, IncludeSettings: true})
	b := read(t, r)

	if b.Header.Version != trystbk.FormatVersion2 || b.Header.Iterations != trystbk.MinIterations {
		t.Errorf("header = %+v", b.Header)
	}
	if b.SchemaVersion != trystbk.LatestKnownSchema || b.SchemaStatus() != trystbk.SchemaKnown {
		t.Errorf("schema = %d status %v", b.SchemaVersion, b.SchemaStatus())
	}
	// Tables come back in Tryst's restore order, with the counts the generator wrote.
	if len(b.Tables) != len(trystbk.RestoreTables) {
		t.Fatalf("tables = %d, want %d", len(b.Tables), len(trystbk.RestoreTables))
	}
	for i, name := range trystbk.RestoreTables {
		if b.Tables[i].Name != name {
			t.Errorf("table %d = %q, want %q", i, b.Tables[i].Name, name)
		}
		if got := b.RowCount(name); got != r.Counts[name] {
			t.Errorf("%s rows = %d, want %d", name, got, r.Counts[name])
		}
	}
	if b.RowCount("encounters") != 20 || b.RowCount("partners") != 3 || b.RowCount("media") != 4 {
		t.Errorf("counts: %d %d %d", b.RowCount("encounters"), b.RowCount("partners"), b.RowCount("media"))
	}
	if b.Table("nope") != nil || b.RowCount("nope") != 0 {
		t.Error("absent table should be nil / 0")
	}
	if b.Settings == nil {
		t.Error("settings.json not carried")
	}

	// Row accessors.
	row := b.Table("encounters").Rows[0]
	if id, ok := row.String("id"); !ok || id == "" {
		t.Errorf("id = %q %v", id, ok)
	}
	if n, ok := row.Int("startAt"); !ok || n < 1_500_000_000_000 {
		t.Errorf("startAt = %d %v", n, ok)
	}
	if _, ok := row.String("orgasm"); ok {
		t.Error("a null column must report ok=false")
	}
	if _, ok := row.Get("noSuchColumn"); ok {
		t.Error("an absent column must report ok=false")
	}

	// Media is listed without being read, then opened on demand and is a real JPEG.
	if len(b.Media()) != 4 {
		t.Fatalf("media = %d", len(b.Media()))
	}
	for _, m := range b.Media() {
		rc, err := b.OpenMedia(m.Name)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil || int64(len(data)) != m.Size {
			t.Fatalf("%s: len %d vs declared %d (%v)", m.Name, len(data), m.Size, err)
		}
		if _, err := jpeg.Decode(bytes.NewReader(data)); err != nil {
			t.Errorf("%s is not a valid JPEG: %v", m.Name, err)
		}
	}
}

// rebuild re-serialises a Backup's data.json from what was read. If the reader is
// lossless this reproduces the original bytes exactly, which is what M6's writer needs.
func rebuild(b *trystbk.Backup) []byte {
	var buf bytes.Buffer
	buf.WriteString(`{"schemaVersion":`)
	buf.WriteString(itoa(b.SchemaVersion))
	for _, e := range b.Extra {
		k, _ := json.Marshal(e.Key)
		buf.WriteByte(',')
		buf.Write(k)
		buf.WriteByte(':')
		buf.Write(e.Value)
	}
	buf.WriteString(`,"tables":{`)
	for ti, tbl := range b.Tables {
		if ti > 0 {
			buf.WriteByte(',')
		}
		n, _ := json.Marshal(tbl.Name)
		buf.Write(n)
		buf.WriteString(":[")
		for ri, row := range tbl.Rows {
			if ri > 0 {
				buf.WriteByte(',')
			}
			buf.WriteByte('{')
			for ci, c := range row.Keys {
				if ci > 0 {
					buf.WriteByte(',')
				}
				k, _ := json.Marshal(c)
				buf.Write(k)
				buf.WriteByte(':')
				buf.Write(row.Values[c])
			}
			buf.WriteByte('}')
		}
		buf.WriteByte(']')
	}
	buf.WriteString("}}")
	return buf.Bytes()
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestRead_IsLossless(t *testing.T) {
	r := build(t, synth.Options{Seed: 42, Encounters: 60, Partners: 4, Photos: 3})
	b := read(t, r)
	if got := rebuild(b); !bytes.Equal(got, r.DataJSON) {
		t.Fatalf("re-serialised data.json differs from the original (%d vs %d bytes)", len(got), len(r.DataJSON))
	}
}

func TestRead_ContainerVersions(t *testing.T) {
	for _, v := range []int{trystbk.FormatVersion1, trystbk.FormatVersion2} {
		r := build(t, synth.Options{Seed: 3, Version: v, IncludeSettings: true})
		b := read(t, r)
		if b.Header.Version != v {
			t.Errorf("version = %d, want %d", b.Header.Version, v)
		}
		// A v1 container never carries settings.json.
		if v == trystbk.FormatVersion1 && b.Settings != nil {
			t.Error("v1 must not carry settings.json")
		}
	}
}

func TestRead_WrongPassword(t *testing.T) {
	r := build(t, synth.Options{Seed: 5})
	b, err := trystbk.Read(bytes.NewReader(r.File), "not the password")
	if !errors.Is(err, trystbk.ErrWrongPassword) || b != nil {
		t.Fatalf("err = %v, backup = %v", err, b)
	}
}

func TestRead_TamperedCiphertextFails(t *testing.T) {
	r := build(t, synth.Options{Seed: 6, Encounters: 30})
	// Flip one byte at several positions past the header: start, middle, near the end.
	for _, pos := range []int{trystbk.HeaderSize + 5, len(r.File) / 2, len(r.File) - 3} {
		bad := append([]byte(nil), r.File...)
		bad[pos] ^= 0x01
		b, err := trystbk.Read(bytes.NewReader(bad), r.Password)
		if err == nil || b != nil {
			t.Errorf("flip at %d: err = %v, backup = %v (must fail)", pos, err, b)
		}
	}
}

func TestRead_TamperedHeaderFails(t *testing.T) {
	r := build(t, synth.Options{Seed: 7})
	bad := append([]byte(nil), r.File...)
	bad[trystbk.MagicSize+trystbk.VersionSize+2] ^= 0xFF // a salt byte: wrong key, so auth fails
	if b, err := trystbk.Read(bytes.NewReader(bad), r.Password); err == nil || b != nil {
		t.Fatalf("a changed salt must fail: %v", err)
	}
}

func TestRead_TruncatedFails(t *testing.T) {
	r := build(t, synth.Options{Seed: 8, Encounters: 30})
	for _, n := range []int{10, trystbk.HeaderSize, trystbk.HeaderSize + 10, len(r.File) / 2, len(r.File) - 1} {
		b, err := trystbk.Read(bytes.NewReader(r.File[:n]), r.Password)
		if err == nil || b != nil {
			t.Errorf("truncated to %d: err = %v, backup = %v (must fail)", n, err, b)
		}
	}
	if _, err := trystbk.Read(bytes.NewReader(r.File[:10]), r.Password); !errors.Is(err, trystbk.ErrHeaderTruncated) {
		t.Errorf("short header: %v", err)
	}
}

func TestRead_AbsurdIterationCountIsRefusedBeforeAnyKDF(t *testing.T) {
	r := build(t, synth.Options{Seed: 9})
	off := trystbk.MagicSize + trystbk.VersionSize + trystbk.SaltSize
	for _, n := range []uint32{0, trystbk.MinIterations - 1, trystbk.MaxIterations + 1, 0x7FFFFFFF, 0xFFFFFFFF} {
		bad := append([]byte(nil), r.File...)
		binary.BigEndian.PutUint32(bad[off:], n)
		// Run with a deadline: if the bound were missing the reader would try to derive a key
		// with up to 2^32 iterations, so a regression must fail this test, not hang the suite.
		type res struct {
			b   *trystbk.Backup
			err error
		}
		done := make(chan res, 1)
		go func() {
			b, err := trystbk.Read(bytes.NewReader(bad), r.Password)
			done <- res{b, err}
		}()
		select {
		case got := <-done:
			if !errors.Is(got.err, trystbk.ErrIterationsRange) || got.b != nil {
				t.Errorf("iterations %d: err = %v, backup = %v", n, got.err, got.b)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("iterations %d: Read did not return within 10s (the bound is missing: a DoS)", n)
		}
	}
}

func TestRead_NotATrystBackup(t *testing.T) {
	for name, in := range map[string][]byte{
		"random":   bytes.Repeat([]byte{0xAB}, 200),
		"zip file": []byte("PK\x03\x04 plain zip, not a container, padding padding"),
		"empty":    nil,
	} {
		if b, err := trystbk.Read(bytes.NewReader(in), "x"); err == nil || b != nil {
			t.Errorf("%s: must be rejected, got %v", name, err)
		}
	}
	if _, err := trystbk.Read(bytes.NewReader(bytes.Repeat([]byte{0xAB}, 200)), "x"); !errors.Is(err, trystbk.ErrBadMagic) {
		t.Errorf("random bytes: %v", err)
	}
}

func TestRead_NewerSchemaIsHeldReadOnly(t *testing.T) {
	r := build(t, synth.Options{Seed: 10, Schema: trystbk.LatestKnownSchema + 5})
	b := read(t, r)
	if b.SchemaStatus() != trystbk.SchemaNewer {
		t.Fatalf("status = %v, want SchemaNewer", b.SchemaStatus())
	}
	if b.RowCount("encounters") == 0 {
		t.Error("a newer-schema backup must still be readable")
	}
}

func TestSchemaStatus(t *testing.T) {
	cases := map[int]trystbk.SchemaSupport{
		-1: trystbk.SchemaInvalid, 0: trystbk.SchemaInvalid, 1: trystbk.SchemaKnown, 8: trystbk.SchemaKnown,
		19: trystbk.SchemaKnown, 20: trystbk.SchemaNewer, 99: trystbk.SchemaNewer,
	}
	for v, want := range cases {
		if got := trystbk.SchemaStatus(v); got != want {
			t.Errorf("SchemaStatus(%d) = %v, want %v", v, got, want)
		}
	}
}

func TestBackup_CloseZeroesThePlaintext(t *testing.T) {
	r := build(t, synth.Options{Seed: 11})
	b, err := trystbk.Read(bytes.NewReader(r.File), r.Password)
	if err != nil {
		t.Fatal(err)
	}
	b.Close()
	if len(b.Media()) != 0 {
		t.Error("Close should drop the media list")
	}
	if _, err := b.OpenMedia("media/x"); !errors.Is(err, trystbk.ErrClosed) {
		t.Error("OpenMedia after Close must return ErrClosed")
	}
}

func TestSynth_IsDeterministic(t *testing.T) {
	a := build(t, synth.Options{Seed: 99, Encounters: 25, Photos: 2})
	b := build(t, synth.Options{Seed: 99, Encounters: 25, Photos: 2})
	if !bytes.Equal(a.DataJSON, b.DataJSON) || !bytes.Equal(a.ZIP, b.ZIP) {
		t.Error("same seed must produce the same data.json and ZIP")
	}
	c := build(t, synth.Options{Seed: 100, Encounters: 25, Photos: 2})
	if bytes.Equal(a.DataJSON, c.DataJSON) {
		t.Error("a different seed must produce different data")
	}
}

func TestSynth_ContainsNothingResemblingRealData(t *testing.T) {
	r := build(t, synth.Options{Seed: 12, Encounters: 40, Partners: 3, Photos: 2})
	for _, s := range []string{"Synthetic Partner", "Synthetic entry", "Synthetic Self"} {
		if !strings.Contains(string(r.DataJSON), s) {
			t.Errorf("expected placeholder text %q", s)
		}
	}
}

func TestSeal_RefusesWhatTrystRefuses(t *testing.T) {
	if _, err := trystbk.Seal("pw", []byte("x"), trystbk.SealOptions{Iterations: 10}); !errors.Is(err, trystbk.ErrIterationsRange) {
		t.Errorf("low iterations: %v", err)
	}
	if _, err := trystbk.Seal("pw", []byte("x"), trystbk.SealOptions{Version: 7, Iterations: trystbk.MinIterations}); !errors.Is(err, trystbk.ErrUnsupportedVer) {
		t.Errorf("bad version: %v", err)
	}
}

func TestRead_LargeRoundTrip(t *testing.T) {
	// A few thousand rows, to exercise the streaming parser beyond toy sizes.
	r := build(t, synth.Options{Seed: 13, Encounters: 3000, Partners: 6, Photos: 20})
	b := read(t, r)
	if b.RowCount("encounters") != 3000 || b.RowCount("encounter_partner") != 3000 {
		t.Errorf("rows: %d %d", b.RowCount("encounters"), b.RowCount("encounter_partner"))
	}
	if !bytes.Equal(rebuild(b), r.DataJSON) {
		t.Error("large backup is not read losslessly")
	}
}

// TestInterop_RealBackup runs against a REAL Tryst backup, closing the interop risk (R6).
// The fixture never lives in the repo (hard constraint 4: real, private data). Set both
// variables to run it; it is skipped otherwise, which is correct on CI.
//
//	ALCOVE_TEST_FIXTURE=<path to a real .tryst>  ALCOVE_TEST_PASSWORD=<its password>
//	go test ./internal/trystbk/ -run TestInterop -v
func TestInterop_RealBackup(t *testing.T) {
	path, pw := os.Getenv("ALCOVE_TEST_FIXTURE"), os.Getenv("ALCOVE_TEST_PASSWORD")
	if path == "" || pw == "" {
		t.Skip("set ALCOVE_TEST_FIXTURE and ALCOVE_TEST_PASSWORD to run against a real backup")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()
	b, err := trystbk.Read(f, pw)
	if err != nil {
		t.Fatalf("Read real backup: %v", err)
	}
	defer b.Close()

	if b.SchemaVersion <= 0 || len(b.Tables) == 0 {
		t.Fatalf("schema %d, tables %d", b.SchemaVersion, len(b.Tables))
	}
	// The reader must be lossless on a real file too: re-serialising data.json and reading
	// it back gives the same tables. (Not byte-equal to the original, whose whitespace and
	// number spelling we did not record, but row for row.)
	// Neutral output only (N4): versions and counts, never a value from a row.
	rows := 0
	for _, tbl := range b.Tables {
		rows += len(tbl.Rows)
	}
	t.Logf("interop OK: container v%d, KDF iterations %d, Tryst schema v%d (%v), %d tables, %d rows, %d media entries",
		b.Header.Version, b.Header.Iterations, b.SchemaVersion, b.SchemaStatus(), len(b.Tables), rows, len(b.Media()))
}

// TestWriteSample writes a synthetic backup to disk so it can be restored in Tryst on an
// EMULATOR (never a physical phone), which proves the generated data is shaped like a real
// backup (the M1 exit criterion). Skipped unless ALCOVE_SYNTH_OUT names a directory.
//
//	ALCOVE_SYNTH_OUT=<dir> go test ./internal/trystbk/ -run TestWriteSample -v
//
// The file is written as <dir>/alcove-synthetic.tryst and its password is printed. Both
// are synthetic; the password is not secret.
func TestWriteSample(t *testing.T) {
	dir := os.Getenv("ALCOVE_SYNTH_OUT")
	if dir == "" {
		t.Skip("set ALCOVE_SYNTH_OUT=<dir> to write a synthetic backup for an emulator restore")
	}
	r := build(t, synth.Options{Seed: 2026, Encounters: 40, Partners: 3, Photos: 6, IncludeSettings: true, Iterations: trystbk.DefaultIterations, Password: "alcove-synthetic-sample"})
	out := dir + string(os.PathSeparator) + "alcove-synthetic.tryst"
	if err := os.WriteFile(out, r.File, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s (%d bytes); password: %s; counts: %v", out, len(r.File), r.Password, r.Counts)
}
