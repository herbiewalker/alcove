package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/herbiewalker/alcove/internal/keyfile"
	"github.com/herbiewalker/alcove/internal/seal"
)

type dirs struct{ data, tmp string }

func newDirs(t *testing.T) dirs {
	t.Helper()
	root := t.TempDir()
	return dirs{data: filepath.Join(root, "data"), tmp: filepath.Join(root, "tmp")}
}

func newKey(t *testing.T) keyfile.Key {
	t.Helper()
	k, _, err := keyfile.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func cfg(d dirs, k keyfile.Key) Config {
	return Config{DataDir: d.data, TmpDir: d.tmp, Key: k, AllowNonTmpfsTmp: true, SealEvery: time.Hour}
}

func open(t *testing.T, d dirs, k keyfile.Key) *Store {
	t.Helper()
	s, err := Open(cfg(d, k))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func TestFirstRunCreatesManifestAndSeal(t *testing.T) {
	d, k := newDirs(t), newKey(t)
	s := open(t, d, k)
	defer s.Close()
	if !seal.Exists(d.data) {
		t.Fatal("a brand-new store must be sealed at once")
	}
	if _, err := os.Stat(filepath.Join(d.data, "manifest.json")); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	v, err := s.SchemaVersionInDB()
	if err != nil || v != SchemaVersion {
		t.Fatalf("schema = %d %v", v, err)
	}
}

func TestDataSurvivesRestartWithNoHumanStep(t *testing.T) {
	d, k := newDirs(t), newKey(t)
	s := open(t, d, k)
	if err := s.SetMeta("hello", "world"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// "Restart": a new process opens the same directories with the same key file, nothing typed.
	s2 := open(t, d, k)
	defer s2.Close()
	if v, ok, err := s2.GetMeta("hello"); err != nil || !ok || v != "world" {
		t.Fatalf("after restart: %q %v %v", v, ok, err)
	}
}

func TestWrongKeyFailsCleanlyAndIsNotAFreshStore(t *testing.T) {
	d, k := newDirs(t), newKey(t)
	s := open(t, d, k)
	_ = s.SetMeta("x", "1")
	_ = s.Close()
	before, _ := os.ReadFile(filepath.Join(d.data, seal.CurrentName))

	_, err := Open(cfg(d, newKey(t)))
	if !errors.Is(err, seal.ErrAuth) {
		t.Fatalf("err = %v, want seal.ErrAuth", err)
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatal("a wrong key must never look like a first run")
	}
	after, _ := os.ReadFile(filepath.Join(d.data, seal.CurrentName))
	if string(before) != string(after) {
		t.Fatal("a failed open changed the sealed data")
	}
	// The right key still works afterwards.
	s2 := open(t, d, k)
	defer s2.Close()
	if v, _, _ := s2.GetMeta("x"); v != "1" {
		t.Fatal("data lost after a wrong-key attempt")
	}
}

func TestTamperedOrMissingManifest(t *testing.T) {
	d, k := newDirs(t), newKey(t)
	s := open(t, d, k)
	_ = s.Close()
	mp := filepath.Join(d.data, "manifest.json")
	orig, _ := os.ReadFile(mp)

	_ = os.WriteFile(mp, []byte(strings.Replace(string(orig), `"store_id": "`, `"store_id": "ff`, 1)), 0o600)
	if _, err := Open(cfg(d, k)); !errors.Is(err, seal.ErrManifestOutOfBand) {
		t.Fatalf("edited manifest: %v", err)
	}
	_ = os.Remove(mp)
	if _, err := Open(cfg(d, k)); !errors.Is(err, ErrManifestMissing) {
		t.Fatalf("missing manifest: %v", err)
	}
	_ = os.WriteFile(mp, orig, 0o600)
	s2 := open(t, d, k)
	_ = s2.Close()
}

func TestRefusesTmpOnDiskAndTmpInsideData(t *testing.T) {
	d, k := newDirs(t), newKey(t)
	if ok, _ := isTmpfsBacked(d.tmp); !ok {
		c := cfg(d, k)
		c.AllowNonTmpfsTmp = false
		if _, err := Open(c); !errors.Is(err, ErrTmpNotTmpfs) {
			t.Fatalf("a disk-backed TmpDir must be refused: %v", err)
		}
	}
	c := cfg(d, k)
	c.TmpDir = filepath.Join(d.data, "scratch")
	if _, err := Open(c); !errors.Is(err, ErrTmpInsideData) {
		t.Fatalf("TmpDir inside DataDir: %v", err)
	}
	if _, err := Open(Config{DataDir: d.data, TmpDir: d.tmp, AllowNonTmpfsTmp: true}); err == nil {
		t.Fatal("a zero key must be refused")
	}
}

func TestDurableWriteSurvivesACrashButLazyWriteMayNot(t *testing.T) {
	d, k := newDirs(t), newKey(t)
	s := open(t, d, k)
	// Never closed: this simulates a crash. Another process opens the same data directory.
	ctx := context.Background()
	if err := s.Update(ctx, true, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO meta(key, value) VALUES ('durable', 'yes')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(ctx, false, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO meta(key, value) VALUES ('lazy', 'maybe')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	d2 := dirs{data: d.data, tmp: filepath.Join(filepath.Dir(d.tmp), "tmp2")}
	s2 := open(t, d2, k)
	if v, ok, _ := s2.GetMeta("durable"); !ok || v != "yes" {
		t.Error("a durable write must be in the sealed copy")
	}
	if _, ok, _ := s2.GetMeta("lazy"); ok {
		t.Error("a non-durable write should not be sealed yet (it waits for the timer)")
	}
	_ = s2.Close()
	_ = s.Close()
}

func TestBackgroundSealerSealsChangedData(t *testing.T) {
	d, k := newDirs(t), newKey(t)
	c := cfg(d, k)
	c.SealEvery = 40 * time.Millisecond
	s, err := Open(c)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first := s.LastSealed()
	_ = s.SetMeta("a", "b")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s.LastSealed().After(first) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the background sealer never sealed the change")
}

func TestCloseRemovesLiveFilesAndRefusesFurtherUse(t *testing.T) {
	d, k := newDirs(t), newKey(t)
	s := open(t, d, k)
	if _, err := os.Stat(filepath.Join(d.tmp, "alcove.db")); err != nil {
		t.Fatalf("live database should exist while open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(d.tmp)
	if len(entries) != 0 {
		t.Fatalf("plaintext left on tmpfs after Close: %v", entries)
	}
	if err := s.Update(context.Background(), false, func(*sql.Tx) error { return nil }); !errors.Is(err, ErrClosed) {
		t.Fatalf("use after Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestCrashLeftoverPlaintextIsDiscardedOnOpen(t *testing.T) {
	d, k := newDirs(t), newKey(t)
	s := open(t, d, k)
	_ = s.Close()
	_ = os.MkdirAll(d.tmp, 0o700)
	_ = os.WriteFile(filepath.Join(d.tmp, "alcove.db"), []byte("SQLite format 3 stale plaintext"), 0o600)
	s2 := open(t, d, k)
	defer s2.Close()
	if v, err := s2.SchemaVersionInDB(); err != nil || v != SchemaVersion {
		t.Fatalf("a stale live file must not be used: %v", err)
	}
}

func TestSearch(t *testing.T) {
	d, k := newDirs(t), newKey(t)
	s := open(t, d, k)
	defer s.Close()
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.IndexText(ctx, "alice", "note", "e1", "Dinner by the lake and a long walk"))
	must(s.IndexText(ctx, "alice", "note", "e2", "Quiet evening at home"))
	must(s.IndexText(ctx, "bob", "note", "e3", "Lake house weekend"))

	hits, err := s.Search(ctx, nil, "lake", 10)
	if err != nil || len(hits) != 2 {
		t.Fatalf("admin search = %v %v", hits, err)
	}
	hits, _ = s.Search(ctx, []string{"alice"}, "lake", 10)
	if len(hits) != 1 || hits[0].OwnerID != "alice" || hits[0].Ref != "e1" {
		t.Fatalf("scoped search leaked or missed: %v", hits)
	}
	if hits, _ := s.Search(ctx, []string{}, "lake", 10); len(hits) != 0 {
		t.Fatal("an empty owner scope must return nothing, not everything")
	}
	if hits, _ := s.Search(ctx, []string{"carol"}, "lake", 10); len(hits) != 0 {
		t.Fatal("an owner with nothing indexed must get nothing")
	}
	// Hostile query text is inert: no syntax error, no operator effect.
	for _, q := range []string{`lake OR home`, `"unbalanced`, `NEAR(a b)`, `lake*`, `a:b`, `-lake`, `(`, `)`, "", "   ", `""`} {
		if _, err := s.Search(ctx, nil, q, 10); err != nil {
			t.Errorf("query %q errored: %v", q, err)
		}
	}
	if hits, _ := s.Search(ctx, nil, "lake OR home", 10); len(hits) != 0 {
		t.Error("OR must be matched as a word, not as an operator")
	}
	// Replace, remove.
	must(s.IndexText(ctx, "alice", "note", "e1", "Totally different text"))
	if hits, _ := s.Search(ctx, []string{"alice"}, "lake", 10); len(hits) != 0 {
		t.Error("re-indexing must replace the old text")
	}
	must(s.RemoveText(ctx, "bob", "note", "e3"))
	must(s.RemoveOwnerText(ctx, "alice"))
	if hits, _ := s.Search(ctx, nil, "lake", 10); len(hits) != 0 {
		t.Errorf("after removal: %v", hits)
	}
}

func TestSearchSurvivesRestart(t *testing.T) {
	d, k := newDirs(t), newKey(t)
	s := open(t, d, k)
	_ = s.IndexText(context.Background(), "alice", "note", "e1", "remember the lighthouse")
	_ = s.Close()
	s2 := open(t, d, k)
	defer s2.Close()
	if hits, err := s2.Search(context.Background(), nil, "lighthouse", 5); err != nil || len(hits) != 1 {
		t.Fatalf("search index lost across a restart: %v %v", hits, err)
	}
}

func TestDataKeys(t *testing.T) {
	d, k := newDirs(t), newKey(t)
	s := open(t, d, k)
	if _, err := s.DataKey("alice"); !errors.Is(err, ErrNoDataKey) {
		t.Fatalf("before create: %v", err)
	}
	if err := s.CreateDataKey("alice"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateDataKey("alice"); !errors.Is(err, ErrDataKeyExists) {
		t.Fatalf("second create: %v", err)
	}
	for _, bad := range []string{"", "../x", "a/b", "a b"} {
		if err := s.CreateDataKey(bad); !errors.Is(err, ErrOwnerID) {
			t.Errorf("owner %q: %v", bad, err)
		}
	}
	a, err := s.DataKey("alice")
	if err != nil {
		t.Fatal(err)
	}
	_ = s.CreateDataKey("bob")
	b, _ := s.DataKey("bob")
	if string(a.Bytes()) == string(b.Bytes()) {
		t.Fatal("two owners got the same data key")
	}
	_ = s.Close()

	// The data key survives a restart, and is identical.
	s2 := open(t, d, k)
	defer s2.Close()
	a2, err := s2.DataKey("alice")
	if err != nil || string(a2.Bytes()) != string(a.Bytes()) {
		t.Fatalf("data key changed across restart: %v", err)
	}

	// A wrapped key copied onto another owner's row must not unwrap (it is bound to the owner id).
	if _, err := s2.db.Exec(`INSERT INTO data_key(owner_id, wrapped, created_at) SELECT 'mallory', wrapped, created_at FROM data_key WHERE owner_id = 'alice'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.DataKey("mallory"); err == nil {
		t.Fatal("a wrapped key moved to another owner unwrapped")
	}
}

func TestDestroyDataKeyIsDurable(t *testing.T) {
	d, k := newDirs(t), newKey(t)
	s := open(t, d, k)
	_ = s.CreateDataKey("alice")
	if err := s.DestroyDataKey("alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DataKey("alice"); !errors.Is(err, ErrNoDataKey) {
		t.Fatalf("after destroy: %v", err)
	}
	// Without closing (a crash right after the purge): the sealed copy already has it gone.
	d2 := dirs{data: d.data, tmp: filepath.Join(filepath.Dir(d.tmp), "tmp2")}
	s2 := open(t, d2, k)
	defer s2.Close()
	if ok, _ := s2.HasDataKey("alice"); ok {
		t.Fatal("a destroyed data key reappeared from the sealed copy")
	}
	_ = s.Close()
}

func TestMigrationsAreIdempotent(t *testing.T) {
	d, k := newDirs(t), newKey(t)
	for i := 0; i < 3; i++ {
		s := open(t, d, k)
		if v, _ := s.SchemaVersionInDB(); v != SchemaVersion {
			t.Fatalf("round %d: schema %d", i, v)
		}
		_ = s.Close()
	}
}

// DATA_MODEL migration rule 3: a store written by the previous schema opens, migrates forward, and
// keeps its data.
func TestMigratesAV2StoreToLatest(t *testing.T) {
	d, k := newDirs(t), newKey(t)
	full := migrations
	migrations = full[:2] // behave like the build that wrote schema v2
	s := open(t, d, k)
	if v, _ := s.SchemaVersionInDB(); v != 2 {
		t.Fatalf("setup: schema v%d", v)
	}
	if err := s.Update(context.Background(), true, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO user(id, username, display_name, role, status, pw_hash, created_at) VALUES ('u1','alice','Alice','admin','active','h',1)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	migrations = full // the new build
	s = open(t, d, k)
	defer s.Close()
	if v, _ := s.SchemaVersionInDB(); v != len(full) {
		t.Fatalf("schema v%d after upgrade, want v%d", v, len(full))
	}
	err := s.View(context.Background(), func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM user WHERE username = 'alice'`).Scan(&n); err != nil || n != 1 {
			t.Errorf("the account did not survive the upgrade: %d %v", n, err)
		}
		for _, tbl := range []string{"upload", "revision", "backup_password", "cache_head", "cache_row"} {
			if err := tx.QueryRow(`SELECT COUNT(*) FROM ` + tbl).Scan(&n); err != nil {
				t.Errorf("table %s missing after the upgrade: %v", tbl, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
