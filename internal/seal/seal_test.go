package seal

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func key(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }

func TestRoundTrip(t *testing.T) {
	pt := []byte("SQLite format 3\x00 plaintext that must not appear in the seal")
	s, err := Seal(key(1), []byte("manifest"), pt)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(s, []byte("SQLite format")) || bytes.Contains(s, []byte("plaintext")) {
		t.Fatal("plaintext visible in the sealed bytes")
	}
	got, err := Unseal(key(1), []byte("manifest"), s)
	if err != nil || !bytes.Equal(got, pt) {
		t.Fatalf("Unseal: %v", err)
	}
	s2, _ := Seal(key(1), []byte("manifest"), pt)
	if bytes.Equal(s, s2) {
		t.Fatal("two seals of the same data must differ (fresh nonce)")
	}
}

func TestWrongKeyFailsCleanly(t *testing.T) {
	s, _ := Seal(key(1), nil, []byte("secret"))
	pt, err := Unseal(key(2), nil, s)
	if !errors.Is(err, ErrAuth) || pt != nil {
		t.Fatalf("err = %v, plaintext = %q (must fail with nothing)", err, pt)
	}
}

func TestTamperFails(t *testing.T) {
	s, _ := Seal(key(1), []byte("m"), []byte("secret data here"))
	for _, pos := range []int{0, 4, 9, 41, hdrLen, len(s) - 1} {
		bad := append([]byte(nil), s...)
		bad[pos] ^= 0x01
		if _, err := Unseal(key(1), []byte("m"), bad); err == nil {
			t.Errorf("flip at %d accepted", pos)
		}
	}
	if _, err := Unseal(key(1), []byte("m"), s[:hdrLen-1]); !errors.Is(err, ErrTruncated) {
		t.Errorf("truncated: %v", err)
	}
	if _, err := Unseal(key(1), []byte("m"), s[:hdrLen]); err == nil {
		t.Error("header-only seal accepted")
	}
}

func TestBindingMismatchIsADistinctError(t *testing.T) {
	s, _ := Seal(key(1), []byte("manifest-A"), []byte("data"))
	if _, err := Unseal(key(1), []byte("manifest-B"), s); !errors.Is(err, ErrManifestOutOfBand) {
		t.Fatalf("err = %v, want ErrManifestOutOfBand", err)
	}
	// A rewritten header hash that matches B still fails the AEAD, because the hash is in the AAD.
	bad := append([]byte(nil), s...)
	h := sha256Of("manifest-B")
	copy(bad[8:8+bindHashLen], h[:])
	if _, err := Unseal(key(1), []byte("manifest-B"), bad); !errors.Is(err, ErrAuth) {
		t.Fatalf("forged binding hash: %v, want ErrAuth", err)
	}
}

func TestBadKeySizeAndMagic(t *testing.T) {
	if _, err := Seal([]byte("short"), nil, []byte("x")); err == nil {
		t.Error("short key accepted")
	}
	s, _ := Seal(key(1), nil, []byte("x"))
	s[0] = 'X'
	if _, err := Unseal(key(1), nil, s); !errors.Is(err, ErrBadMagic) {
		t.Errorf("magic: %v", err)
	}
	s[0] = 'A'
	s[4] = 9
	if _, err := Unseal(key(1), nil, s); !errors.Is(err, ErrBadVersion) {
		t.Errorf("version: %v", err)
	}
}

func TestFileGenerationsAndRecovery(t *testing.T) {
	dir := t.TempDir()
	if Exists(dir) {
		t.Fatal("empty dir reported a seal")
	}
	if _, err := ReadFile(dir, key(1), nil); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("first run must wrap ErrNotExist: %v", err)
	}
	if err := WriteFile(dir, key(1), nil, []byte("gen1")); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(dir, key(1), nil, []byte("gen2")); err != nil {
		t.Fatal(err)
	}
	if !Exists(dir) {
		t.Fatal("Exists false after writing")
	}
	if got, _ := ReadFile(dir, key(1), nil); string(got) != "gen2" {
		t.Fatalf("current = %q", got)
	}
	// Corrupt the current seal: the previous generation must be used.
	cur := filepath.Join(dir, CurrentName)
	b, _ := os.ReadFile(cur)
	b[len(b)-1] ^= 0xFF
	_ = os.WriteFile(cur, b, 0o600)
	if got, err := ReadFile(dir, key(1), nil); err != nil || string(got) != "gen1" {
		t.Fatalf("fallback to .prev: %q %v", got, err)
	}
}

func TestCrashBetweenRenamesRecoversFromTmp(t *testing.T) {
	dir := t.TempDir()
	_ = WriteFile(dir, key(1), nil, []byte("old"))
	// Simulate a crash after the new seal was written and synced to .tmp but before it was promoted:
	// the current seal has been moved to .prev and no current seal exists.
	newer, _ := Seal(key(1), nil, []byte("new"))
	_ = os.WriteFile(filepath.Join(dir, TmpName), newer, 0o600)
	_ = os.Rename(filepath.Join(dir, CurrentName), filepath.Join(dir, PrevName))
	if got, err := ReadFile(dir, key(1), nil); err != nil || string(got) != "new" {
		t.Fatalf("recover from .tmp: %q %v", got, err)
	}
}

func TestWrongKeyIsNotAFirstRun(t *testing.T) {
	dir := t.TempDir()
	_ = WriteFile(dir, key(1), nil, []byte("data"))
	_, err := ReadFile(dir, key(2), nil)
	if errors.Is(err, os.ErrNotExist) || !errors.Is(err, ErrAuth) {
		t.Fatalf("a wrong key must report ErrAuth, never look like a missing store: %v", err)
	}
}

func TestBindingMismatchShortCircuitsFallbacks(t *testing.T) {
	dir := t.TempDir()
	_ = WriteFile(dir, key(1), []byte("A"), []byte("one"))
	_ = WriteFile(dir, key(1), []byte("A"), []byte("two"))
	if _, err := ReadFile(dir, key(1), []byte("B")); !errors.Is(err, ErrManifestOutOfBand) {
		t.Fatalf("err = %v", err)
	}
}

func sha256Of(s string) [32]byte { return sha256.Sum256([]byte(s)) }
