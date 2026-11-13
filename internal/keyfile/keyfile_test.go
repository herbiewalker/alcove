package keyfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateAndRoundTrip(t *testing.T) {
	k, line, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(line, "alcove-key-1:") || strings.ContainsAny(line, " \n") {
		t.Fatalf("line = %q", line)
	}
	got, err := Parse([]byte(line+"\n"), "", ParseOptions{})
	if err != nil || got != k {
		t.Fatalf("Parse: %v equal=%v", err, got == k)
	}
	k2, _, _ := Generate()
	if k == k2 {
		t.Fatal("two generated keys must differ")
	}
}

func TestZero(t *testing.T) {
	k, _, _ := Generate()
	if k.IsZero() {
		t.Fatal("fresh key reported zero")
	}
	k.Zero()
	if !k.IsZero() {
		t.Fatal("Zero did not wipe the key")
	}
}

func TestWrapUnwrap(t *testing.T) {
	k, _, _ := Generate()
	line, err := Wrap(k, "correct horse", TestParams())
	if err != nil {
		t.Fatal(err)
	}
	if !IsWrapped([]byte(line)) || !strings.HasPrefix(line, "alcove-key-1w:argon2id:") {
		t.Fatalf("line = %q", line)
	}
	got, err := Parse([]byte(line), "correct horse", ParseOptions{AllowWeakParams: true})
	if err != nil || got != k {
		t.Fatalf("unwrap: %v", err)
	}
	if _, err := Parse([]byte(line), "wrong", ParseOptions{AllowWeakParams: true}); !errors.Is(err, ErrWrongPassphrase) {
		t.Errorf("wrong passphrase: %v", err)
	}
	if _, err := Parse([]byte(line), "", ParseOptions{AllowWeakParams: true}); !errors.Is(err, ErrNeedsPassphrase) {
		t.Errorf("no passphrase: %v", err)
	}
	if _, err := Wrap(k, "", TestParams()); err == nil {
		t.Error("an empty passphrase must be refused")
	}
}

func TestWrappedRefusesWeakParamsByDefault(t *testing.T) {
	k, _, _ := Generate()
	line, _ := Wrap(k, "pw", TestParams())
	if _, err := Parse([]byte(line), "pw", ParseOptions{}); !errors.Is(err, ErrWeakParams) {
		t.Fatalf("weak params must be refused outside tests: %v", err)
	}
}

func TestWrappedTamperFails(t *testing.T) {
	k, _, _ := Generate()
	line, _ := Wrap(k, "pw", TestParams())
	// Changing the parameters (which are authenticated) or any field must not unwrap.
	for _, bad := range []string{
		strings.Replace(line, "t=1,", "t=2,", 1),
		strings.Replace(line, "m=64,", "m=65,", 1),
		line[:len(line)-3] + "AAA",
	} {
		if _, err := Parse([]byte(bad), "pw", ParseOptions{AllowWeakParams: true}); err == nil {
			t.Errorf("tampered key file accepted: %q", bad)
		}
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "hello", "alcove-key-1:", "alcove-key-1:!!!", "alcove-key-1:AAAA", "alcove-key-1w:argon2id:x", "ALCOVE-KEY-1:AAAA"} {
		if _, err := Parse([]byte(in), "pw", ParseOptions{AllowWeakParams: true}); err == nil {
			t.Errorf("accepted %q", in)
		}
	}
	if _, err := Parse([]byte("nope"), "", ParseOptions{}); !errors.Is(err, ErrBadFormat) {
		t.Errorf("want ErrBadFormat")
	}
}

func TestWriteNewRefusesToOverwrite(t *testing.T) {
	p := filepath.Join(t.TempDir(), "keys", "alcove.key")
	_, line, _ := Generate()
	if err := WriteNew(p, line); err != nil {
		t.Fatal(err)
	}
	if err := WriteNew(p, line); !errors.Is(err, ErrExists) {
		t.Fatalf("second write: %v (a key must never be overwritten)", err)
	}
	got, err := Load(p, "", ParseOptions{})
	if err != nil || got.IsZero() {
		t.Fatalf("Load: %v", err)
	}
}

func TestCheckOutside(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	keys := filepath.Join(root, "keys")
	for _, d := range []string{data, keys} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Outside: fine, including a sibling whose name merely starts the same.
	for _, k := range []string{filepath.Join(keys, "a.key"), filepath.Join(root, "data2", "a.key"), filepath.Join(root, "data.key")} {
		if err := CheckOutside(k, data); err != nil {
			t.Errorf("%s should be outside: %v", k, err)
		}
	}
	// Inside, including a path that does not exist yet and one spelled with "..".
	for _, k := range []string{
		filepath.Join(data, "alcove.key"),
		filepath.Join(data, "sub", "deeper", "alcove.key"),
		filepath.Join(keys, "..", "data", "alcove.key"),
		data,
	} {
		if err := CheckOutside(k, data); !errors.Is(err, ErrInsideData) {
			t.Errorf("%s should be inside: %v", k, err)
		}
	}
}

func TestCheckOutsideSeesThroughSymlinks(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	if err := os.MkdirAll(data, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(data, link); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	if err := CheckOutside(filepath.Join(link, "alcove.key"), data); !errors.Is(err, ErrInsideData) {
		t.Fatalf("a key reached through a symlink into the data directory must be refused: %v", err)
	}
}
