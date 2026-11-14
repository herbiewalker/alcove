package ingest

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/herbiewalker/alcove/internal/blobstore"
	"github.com/herbiewalker/alcove/internal/keyfile"
	"github.com/herbiewalker/alcove/internal/store"
	"github.com/herbiewalker/alcove/internal/trystbk"
	"github.com/herbiewalker/alcove/internal/trystbk/synth"
)

const owner = "ualice"

type rig struct {
	t    *testing.T
	st   *store.Store
	svc  *Service
	data string
	tmp  string
	key  keyfile.Key
}

func newRig(t *testing.T) *rig {
	t.Helper()
	k, _, err := keyfile.Generate()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	r := &rig{t: t, data: filepath.Join(root, "data"), tmp: filepath.Join(root, "tmp"), key: k}
	st, err := store.Open(store.Config{DataDir: r.data, TmpDir: r.tmp, Key: k, AllowNonTmpfsTmp: true, SealEvery: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	r.st = st
	r.svc = New(st, blobstore.New(r.data))
	return r
}

func build(t *testing.T, o synth.Options) *synth.Result {
	t.Helper()
	res, err := synth.Build(o)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func (r *rig) upload(res *synth.Result, remember bool) (UploadInfo, error) {
	return r.svc.Upload(context.Background(), Params{Owner: owner, CreatedBy: owner, Filename: "tryst-2026.tryst",
		Password: res.Password, Remember: remember}, bytes.NewReader(res.File))
}

func (r *rig) count(q string, args ...any) int {
	r.t.Helper()
	var n int
	if err := r.st.View(context.Background(), func(tx *sql.Tx) error { return tx.QueryRow(q, args...).Scan(&n) }); err != nil {
		r.t.Fatal(err)
	}
	return n
}

func (r *rig) blobFiles() []string {
	var out []string
	_ = filepath.WalkDir(filepath.Join(r.data, "blobs"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// nothingRecorded asserts a refused upload left no trace (N7): no rows, no blob, no password.
func (r *rig) nothingRecorded() {
	r.t.Helper()
	for _, tbl := range []string{"upload", "revision", "cache_row", "cache_head", "backup_password"} {
		if n := r.count("SELECT COUNT(*) FROM " + tbl); n != 0 {
			r.t.Errorf("a refused upload left %d row(s) in %s", n, tbl)
		}
	}
	if n := r.count("SELECT COUNT(*) FROM search"); n != 0 {
		r.t.Errorf("a refused upload left %d search row(s)", n)
	}
	if f := r.blobFiles(); len(f) != 0 {
		r.t.Errorf("a refused upload left blobs: %v", f)
	}
}

func TestUploadRecordsARevisionAndTheCache(t *testing.T) {
	r := newRig(t)
	res := build(t, synth.Options{Seed: 1, Encounters: 12, Partners: 3, Photos: 2})
	info, err := r.upload(res, true)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if info.Encounters != 12 || info.Media != 2 || info.TrystSchema != trystbk.LatestKnownSchema || info.SchemaStatus != trystbk.SchemaKnown || !info.Head {
		t.Fatalf("info: %+v", info)
	}
	if info.SHA256 == "" || info.Size != int64(len(res.File)) || info.Filename != "tryst-2026.tryst" {
		t.Fatalf("info: %+v", info)
	}
	// The stored upload is the file, byte for byte (D-7).
	files := r.blobFiles()
	if len(files) != 1 {
		t.Fatalf("blobs: %v", files)
	}
	if got, _ := os.ReadFile(files[0]); !bytes.Equal(got, res.File) {
		t.Fatal("the upload was not stored verbatim")
	}
	// Revision chain, cache and search.
	if r.count("SELECT COUNT(*) FROM revision WHERE kind = 'upload' AND parent_id IS NULL") != 1 {
		t.Error("the first revision must have no parent")
	}
	rows, err := r.svc.CacheRows(context.Background(), owner, "encounters")
	if err != nil || len(rows) != 12 {
		t.Fatalf("cached encounters: %d %v", len(rows), err)
	}
	joined := strings.Join(rows, "\n")
	if !strings.HasPrefix(rows[0], `{"id":"`) || strings.Index(rows[0], `"startAt"`) > strings.Index(rows[0], `"note"`) || !strings.Contains(joined, `"note":"Synthetic entry `) {
		t.Errorf("row not cached as ordered raw JSON: %s", rows[0])
	}
	if n := r.count("SELECT COUNT(*) FROM cache_row WHERE owner_id = ?", owner); n != info.Rows {
		t.Errorf("cache holds %d rows, the backup has %d", n, info.Rows)
	}
	hits, err := r.st.Search(context.Background(), []string{owner}, "entry 3", 10)
	if err != nil || len(hits) == 0 {
		t.Fatalf("search: %v %v", hits, err)
	}
	if hits, _ := r.st.Search(context.Background(), []string{"ubob"}, "entry", 10); len(hits) != 0 {
		t.Error("another owner's search must not see these rows")
	}
	if !r.svc.HasStoredPassword(context.Background(), owner) {
		t.Error("the password was not remembered")
	}
	if pw, err := r.svc.StoredPassword(context.Background(), owner); err != nil || pw != res.Password {
		t.Errorf("stored password: %q %v", pw, err)
	}
}

func TestRefusalsRecordNothing(t *testing.T) {
	r := newRig(t)
	good := build(t, synth.Options{Seed: 2})

	t.Run("wrong password", func(t *testing.T) {
		_, err := r.svc.Upload(context.Background(), Params{Owner: owner, CreatedBy: owner, Filename: "x", Password: "not the password", Remember: true}, bytes.NewReader(good.File))
		if !errors.Is(err, ErrWrongPassword) {
			t.Fatalf("got %v", err)
		}
		r.nothingRecorded()
	})
	t.Run("not a backup", func(t *testing.T) {
		for name, data := range map[string][]byte{
			"text":      []byte("hello, this is not a backup at all, just some text of reasonable length"),
			"empty":     {},
			"truncated": good.File[:10],
			"zip":       {'P', 'K', 3, 4, 0, 0, 0, 0},
		} {
			_, err := r.svc.Upload(context.Background(), Params{Owner: owner, CreatedBy: owner, Filename: name, Password: good.Password, Remember: true}, bytes.NewReader(data))
			if !errors.Is(err, ErrNotBackup) {
				t.Errorf("%s: got %v", name, err)
			}
		}
		r.nothingRecorded()
	})
	t.Run("hostile header", func(t *testing.T) {
		evil := append([]byte(nil), good.File...)
		for i := 25; i < 29; i++ {
			evil[i] = 0xff // an absurd PBKDF2 count: a denial-of-service attempt
		}
		_, err := r.svc.Upload(context.Background(), Params{Owner: owner, CreatedBy: owner, Filename: "x", Password: good.Password, Remember: true}, bytes.NewReader(evil))
		if !errors.Is(err, ErrDamaged) || !strings.Contains(err.Error(), "iterations") {
			t.Fatalf("got %v", err)
		}
		r.nothingRecorded()
	})
	t.Run("tampered stream", func(t *testing.T) {
		bad := append([]byte(nil), good.File...)
		bad[len(bad)/2] ^= 0xff
		_, err := r.svc.Upload(context.Background(), Params{Owner: owner, CreatedBy: owner, Filename: "x", Password: good.Password, Remember: true}, bytes.NewReader(bad))
		if err == nil {
			t.Fatal("a tampered backup was accepted")
		}
		r.nothingRecorded()
	})
	t.Run("invalid schema", func(t *testing.T) {
		zero := build(t, synth.Options{Seed: 3, Schema: -1})
		_, err := r.upload(zero, true)
		if err == nil {
			t.Fatal("schema below 1 accepted")
		}
		r.nothingRecorded()
	})
	t.Run("oversize", func(t *testing.T) {
		small := New(r.st, blobstore.New(r.data))
		small.MaxBytes = 1024
		_, err := small.Upload(context.Background(), Params{Owner: owner, CreatedBy: owner, Filename: "x", Password: good.Password}, bytes.NewReader(good.File))
		if !errors.Is(err, ErrTooLarge) {
			t.Fatalf("got %v", err)
		}
		r.nothingRecorded()
	})
	t.Run("bad owner id", func(t *testing.T) {
		_, err := r.svc.Upload(context.Background(), Params{Owner: "../evil", CreatedBy: "x", Password: good.Password}, bytes.NewReader(good.File))
		if err == nil {
			t.Fatal("a path-like owner id was accepted")
		}
	})
	// And after all that, the good file still uploads: nothing was left in the way.
	if _, err := r.upload(good, true); err != nil {
		t.Fatalf("the good upload after the refusals: %v", err)
	}
}

func TestDuplicateIsRefusedAndTheNewCopyRemoved(t *testing.T) {
	r := newRig(t)
	res := build(t, synth.Options{Seed: 4})
	first, err := r.upload(res, true)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.upload(res, true)
	var dup *DuplicateError
	if !errors.As(err, &dup) || dup.ExistingID != first.ID {
		t.Fatalf("second upload: %v", err)
	}
	if n := r.count("SELECT COUNT(*) FROM upload"); n != 1 {
		t.Errorf("uploads = %d", n)
	}
	if f := r.blobFiles(); len(f) != 1 {
		t.Errorf("the refused copy was kept: %v", f)
	}
	// The same bytes under another owner are not a duplicate (each person has their own history).
	other, err := r.svc.Upload(context.Background(), Params{Owner: "ubob", CreatedBy: "ubob", Filename: "x", Password: res.Password}, bytes.NewReader(res.File))
	if err != nil || other.SHA256 != first.SHA256 {
		t.Fatalf("another owner: %v", err)
	}
}

func TestConcurrentSameFileUploadsRecordOne(t *testing.T) {
	r := newRig(t)
	res := build(t, synth.Options{Seed: 5})
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, dups := 0, 0
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.upload(res, true)
			mu.Lock()
			defer mu.Unlock()
			var d *DuplicateError
			switch {
			case err == nil:
				ok++
			case errors.As(err, &d):
				dups++
			default:
				t.Errorf("unexpected: %v", err)
			}
		}()
	}
	wg.Wait()
	if ok != 1 || dups != 3 {
		t.Fatalf("ok=%d dup=%d", ok, dups)
	}
	if n := r.count("SELECT COUNT(*) FROM upload"); n != 1 {
		t.Errorf("uploads = %d", n)
	}
	if f := r.blobFiles(); len(f) != 1 {
		t.Errorf("blobs = %v", f)
	}
}

func TestSecondUploadIsANewRevisionAndReplacesTheCache(t *testing.T) {
	r := newRig(t)
	a, _ := r.upload(build(t, synth.Options{Seed: 6, Encounters: 5}), true)
	time.Sleep(1100 * time.Millisecond) // revision order is by second
	b, err := r.upload(build(t, synth.Options{Seed: 7, Encounters: 9}), true)
	if err != nil {
		t.Fatal(err)
	}
	list, err := r.svc.List(context.Background(), owner)
	if err != nil || len(list) != 2 || list[0].ID != b.ID || !list[0].Head || list[1].ID != a.ID || list[1].Head {
		t.Fatalf("list: %+v %v", list, err)
	}
	var parent string
	_ = r.st.View(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT parent_id FROM revision WHERE id = ?`, b.RevisionID).Scan(&parent)
	})
	if parent != a.RevisionID {
		t.Errorf("the second revision's parent is %q, want %q", parent, a.RevisionID)
	}
	if rows, _ := r.svc.CacheRows(context.Background(), owner, "encounters"); len(rows) != 9 {
		t.Errorf("the cache must hold the head revision (9), got %d", len(rows))
	}
	if n := r.count("SELECT COUNT(*) FROM search WHERE kind = 'encounter'"); n != 9 {
		t.Errorf("search rows = %d, want 9 (the old revision's rows must be gone)", n)
	}
	if len(r.blobFiles()) != 2 {
		t.Error("both uploads must be kept")
	}
}

func TestNewerSchemaIsKeptReadOnly(t *testing.T) {
	r := newRig(t)
	info, err := r.upload(build(t, synth.Options{Seed: 8, Schema: trystbk.LatestKnownSchema + 3}), true)
	if err != nil {
		t.Fatalf("a newer schema should be stored: %v", err)
	}
	if info.SchemaStatus != trystbk.SchemaNewer || info.TrystSchema != trystbk.LatestKnownSchema+3 {
		t.Fatalf("%+v", info)
	}
	if rows, _ := r.svc.CacheRows(context.Background(), owner, "encounters"); len(rows) == 0 {
		t.Error("a newer schema should still be readable")
	}
}

func TestRememberAndForgetPassword(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	a := build(t, synth.Options{Seed: 9})
	if _, err := r.upload(a, false); err != nil {
		t.Fatal(err)
	}
	if r.svc.HasStoredPassword(ctx, owner) {
		t.Fatal("the password was stored although the person asked not to")
	}
	if _, err := r.svc.StoredPassword(ctx, owner); !errors.Is(err, ErrNoPassword) {
		t.Fatalf("StoredPassword: %v", err)
	}
	if err := r.svc.Rebuild(ctx, owner); !errors.Is(err, ErrNoPassword) {
		t.Fatalf("rebuild without a stored password: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err := r.upload(build(t, synth.Options{Seed: 10, Password: "second password!"}), true); err != nil {
		t.Fatal(err)
	}
	if pw, _ := r.svc.StoredPassword(ctx, owner); pw != "second password!" {
		t.Fatalf("stored: %q", pw)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err := r.upload(build(t, synth.Options{Seed: 11}), false); err != nil {
		t.Fatal(err)
	}
	if r.svc.HasStoredPassword(ctx, owner) {
		t.Fatal("unticking 'remember' must also forget the old password")
	}
}

func TestStoredPasswordIsSealedUnderTheOwnersKey(t *testing.T) {
	r := newRig(t)
	res := build(t, synth.Options{Seed: 12, Password: "Sentinel-Backup-Password-42"})
	if _, err := r.upload(res, true); err != nil {
		t.Fatal(err)
	}
	var sealed []byte
	_ = r.st.View(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT sealed FROM backup_password WHERE owner_id = ?`, owner).Scan(&sealed)
	})
	if len(sealed) < 28 || bytes.Contains(sealed, []byte("Sentinel")) {
		t.Fatal("the password is not sealed")
	}
	// Moved to another owner's row it cannot be opened (the owner id is bound in).
	if err := r.st.CreateDataKey("ubob"); err != nil {
		t.Fatal(err)
	}
	_ = r.st.Update(context.Background(), true, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO backup_password(owner_id, sealed, updated_at) VALUES ('ubob', ?, 0)`, sealed)
		return err
	})
	if _, err := r.svc.StoredPassword(context.Background(), "ubob"); err == nil {
		t.Fatal("a sealed password opened under another owner")
	}
}

func TestRebuildRestoresADamagedCache(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.upload(build(t, synth.Options{Seed: 13, Encounters: 20}), true); err != nil {
		t.Fatal(err)
	}
	before, _ := r.svc.CacheRows(ctx, owner, "encounters")
	nBefore := r.count("SELECT COUNT(*) FROM cache_row")
	searchBefore := r.count("SELECT COUNT(*) FROM search")

	// Wreck the derived data: delete some rows, corrupt one, drop the search rows.
	if err := r.st.Update(ctx, true, func(tx *sql.Tx) error {
		for _, q := range []string{
			`DELETE FROM cache_row WHERE tbl = 'partners'`,
			`UPDATE cache_row SET data = '{"corrupt":true}' WHERE tbl = 'encounters' AND pos = 3`,
			`DELETE FROM search`,
			`DELETE FROM cache_head`,
		} {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.Rebuild(ctx, owner); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	after, _ := r.svc.CacheRows(ctx, owner, "encounters")
	if len(after) != len(before) {
		t.Fatalf("rows %d -> %d", len(before), len(after))
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("row %d differs after rebuild:\n%s\n%s", i, before[i], after[i])
		}
	}
	if r.count("SELECT COUNT(*) FROM cache_row") != nBefore || r.count("SELECT COUNT(*) FROM search") != searchBefore || r.count("SELECT COUNT(*) FROM cache_head") != 1 {
		t.Error("the rebuilt cache does not match the original")
	}
	if done, failed := r.svc.RebuildAll(ctx); done != 1 || len(failed) != 0 {
		t.Fatalf("RebuildAll: %d %v", done, failed)
	}
	// A rebuild detects a tampered stored upload (its SHA-256 no longer matches).
	files := r.blobFiles()
	_ = os.Chmod(files[0], 0o600)
	raw, _ := os.ReadFile(files[0])
	raw[len(raw)/2] ^= 1
	_ = os.WriteFile(files[0], raw, 0o600)
	if err := r.svc.Rebuild(ctx, owner); err == nil {
		t.Fatal("a tampered stored upload was rebuilt from (N6)")
	}
}

func TestRowsWithDuplicateOrMissingIDsAreAllCached(t *testing.T) {
	r := newRig(t)
	res := build(t, synth.Options{Seed: 14, Encounters: 4})
	// encounter_partner has no id column: every row must still be cached, by position.
	if _, err := r.upload(res, true); err != nil {
		t.Fatal(err)
	}
	want := res.Counts["encounter_partner"]
	if got := r.count("SELECT COUNT(*) FROM cache_row WHERE tbl = 'encounter_partner'"); got != want || want == 0 {
		t.Fatalf("link rows cached = %d, want %d", got, want)
	}
}

func TestCleanFilename(t *testing.T) {
	for in, want := range map[string]string{
		"my backup.tryst":              "my backup.tryst",
		`..\..\Windows\system32\x.bak`: "x.bak",
		"../../etc/passwd":             "passwd",
		"a\x00b\nc.tryst":              "abc.tryst",
		"<script>x.tryst":              "scriptx.tryst",
		"":                             "backup.tryst",
		"..":                           "backup.tryst",
		"   ":                          "backup.tryst",
		strings.Repeat("é", 300):       strings.Repeat("é", 120),
	} {
		if got := CleanFilename(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

// N16 / constraint 4: after ingesting a backup, nothing readable is on the data volume.
func TestIngestLeavesNoPlaintext(t *testing.T) {
	r := newRig(t)
	res := build(t, synth.Options{Seed: 15, Encounters: 10, Photos: 1, Password: "Plaintext-Sentinel-Password-7"})
	if _, err := r.upload(res, true); err != nil {
		t.Fatal(err)
	}
	if err := r.st.SealNow(true); err != nil {
		t.Fatal(err)
	}
	needles := []string{"Synthetic entry", "Synthetic Partner", "Synthetic Self", "Plaintext-Sentinel-Password-7",
		"SQLite format 3", `"encounters"`, "schemaVersion", "data.json"}
	err := filepath.WalkDir(r.data, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, _ := os.ReadFile(p)
		for _, n := range needles {
			if bytes.Contains(raw, []byte(n)) {
				t.Errorf("%s contains %q in the clear", strings.TrimPrefix(p, r.data), n)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
