package backupcli

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/herbiewalker/alcove/internal/adminlog"
	"github.com/herbiewalker/alcove/internal/backup"
	"github.com/herbiewalker/alcove/internal/keyfile"
	"github.com/herbiewalker/alcove/internal/store"
	"github.com/herbiewalker/alcove/internal/trystbk"
)

type env struct {
	archive, keyPath, tmp string
	key                   keyfile.Key
}

func setup(t *testing.T) env {
	t.Helper()
	root := t.TempDir()
	k, line, err := keyfile.Generate()
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(root, "keys", "alcove.key")
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := keyfile.WriteNew(keyPath, line); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(store.Config{DataDir: filepath.Join(root, "data"), TmpDir: filepath.Join(root, "live"), Key: k, AllowNonTmpfsTmp: true, SealEvery: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Update(context.Background(), true, func(tx *sql.Tx) error {
		_, err := adminlog.Append(tx, time.Now(), "u1", "admin.first_run", "user", "u1", "")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "backups")
	if err := os.MkdirAll(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	info, err := backup.Create(st, dest, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	return env{archive: info.Path, keyPath: keyPath, tmp: filepath.Join(root, "drilltmp"), key: k}
}

func TestVerifyAndRestore(t *testing.T) {
	e := setup(t)
	var out, errb bytes.Buffer
	if code := Verify([]string{"-file", e.archive}, &out, &errb); code != 0 || !strings.Contains(out.String(), "every checksum matches") {
		t.Fatalf("verify: %d %s %s", code, out.String(), errb.String())
	}
	out.Reset()
	errb.Reset()
	to := filepath.Join(t.TempDir(), "data")
	if code := Restore([]string{"-backup", e.archive, "-to", to}, &out, &errb); code != 0 || !strings.Contains(out.String(), "restored") {
		t.Fatalf("restore: %d %s %s", code, out.String(), errb.String())
	}
	if _, err := os.Stat(filepath.Join(to, "alcove.sealed")); err != nil {
		t.Fatal("the sealed database was not restored")
	}
	// A second restore into the same (now non-empty) place is refused.
	errb.Reset()
	if code := Restore([]string{"-backup", e.archive, "-to", to}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "FAILED") {
		t.Fatalf("restore into a non-empty folder: %d %s", code, errb.String())
	}
	// A damaged archive is refused.
	raw, _ := os.ReadFile(e.archive)
	raw[len(raw)/2] ^= 0xff
	bad := filepath.Join(t.TempDir(), "alcove-backup-20260101T000000Z-x.tar")
	_ = os.WriteFile(bad, raw, 0o600)
	errb.Reset()
	if code := Verify([]string{"-file", bad}, &out, &errb); code != 1 {
		t.Fatalf("a damaged archive verified: %d", code)
	}
	if code := Verify(nil, &out, &errb); code != 2 {
		t.Fatalf("missing flag: %d", code)
	}
	if code := Restore([]string{"-backup", e.archive}, &out, &errb); code != 2 {
		t.Fatalf("missing flag: %d", code)
	}
}

func TestDrillProvesAKeyCopy(t *testing.T) {
	e := setup(t)
	var out, errb bytes.Buffer
	args := []string{"-backup", e.archive, "-key", e.keyPath, "-tmp", e.tmp, "-allow-nontmpfs", "-scratch", t.TempDir()}
	if code := Drill(args, strings.NewReader(""), &out, &errb); code != 0 {
		t.Fatalf("drill: %d %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "chain verifies") || !strings.Contains(out.String(), keyfile.Fingerprint(e.key)) {
		t.Fatalf("drill output: %s", out.String())
	}

	// A different key is refused, and the report names nothing sensitive.
	other, line, _ := keyfile.Generate()
	_ = other
	otherPath := filepath.Join(t.TempDir(), "other.key")
	if err := keyfile.WriteNew(otherPath, line); err != nil {
		t.Fatal(err)
	}
	errb.Reset()
	args[3] = otherPath
	if code := Drill(args, strings.NewReader(""), &out, &errb); code != 1 || !strings.Contains(errb.String(), "does not open") {
		t.Fatalf("a wrong key: %d %s", code, errb.String())
	}
	if code := Drill([]string{"-backup", e.archive}, strings.NewReader(""), &out, &errb); code != 2 {
		t.Fatalf("missing -key: %d", code)
	}
}

func TestDrillWithAWrappedKeyCopy(t *testing.T) {
	e := setup(t)
	loadOpts = keyfile.ParseOptions{AllowWeakParams: true}
	t.Cleanup(func() { loadOpts = keyfile.ParseOptions{} })
	wrapped, err := keyfile.Wrap(e.key, "correct horse battery", keyfile.TestParams())
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "wrapped.key")
	if err := keyfile.WriteNew(p, wrapped); err != nil {
		t.Fatal(err)
	}
	args := []string{"-backup", e.archive, "-key", p, "-tmp", e.tmp, "-allow-nontmpfs", "-scratch", t.TempDir()}
	var out, errb bytes.Buffer
	if code := Drill(args, strings.NewReader("\n"), &out, &errb); code != 2 || !strings.Contains(errb.String(), "passphrase") {
		t.Fatalf("no passphrase: %d %s", code, errb.String())
	}
	errb.Reset()
	if code := Drill(args, strings.NewReader("wrong passphrase!\n"), &out, &errb); code != 1 {
		t.Fatalf("wrong passphrase: %d", code)
	}
	if code := Drill(args, strings.NewReader("correct horse battery\n"), &out, &errb); code != 0 {
		t.Fatalf("right passphrase: %d %s", code, errb.String())
	}
}

func TestMakeTestBackupReadsBackThroughTheRealReader(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.tryst")
	var out, errb bytes.Buffer
	if code := MakeTest([]string{"-out", p, "-entries", "5", "-photos", "2", "-password", "a test password"}, &out, &errb); code != 0 {
		t.Fatalf("%d %s", code, errb.String())
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	bk, err := trystbk.Read(f, "a test password")
	if err != nil {
		t.Fatalf("the synthetic backup does not read: %v", err)
	}
	defer bk.Close()
	if bk.Table("encounters") == nil || len(bk.Table("encounters").Rows) != 5 || len(bk.Media()) != 2 {
		t.Fatal("wrong contents")
	}
	if code := MakeTest([]string{"-out", p}, &out, &errb); code != 1 {
		t.Fatalf("overwriting must be refused, got %d", code)
	}
	if code := MakeTest([]string{"-out", filepath.Join(t.TempDir(), "x"), "-entries", "0"}, &out, &errb); code != 2 {
		t.Fatalf("bad flag: %d", code)
	}
	if code := MakeTest(nil, &out, &errb); code != 2 {
		t.Fatalf("missing -out: %d", code)
	}
}
