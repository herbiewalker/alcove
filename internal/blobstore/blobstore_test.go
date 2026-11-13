package blobstore

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func dek(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }

func TestUploadIsVerbatimImmutableAndVerified(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	data := []byte("TRYSTBK1 pretend ciphertext bytes")
	sum, size, err := s.PutUpload("alice", "up1", bytes.NewReader(data), 0)
	if err != nil || size != int64(len(data)) {
		t.Fatalf("PutUpload: %v size=%d", err, size)
	}
	want := sha256.Sum256(data)
	if sum != hex.EncodeToString(want[:]) {
		t.Fatalf("sha = %s", sum)
	}
	f, err := s.OpenUpload("alice", "up1", sum)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(f)
	f.Close()
	if !bytes.Equal(got, data) {
		t.Fatal("upload not stored verbatim")
	}
	if _, _, err := s.PutUpload("alice", "up1", bytes.NewReader(data), 0); !errors.Is(err, ErrExists) {
		t.Fatalf("overwrite: %v (blobs are immutable)", err)
	}
	// Tamper with the stored file: verification must refuse before serving a byte.
	p := filepath.Join(root, "blobs", "alice", "u", "up1")
	_ = os.Chmod(p, 0o600)
	b, _ := os.ReadFile(p)
	b[3] ^= 0x01
	_ = os.WriteFile(p, b, 0o600)
	if _, err := s.OpenUpload("alice", "up1", sum); !errors.Is(err, ErrTampered) {
		t.Fatalf("tampered upload: %v", err)
	}
	if _, err := s.OpenUpload("alice", "missing", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestUploadSizeBoundKeepsNothing(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	_, _, err := s.PutUpload("alice", "big", bytes.NewReader(make([]byte, 1000)), 100)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v", err)
	}
	assertNoFiles(t, filepath.Join(root, "blobs", "alice"))
}

func TestHostileNamesAreRefused(t *testing.T) {
	s := New(t.TempDir())
	for _, bad := range []string{"", "..", "../x", "a/b", `a\b`, "a b", ".", "x\x00y", strings.Repeat("a", 200)} {
		if _, _, err := s.PutUpload(bad, "id", strings.NewReader("x"), 0); !errors.Is(err, ErrBadName) {
			t.Errorf("owner %q: %v", bad, err)
		}
		if _, _, err := s.PutUpload("alice", bad, strings.NewReader("x"), 0); !errors.Is(err, ErrBadName) {
			t.Errorf("id %q: %v", bad, err)
		}
		if _, err := s.OpenUpload("alice", bad, ""); !errors.Is(err, ErrBadName) {
			t.Errorf("open id %q: %v", bad, err)
		}
	}
	if err := s.DeleteOwner("../.."); !errors.Is(err, ErrBadName) {
		t.Errorf("DeleteOwner: %v", err)
	}
}

func TestMediaRoundTripAndCiphertextOnly(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	plain := make([]byte, 3<<20+777) // three segments and a bit
	_, _ = rand.Read(plain)
	copy(plain, "JFIF-plaintext-marker")
	if _, err := s.PutMedia("alice", "m1", dek(1), bytes.NewReader(plain), 0); err != nil {
		t.Fatal(err)
	}
	onDisk, _ := os.ReadFile(filepath.Join(root, "blobs", "alice", "m", "m1"))
	if bytes.Contains(onDisk, []byte("JFIF-plaintext-marker")) {
		t.Fatal("plaintext found on disk")
	}
	r, err := s.OpenMedia("alice", "m1", dek(1))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	r.Close()
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("round trip failed: %v", err)
	}
	if _, err := s.PutMedia("alice", "m1", dek(1), bytes.NewReader(plain), 0); !errors.Is(err, ErrExists) {
		t.Fatalf("overwrite: %v", err)
	}
}

func TestMediaWrongKeyAndTamperFailWithoutPlaintext(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	plain := bytes.Repeat([]byte("photo bytes "), 100000)
	_, _ = s.PutMedia("alice", "m1", dek(1), bytes.NewReader(plain), 0)

	read := func(key []byte) ([]byte, error) {
		r, err := s.OpenMedia("alice", "m1", key)
		if err != nil {
			return nil, err
		}
		defer r.Close()
		return io.ReadAll(r)
	}
	if got, err := read(dek(2)); err == nil || bytes.Contains(got, []byte("photo bytes")) {
		t.Fatalf("a wrong key must fail with no plaintext: err=%v", err)
	}
	p := filepath.Join(root, "blobs", "alice", "m", "m1")
	_ = os.Chmod(p, 0o600)
	b, _ := os.ReadFile(p)
	b[len(b)/2] ^= 0x01
	_ = os.WriteFile(p, b, 0o600)
	if _, err := read(dek(1)); err == nil {
		t.Fatal("a flipped ciphertext byte must fail")
	}
}

func TestMediaIsBoundToItsOwnerAndID(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	_, _ = s.PutMedia("alice", "m1", dek(1), strings.NewReader("secret photo"), 0)
	// Copy alice's file to bob's directory under another id, even with the same key it must not decrypt.
	src := filepath.Join(root, "blobs", "alice", "m", "m1")
	dst := filepath.Join(root, "blobs", "bob", "m")
	_ = os.MkdirAll(dst, 0o700)
	b, _ := os.ReadFile(src)
	_ = os.WriteFile(filepath.Join(dst, "m2"), b, 0o600)
	r, err := s.OpenMedia("bob", "m2", dek(1))
	if err == nil {
		got, rerr := io.ReadAll(r)
		r.Close()
		if rerr == nil {
			t.Fatalf("a blob moved to another owner and id decrypted: %q", got)
		}
	}
}

func TestMediaSizeBoundAndKeySize(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	if _, err := s.PutMedia("alice", "m1", dek(1), bytes.NewReader(make([]byte, 500)), 100); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v", err)
	}
	assertNoFiles(t, filepath.Join(root, "blobs", "alice"))
	if _, err := s.PutMedia("alice", "m1", []byte("short"), strings.NewReader("x"), 0); err == nil {
		t.Fatal("short key accepted")
	}
}

func TestDeleteIsIdempotentAndOwnerPurge(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	_, _, _ = s.PutUpload("alice", "u1", strings.NewReader("a"), 0)
	_, _ = s.PutMedia("alice", "m1", dek(1), strings.NewReader("b"), 0)
	if err := s.Delete("alice", "u1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("alice", "u1"); err != nil {
		t.Fatalf("second delete: %v", err)
	}
	if _, err := s.OpenUpload("alice", "u1", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	if err := s.DeleteOwner("alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "blobs", "alice")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owner directory still there: %v", err)
	}
	if err := s.DeleteOwner("alice"); err != nil {
		t.Fatalf("purging nothing must succeed: %v", err)
	}
}

func assertNoFiles(t *testing.T, dir string) {
	t.Helper()
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			t.Errorf("leftover file after a rejected write: %s", p)
		}
		return nil
	})
}
