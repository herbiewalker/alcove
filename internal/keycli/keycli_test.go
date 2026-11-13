package keycli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/herbiewalker/alcove/internal/keyfile"
)

func TestKeygenToFileThenKeycheck(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys", "alcove.key")
	var out, errb bytes.Buffer
	if code := Keygen([]string{"-out", path}, strings.NewReader(""), &out, &errb); code != 0 {
		t.Fatalf("keygen exit %d: %s", code, errb.String())
	}
	if strings.Contains(out.String(), "alcove-key-1") {
		t.Fatal("writing to a file must not also print the key")
	}
	if !strings.Contains(errb.String(), "second copy") {
		t.Error("the backup reminder is missing")
	}
	// Keycheck proves the file opens, and prints the same fingerprint keygen reported.
	var cout, cerr bytes.Buffer
	if code := Keycheck([]string{"-file", path}, strings.NewReader(""), &cout, &cerr); code != 0 {
		t.Fatalf("keycheck exit %d: %s", code, cerr.String())
	}
	fp := between(out.String(), "fingerprint: ", "\n")
	if fp == "" || !strings.Contains(cout.String(), fp) {
		t.Fatalf("fingerprints differ: keygen %q vs keycheck %q", fp, cout.String())
	}
}

func TestKeygenNeverOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alcove.key")
	var o, e bytes.Buffer
	if Keygen([]string{"-out", path}, strings.NewReader(""), &o, &e) != 0 {
		t.Fatal(e.String())
	}
	first, _ := os.ReadFile(path)
	o.Reset()
	e.Reset()
	if code := Keygen([]string{"-out", path}, strings.NewReader(""), &o, &e); code == 0 {
		t.Fatal("a second keygen over an existing key must fail")
	}
	second, _ := os.ReadFile(path)
	if !bytes.Equal(first, second) {
		t.Fatal("an existing key was overwritten")
	}
}

func TestKeygenToStdout(t *testing.T) {
	var out, errb bytes.Buffer
	if Keygen(nil, strings.NewReader(""), &out, &errb) != 0 {
		t.Fatal(errb.String())
	}
	if !strings.HasPrefix(out.String(), "alcove-key-1:") {
		t.Fatalf("stdout = %q", out.String())
	}
}

func TestWrappedKeygenAndKeycheck(t *testing.T) {
	old, oldOpts := wrapParams, loadOpts
	wrapParams, loadOpts = keyfile.TestParams(), keyfile.ParseOptions{AllowWeakParams: true}
	t.Cleanup(func() { wrapParams, loadOpts = old, oldOpts })

	path := filepath.Join(t.TempDir(), "alcove.key")
	var o, e bytes.Buffer
	if code := Keygen([]string{"-out", path, "-wrap"}, strings.NewReader("hunter2\n"), &o, &e); code != 0 {
		t.Fatalf("exit %d: %s", code, e.String())
	}
	raw, _ := os.ReadFile(path)
	if !keyfile.IsWrapped(raw) {
		t.Fatal("not wrapped")
	}
	var co, ce bytes.Buffer
	if code := Keycheck([]string{"-file", path}, strings.NewReader("hunter2\n"), &co, &ce); code != 0 || !strings.Contains(co.String(), "passphrase-wrapped") {
		t.Fatalf("keycheck: %d %s %s", code, co.String(), ce.String())
	}
	co.Reset()
	ce.Reset()
	if code := Keycheck([]string{"-file", path}, strings.NewReader("wrong\n"), &co, &ce); code == 0 {
		t.Fatal("a wrong passphrase must fail")
	}
	if code := Keycheck([]string{"-file", path}, strings.NewReader(""), &co, &ce); code == 0 {
		t.Fatal("a missing passphrase must fail")
	}
	if code := Keygen([]string{"-out", filepath.Join(t.TempDir(), "x.key"), "-wrap"}, strings.NewReader(""), &o, &e); code == 0 {
		t.Fatal("-wrap with no passphrase must fail")
	}
}

func TestKeycheckRejectsGarbageAndMissing(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "bad.key")
	_ = os.WriteFile(bad, []byte("nonsense"), 0o600)
	var o, e bytes.Buffer
	if Keycheck([]string{"-file", bad}, strings.NewReader(""), &o, &e) == 0 {
		t.Fatal("garbage accepted")
	}
	if Keycheck([]string{"-file", filepath.Join(t.TempDir(), "none")}, strings.NewReader(""), &o, &e) == 0 {
		t.Fatal("a missing file accepted")
	}
	if Keycheck(nil, strings.NewReader(""), &o, &e) != 2 {
		t.Fatal("missing -file must be a usage error")
	}
}

func between(s, a, b string) string {
	i := strings.Index(s, a)
	if i < 0 {
		return ""
	}
	s = s[i+len(a):]
	if j := strings.Index(s, b); j >= 0 {
		return s[:j]
	}
	return s
}
