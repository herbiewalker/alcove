// Package seal encrypts Alcove's database snapshot for the data directory (ADR D-16).
//
// The live database sits on tmpfs; what reaches the data volume is this sealed blob. The
// primitive is AES-256-GCM, single shot: the database holds metadata and rows, not photos, so
// it is small, and streaming would add complexity for no gain.
//
// Adapted from Sanctum's `internal/index/seal.go` (MIT, see internal/trystbk/NOTICE for the
// licence text), including its crash-safety rules.
//
// Wire format (raw bytes on disk):
//
//	magic      4 bytes  "ALSL"
//	version    1 byte   0x01
//	reserved   3 bytes  0
//	bindHash  32 bytes  SHA-256 of the caller's binding blob
//	nonce     12 bytes  random per seal
//	ciphertext + tag     AAD = the first 40 bytes (everything before the nonce)
//
// The binding ties a seal to something outside it (Alcove passes the store manifest bytes), so a
// swapped or edited manifest fails with ErrManifestOutOfBand BEFORE the AEAD attempt, a separate
// and more useful signal than "wrong key". A wrong key or a tampered ciphertext both fail the tag
// check; the AEAD cannot tell them apart, so neither do we.
package seal

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	magic       = "ALSL"
	version     = byte(1)
	bindHashLen = 32
	nonceLen    = 12
	hdrLen      = len(magic) + 1 + 3 + bindHashLen + nonceLen // 52

	// File names inside the data directory.
	CurrentName = "alcove.sealed"
	PrevName    = "alcove.sealed.prev"
	TmpName     = "alcove.sealed.tmp"
)

// Errors.
var (
	ErrBadMagic          = errors.New("seal: bad magic, not an Alcove seal")
	ErrBadVersion        = errors.New("seal: unsupported seal format version")
	ErrTruncated         = errors.New("seal: file too short to be a valid seal")
	ErrAuth              = errors.New("seal: unseal failed, wrong key or tampered seal")
	ErrManifestOutOfBand = errors.New("seal: the manifest binding does not match, so manifest.json changed since this seal was written (or the seal was moved between stores)")
)

// Seal encrypts plaintext under key (32 bytes). binding may be nil only in tests.
func Seal(key, binding, plaintext []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	var nonce [nonceLen]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, fmt.Errorf("seal: nonce: %w", err)
	}
	bind := sha256.Sum256(binding)
	out := make([]byte, 0, hdrLen+len(plaintext)+gcm.Overhead())
	out = append(out, magic...)
	out = append(out, version, 0, 0, 0)
	out = append(out, bind[:]...)
	out = append(out, nonce[:]...)
	return gcm.Seal(out, nonce[:], plaintext, out[:hdrLen-nonceLen]), nil
}

// Unseal reverses Seal.
func Unseal(key, binding, sealed []byte) ([]byte, error) {
	if len(sealed) < hdrLen {
		return nil, ErrTruncated
	}
	if string(sealed[:4]) != magic {
		return nil, ErrBadMagic
	}
	if sealed[4] != version {
		return nil, fmt.Errorf("%w: %d (this build handles %d)", ErrBadVersion, sealed[4], version)
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	want := sha256.Sum256(binding)
	if subtle.ConstantTimeCompare(sealed[8:8+bindHashLen], want[:]) != 1 {
		return nil, ErrManifestOutOfBand
	}
	nonce := sealed[8+bindHashLen : hdrLen]
	pt, err := gcm.Open(nil, nonce, sealed[hdrLen:], sealed[:hdrLen-nonceLen])
	if err != nil {
		return nil, fmt.Errorf("%w (%v)", ErrAuth, err)
	}
	return pt, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("seal: key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// WriteFile seals plaintext into dir atomically: write a temp file and fsync it, move the current
// seal to .prev, then move the temp file into place. A crash at any point leaves at least one
// complete, authenticating generation; a failed seal never destroys the last good one.
func WriteFile(dir string, key, binding, plaintext []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	sealed, err := Seal(key, binding, plaintext)
	if err != nil {
		return err
	}
	cur, prev, tmp := filepath.Join(dir, CurrentName), filepath.Join(dir, PrevName), filepath.Join(dir, TmpName)
	if err := writeSynced(tmp, sealed); err != nil {
		return err
	}
	if _, err := os.Stat(cur); err == nil {
		if err := os.Rename(cur, prev); err != nil {
			return fmt.Errorf("seal: promote to .prev: %w", err)
		}
	}
	if err := os.Rename(tmp, cur); err != nil {
		return fmt.Errorf("seal: promote tmp: %w", err)
	}
	return nil
}

// ReadFile returns the plaintext of the newest generation that authenticates under key.
// Candidates, newest first: the current seal, a complete-but-unpromoted temp file left by a crash
// (the old seal is only moved aside after the temp file is synced), then the previous generation.
//
// If nothing exists the error wraps os.ErrNotExist; callers must treat that as a first run only
// after checking Exists, never on any other failure: an unreadable or unauthenticated seal must
// never be mistaken for a fresh store. If a candidate existed but did not authenticate, that error
// wins over "not found", because a wrong key is the more actionable fact. A binding mismatch
// short-circuits the fallbacks: every generation shares the same manifest.
func ReadFile(dir string, key, binding []byte) ([]byte, error) {
	var authErr, readErr error
	for _, name := range []string{CurrentName, TmpName, PrevName} {
		sealed, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			if readErr == nil {
				readErr = err
			}
			continue
		}
		pt, err := Unseal(key, binding, sealed)
		if err == nil {
			return pt, nil
		}
		if errors.Is(err, ErrManifestOutOfBand) {
			return nil, err
		}
		if authErr == nil {
			authErr = err
		}
	}
	if authErr != nil {
		return nil, authErr
	}
	return nil, readErr
}

// Exists reports whether any seal generation is on disk. False means a brand-new store.
func Exists(dir string) bool {
	for _, name := range []string{CurrentName, TmpName, PrevName} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}

func writeSynced(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
