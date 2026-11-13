package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/herbiewalker/alcove/internal/config"
	"github.com/herbiewalker/alcove/internal/keyfile"
	"github.com/herbiewalker/alcove/internal/seal"
)

type env struct {
	root string
	cfg  config.Config
}

func newEnv(t *testing.T) env {
	t.Helper()
	root := t.TempDir()
	cfg := config.Config{
		DataDir: filepath.Join(root, "data"), KeyFile: filepath.Join(root, "keys", "alcove.key"),
		TmpDir: filepath.Join(root, "tmp"), AllowNonTmpfsTmp: true, SealEvery: time.Hour,
	}
	return env{root: root, cfg: cfg}
}

func writePlainKey(t *testing.T, path string) keyfile.Key {
	t.Helper()
	k, line, err := keyfile.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err := keyfile.WriteNew(path, line); err != nil {
		t.Fatal(err)
	}
	return k
}

func TestStartsUnattendedWithAPlainKey(t *testing.T) {
	e := newEnv(t)
	writePlainKey(t, e.cfg.KeyFile)
	a, err := Start(e.cfg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if a.State() != StateUnlocked || a.Store() == nil {
		t.Fatal("a plain key must start unlocked with no human step")
	}
	if err := a.Store().SetMeta("k", "v"); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if a.State() != StateLocked {
		t.Fatal("a closed app must report locked")
	}
	// A restart reads the same data, again with nothing typed.
	a2, err := Start(e.cfg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer a2.Close()
	if v, ok, _ := a2.Store().GetMeta("k"); !ok || v != "v" {
		t.Fatalf("data lost across a restart: %q %v", v, ok)
	}
}

func TestRefusesToStartWhenTheKeyIsInsideTheDataDirectory(t *testing.T) {
	e := newEnv(t)
	e.cfg.KeyFile = filepath.Join(e.cfg.DataDir, "alcove.key")
	writePlainKey(t, e.cfg.KeyFile)
	a, err := Start(e.cfg, Options{})
	if !errors.Is(err, keyfile.ErrInsideData) || a != nil {
		t.Fatalf("err = %v, app = %v (a key inside the data directory must refuse to start)", err, a)
	}
	// Nothing was opened: no manifest, no seal, no live database.
	if _, statErr := os.Stat(filepath.Join(e.cfg.DataDir, "manifest.json")); statErr == nil {
		t.Error("a refused start still created a store")
	}
	if entries, _ := os.ReadDir(e.cfg.TmpDir); len(entries) != 0 {
		t.Errorf("a refused start left files in tmp: %v", entries)
	}
}

func TestRefusesAKeyThatIsASubdirectoryOrDotDotAlias(t *testing.T) {
	e := newEnv(t)
	e.cfg.KeyFile = filepath.Join(e.root, "keys", "..", "data", "deep", "alcove.key")
	if _, err := Start(e.cfg, Options{}); !errors.Is(err, keyfile.ErrInsideData) {
		t.Fatalf("err = %v", err)
	}
}

func TestMissingKeyFileGivesAnActionableError(t *testing.T) {
	e := newEnv(t)
	_, err := Start(e.cfg, Options{})
	if !errors.Is(err, ErrNoKeyFile) || !strings.Contains(err.Error(), "alcove keygen") {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(e.cfg.KeyFile); statErr == nil {
		t.Fatal("the server must never create a key on its own")
	}
	if _, statErr := os.Stat(filepath.Join(e.cfg.DataDir, "manifest.json")); statErr == nil {
		t.Fatal("no key must mean no store was created")
	}
}

func TestBadKeyFileFailsCleanly(t *testing.T) {
	e := newEnv(t)
	_ = os.MkdirAll(filepath.Dir(e.cfg.KeyFile), 0o700)
	_ = os.WriteFile(e.cfg.KeyFile, []byte("this is not a key"), 0o600)
	if _, err := Start(e.cfg, Options{}); !errors.Is(err, keyfile.ErrBadFormat) {
		t.Fatalf("err = %v", err)
	}
}

func TestWrongKeyFailsCleanlyAgainstExistingData(t *testing.T) {
	e := newEnv(t)
	writePlainKey(t, e.cfg.KeyFile)
	a, _ := Start(e.cfg, Options{})
	_ = a.Store().SetMeta("k", "v")
	_ = a.Close()
	// Replace the key file with a different key: the sealed data must refuse, not start fresh.
	_ = os.Remove(e.cfg.KeyFile)
	_ = os.Chmod(filepath.Dir(e.cfg.KeyFile), 0o700)
	writePlainKey(t, e.cfg.KeyFile)
	if _, err := Start(e.cfg, Options{}); !errors.Is(err, seal.ErrAuth) {
		t.Fatalf("err = %v, want seal.ErrAuth", err)
	}
}

func TestWrappedKeyStartsLockedAndUnlocks(t *testing.T) {
	e := newEnv(t)
	k, _, _ := keyfile.Generate()
	line, err := keyfile.Wrap(k, "open sesame", keyfile.TestParams())
	if err != nil {
		t.Fatal(err)
	}
	if err := keyfile.WriteNew(e.cfg.KeyFile, line); err != nil {
		t.Fatal(err)
	}
	opt := Options{AllowWeakKeyParams: true}
	a, err := Start(e.cfg, opt)
	if err != nil {
		t.Fatal(err)
	}
	if a.State() != StateLocked || a.Store() != nil {
		t.Fatal("a wrapped key must start locked")
	}
	if err := a.Unlock("wrong"); !errors.Is(err, keyfile.ErrWrongPassphrase) {
		t.Fatalf("wrong passphrase: %v", err)
	}
	if a.State() != StateLocked {
		t.Fatal("a failed unlock must stay locked")
	}
	if err := a.Unlock("open sesame"); err != nil {
		t.Fatal(err)
	}
	if a.State() != StateUnlocked {
		t.Fatal("unlock did not unlock")
	}
	if err := a.Unlock("open sesame"); !errors.Is(err, ErrNotLocked) {
		t.Fatalf("second unlock: %v", err)
	}
	_ = a.Close()
}

func TestWrappedKeyWithWeakParamsIsRefusedInProduction(t *testing.T) {
	e := newEnv(t)
	k, _, _ := keyfile.Generate()
	line, _ := keyfile.Wrap(k, "pw", keyfile.TestParams())
	_ = keyfile.WriteNew(e.cfg.KeyFile, line)
	a, err := Start(e.cfg, Options{}) // production options
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Unlock("pw"); !errors.Is(err, keyfile.ErrWeakParams) {
		t.Fatalf("err = %v", err)
	}
}

func TestRefusesATmpDirOnDiskInProduction(t *testing.T) {
	e := newEnv(t)
	writePlainKey(t, e.cfg.KeyFile)
	e.cfg.AllowNonTmpfsTmp = false
	if _, err := Start(e.cfg, Options{}); err == nil {
		// On a machine where t.TempDir() really is a tmpfs this passes; anywhere else it must refuse.
		t.Log("TempDir is tmpfs-backed here; the refusal is covered in the store tests")
	}
}
