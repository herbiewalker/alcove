package store

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/herbiewalker/alcove/internal/blobstore"
)

// Hard constraint 4 / requirement N16: decrypted user data exists only in memory or tmpfs, never
// on the data volume. `scripts/guards.sh no-plaintext` runs TestGuard_NoPlaintext.
//
// The test stores sentinel strings through every path that persists something (database rows,
// the search index, a data key, an upload, a photo), seals, closes, and then scans every file in
// the data directory for the sentinels and for the signatures of readable formats (a SQLite file,
// a ZIP, JSON). Only manifest.json is allowed to be readable: it names the store and holds no data.

const sentinel = "ALCOVE-SENTINEL-7f3a9c-do-not-persist"

var plaintextSignatures = [][]byte{
	[]byte(sentinel),
	[]byte("SQLite format 3"),
	[]byte("PK\x03\x04"),   // ZIP
	[]byte("\xff\xd8\xff"), // JPEG
}

// findPlaintext returns the files under dir that look like readable user data. allow lists base
// names that may legitimately be readable.
func findPlaintext(t *testing.T, dir string, allow map[string]bool) []string {
	t.Helper()
	var bad []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || allow[d.Name()] {
			return err
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		for _, sig := range plaintextSignatures {
			if bytes.Contains(b, sig) {
				bad = append(bad, p+" contains "+strings.TrimSpace(string(sig[:min(len(sig), 12)])))
				return nil
			}
		}
		if trimmed := bytes.TrimSpace(b); len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
			bad = append(bad, p+" looks like JSON")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	return bad
}

func TestGuard_NoPlaintext(t *testing.T) {
	d, k := newDirs(t), newKey(t)
	s := open(t, d, k)
	ctx := context.Background()

	if err := s.SetMeta("note", sentinel); err != nil {
		t.Fatal(err)
	}
	if err := s.IndexText(ctx, "alice", "note", "e1", "entry text "+sentinel); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateDataKey("alice"); err != nil {
		t.Fatal(err)
	}
	dek, err := s.DataKey("alice")
	if err != nil {
		t.Fatal(err)
	}
	blobs := blobstore.New(d.data)
	// An upload is kept verbatim, but a real one is already ciphertext; the sentinel stands in
	// for the plaintext that must never be here, so store opaque bytes for it.
	if _, _, err := blobs.PutUpload("alice", "u1", bytes.NewReader([]byte("TRYSTBK1-opaque-ciphertext-stand-in")), 0); err != nil {
		t.Fatal(err)
	}
	photo := append([]byte("\xff\xd8\xff\xe0 JFIF "), []byte(sentinel)...)
	if _, err := blobs.PutMedia("alice", "m1", dek.Bytes(), bytes.NewReader(photo), 0); err != nil {
		t.Fatal(err)
	}
	if err := s.SealNow(true); err != nil {
		t.Fatal(err)
	}
	// Scan while open and again after Close: both must be clean.
	allow := map[string]bool{"manifest.json": true}
	if bad := findPlaintext(t, d.data, allow); len(bad) > 0 {
		t.Fatalf("plaintext on the data volume while open:\n  %s", strings.Join(bad, "\n  "))
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if bad := findPlaintext(t, d.data, allow); len(bad) > 0 {
		t.Fatalf("plaintext on the data volume after Close:\n  %s", strings.Join(bad, "\n  "))
	}
	// Sanity: the scan really sees files, so an empty result means something.
	n := 0
	_ = filepath.WalkDir(d.data, func(p string, de fs.DirEntry, err error) error {
		if err == nil && !de.IsDir() {
			n++
		}
		return nil
	})
	if n < 4 {
		t.Fatalf("expected the sealed database, its manifest and two blobs, found %d files", n)
	}
}

// The scanner itself must catch what it claims to, or a clean result proves nothing (principle 12).
func TestGuard_ScannerCatchesPlaintext(t *testing.T) {
	cases := map[string][]byte{
		"sentinel.txt": []byte("hello " + sentinel),
		"live.sqlite":  []byte("SQLite format 3\x00 rest of a database"),
		"archive.zip":  []byte("PK\x03\x04 zip bytes"),
		"photo.jpg":    []byte("\xff\xd8\xff\xe0 jpeg"),
		"export.json":  []byte(`{"encounters":[]}`),
		"rows.json":    []byte(`  [{"a":1}]`),
	}
	for name, content := range cases {
		dir := t.TempDir()
		_ = os.WriteFile(filepath.Join(dir, name), content, 0o600)
		if bad := findPlaintext(t, dir, map[string]bool{"manifest.json": true}); len(bad) == 0 {
			t.Errorf("scanner missed %s", name)
		}
	}
	// An allowed file is skipped, and ciphertext-looking bytes pass.
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"store_id":"x"}`), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "blob"), bytes.Repeat([]byte{0x9c, 0x11, 0xe7}, 100), 0o600)
	if bad := findPlaintext(t, dir, map[string]bool{"manifest.json": true}); len(bad) != 0 {
		t.Errorf("false positive: %v", bad)
	}
}
