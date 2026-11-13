// Package blobstore keeps Alcove's files on the data volume: original uploads and photos.
//
// Hard constraint 4: only ciphertext reaches the data volume.
//   - An upload is stored exactly as Tryst wrote it. It is already encrypted under the member's
//     backup password, so it is written verbatim, immutably, with a SHA-256 for tamper evidence
//     (hard constraint 1, ADR D-7).
//   - A photo is encrypted on the way in under its owner's data key with Tryst's own streaming
//     primitive (AES-GCM-HKDF, 1 MiB segments). The plaintext is never written anywhere; it only
//     passes through memory while it is encrypted or decrypted.
//
// Layout: <root>/blobs/<owner>/<kind>/<id>, with owner, kind and id validated so a hostile name
// cannot escape the directory.
package blobstore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/tink-crypto/tink-go/v2/streamingaead/subtle"
)

// Errors.
var (
	ErrBadName  = errors.New("blobstore: invalid owner or blob id")
	ErrExists   = errors.New("blobstore: blob already exists (blobs are immutable)")
	ErrNotFound = errors.New("blobstore: blob not found")
	ErrTampered = errors.New("blobstore: stored blob does not match its SHA-256")
	ErrTooLarge = errors.New("blobstore: blob exceeds the size bound")
)

const (
	kindUpload = "u"
	kindMedia  = "m"

	segmentSize = 1 << 20
	keySize     = 32
)

// Store is a blob store rooted at a directory.
type Store struct{ root string }

// New returns a store under root (usually the data directory).
func New(root string) *Store { return &Store{root: root} }

// valid reports whether s is a safe single path element: letters, digits, '-' and '_' only.
func valid(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		ok := r == '-' || r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !ok {
			return false
		}
	}
	return true
}

func (s *Store) path(owner, kind, id string) (string, error) {
	if !valid(owner) || !valid(id) {
		return "", ErrBadName
	}
	return filepath.Join(s.root, "blobs", owner, kind, id), nil
}

// PutUpload stores r verbatim as an immutable upload and returns its SHA-256 (hex) and size.
// max bounds the size (0 means no bound); a larger stream is rejected and nothing is kept.
func (s *Store) PutUpload(owner, id string, r io.Reader, max int64) (sum string, size int64, err error) {
	dst, err := s.path(owner, kindUpload, id)
	if err != nil {
		return "", 0, err
	}
	if _, err := os.Stat(dst); err == nil {
		return "", 0, ErrExists
	}
	tmp, err := s.tempFile(filepath.Dir(dst))
	if err != nil {
		return "", 0, err
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	h := sha256.New()
	var src io.Reader = r
	if max > 0 {
		src = io.LimitReader(r, max+1)
	}
	size, err = io.Copy(io.MultiWriter(tmp, h), src)
	if err != nil {
		return "", 0, err
	}
	if max > 0 && size > max {
		err = ErrTooLarge
		return "", 0, err
	}
	if err = tmp.Sync(); err != nil {
		return "", 0, err
	}
	if err = tmp.Close(); err != nil {
		return "", 0, err
	}
	if err = os.Rename(tmpName, dst); err != nil {
		return "", 0, err
	}
	_ = os.Chmod(dst, 0o400)
	return hex.EncodeToString(h.Sum(nil)), size, nil
}

// OpenUpload opens a stored upload. If wantSHA is not empty the file is verified against it first,
// so a flipped byte is refused before any of it is served.
func (s *Store) OpenUpload(owner, id, wantSHA string) (*os.File, error) {
	p, err := s.path(owner, kindUpload, id)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if wantSHA != "" {
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			_ = f.Close()
			return nil, err
		}
		if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), wantSHA) {
			_ = f.Close()
			return nil, ErrTampered
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			_ = f.Close()
			return nil, err
		}
	}
	return f, nil
}

func aeadFor(dek []byte) (interface {
	NewEncryptingWriter(io.Writer, []byte) (io.WriteCloser, error)
	NewDecryptingReader(io.Reader, []byte) (io.Reader, error)
}, error) {
	if len(dek) != keySize {
		return nil, fmt.Errorf("blobstore: data key must be %d bytes, got %d", keySize, len(dek))
	}
	a, err := subtle.NewAESGCMHKDF(dek, "SHA256", keySize, segmentSize, 0)
	if err != nil {
		return nil, err
	}
	return a, nil
}

func mediaAD(owner, id string) []byte { return []byte("alcove-media|" + owner + "|" + id) }

// PutMedia encrypts r under dek and stores it. The plaintext never touches the disk. max bounds
// the plaintext size (0 means no bound).
func (s *Store) PutMedia(owner, id string, dek []byte, r io.Reader, max int64) (size int64, err error) {
	dst, err := s.path(owner, kindMedia, id)
	if err != nil {
		return 0, err
	}
	if _, err := os.Stat(dst); err == nil {
		return 0, ErrExists
	}
	a, err := aeadFor(dek)
	if err != nil {
		return 0, err
	}
	tmp, err := s.tempFile(filepath.Dir(dst))
	if err != nil {
		return 0, err
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	w, err := a.NewEncryptingWriter(tmp, mediaAD(owner, id))
	if err != nil {
		return 0, err
	}
	var src io.Reader = r
	if max > 0 {
		src = io.LimitReader(r, max+1)
	}
	size, err = io.Copy(w, src)
	if err != nil {
		return 0, err
	}
	if max > 0 && size > max {
		err = ErrTooLarge
		return 0, err
	}
	if err = w.Close(); err != nil {
		return 0, err
	}
	if err = tmp.Sync(); err != nil {
		return 0, err
	}
	if err = tmp.Close(); err != nil {
		return 0, err
	}
	if err = os.Rename(tmpName, dst); err != nil {
		return 0, err
	}
	_ = os.Chmod(dst, 0o400)
	return size, nil
}

// OpenMedia returns a reader that decrypts a stored photo under dek. A wrong key or a changed
// byte surfaces as a read error, never as partial plaintext passed off as good.
func (s *Store) OpenMedia(owner, id string, dek []byte) (io.ReadCloser, error) {
	p, err := s.path(owner, kindMedia, id)
	if err != nil {
		return nil, err
	}
	a, err := aeadFor(dek)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r, err := a.NewDecryptingReader(f, mediaAD(owner, id))
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("blobstore: decrypt: %w", err)
	}
	return &readCloser{Reader: r, c: f}, nil
}

type readCloser struct {
	io.Reader
	c io.Closer
}

func (r *readCloser) Close() error { return r.c.Close() }

// Delete removes one blob (the only sanctioned deleter is retention, ADR D-8). It is idempotent.
func (s *Store) Delete(owner, id string) error {
	for _, kind := range []string{kindUpload, kindMedia} {
		p, err := s.path(owner, kind, id)
		if err != nil {
			return err
		}
		if err := removeReadOnly(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// DeleteOwner removes every blob an owner has (a purge). It is idempotent.
func (s *Store) DeleteOwner(owner string) error {
	if !valid(owner) {
		return ErrBadName
	}
	dir := filepath.Join(s.root, "blobs", owner)
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			_ = os.Chmod(p, 0o600)
		}
		return nil
	})
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return nil
}

func removeReadOnly(p string) error {
	_ = os.Chmod(p, 0o600)
	return os.Remove(p)
}

func (s *Store) tempFile(dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return os.CreateTemp(dir, ".incoming-*")
}
