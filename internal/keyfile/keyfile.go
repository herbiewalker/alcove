// Package keyfile owns Alcove's master key: its text format, generation, optional
// passphrase wrapping, loading, and the rule that it must live outside the data directory.
//
// ADR D-4 and D-16: the master key seals the database and wraps every user's data key. It is
// read from a file that is NOT inside the data directory, so a pulled disk or a copied data
// folder is ciphertext only. The file is plain ASCII on one line so it can be copied into a
// password manager and pasted back; losing every copy loses every upload and edit, which is why
// the server never creates a key on its own (the operator runs `alcove keygen` deliberately).
//
// Two forms:
//
//	alcove-key-1:<base64url key>                                  plain, starts unattended
//	alcove-key-1w:argon2id:t=3,m=65536,p=4:<salt>:<nonce>:<ct>     wrapped, needs a passphrase (locked mode)
//
// A wrapped key uses Argon2id (memory-hard, ADR D-16) and AES-256-GCM, with the header fields
// bound in as associated data, so a changed parameter fails authentication.
package keyfile

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// KeySize is the master key length in bytes (AES-256).
const KeySize = 32

const (
	plainPrefix   = "alcove-key-1:"
	wrappedPrefix = "alcove-key-1w:"
	saltSize      = 16
	nonceSize     = 12
)

// Production Argon2id floor (OWASP). Parameters below it are refused when loading unless the
// caller opts in for tests, so a hand-edited key file cannot silently weaken the wrap.
const (
	MinTime    = 3
	MinMemKiB  = 64 * 1024
	MinThreads = 1
)

// Errors.
var (
	ErrBadFormat       = errors.New("keyfile: not an Alcove key file")
	ErrNeedsPassphrase = errors.New("keyfile: the key file is passphrase-wrapped; a passphrase is required")
	ErrWrongPassphrase = errors.New("keyfile: wrong passphrase, or the key file was altered")
	ErrWeakParams      = errors.New("keyfile: Argon2id parameters are below the production floor")
	ErrInsideData      = errors.New("keyfile: the key file is inside the data directory")
	ErrExists          = errors.New("keyfile: the key file already exists; refusing to overwrite a key")
)

// Key is the 32-byte master key. Zero it when finished.
type Key struct{ b [KeySize]byte }

// Bytes returns a copy of the key material.
func (k *Key) Bytes() []byte { out := make([]byte, KeySize); copy(out, k.b[:]); return out }

// Zero wipes the key.
func (k *Key) Zero() {
	for i := range k.b {
		k.b[i] = 0
	}
}

// IsZero reports whether the key has been wiped (or never set).
func (k *Key) IsZero() bool {
	var z [KeySize]byte
	return k.b == z
}

// FromBytes builds a Key from exactly KeySize bytes.
func FromBytes(b []byte) (Key, error) {
	if len(b) != KeySize {
		return Key{}, fmt.Errorf("keyfile: key must be %d bytes, got %d", KeySize, len(b))
	}
	var k Key
	copy(k.b[:], b)
	return k, nil
}

// Params are the Argon2id settings for a wrapped key.
type Params struct {
	Time, MemKiB uint32
	Threads      uint8
}

// DefaultParams is the production setting (t=3, m=64 MiB, p=4).
func DefaultParams() Params { return Params{Time: 3, MemKiB: 64 * 1024, Threads: 4} }

// TestParams is deliberately weak and ONLY for tests: it lets the suite wrap and unwrap quickly.
func TestParams() Params { return Params{Time: 1, MemKiB: 64, Threads: 1} }

func (p Params) floorOK() bool {
	return p.Time >= MinTime && p.MemKiB >= MinMemKiB && p.Threads >= MinThreads
}

// Generate makes a new random master key and returns it with its plain text line (no newline).
func Generate() (Key, string, error) {
	var k Key
	if _, err := rand.Read(k.b[:]); err != nil {
		return Key{}, "", fmt.Errorf("keyfile: random: %w", err)
	}
	return k, Encode(k), nil
}

// Encode returns the plain text form of k.
func Encode(k Key) string { return plainPrefix + base64.RawURLEncoding.EncodeToString(k.b[:]) }

// Wrap encrypts k under passphrase and returns the wrapped text line.
func Wrap(k Key, passphrase string, p Params) (string, error) {
	if passphrase == "" {
		return "", errors.New("keyfile: an empty passphrase cannot wrap a key")
	}
	var salt [saltSize]byte
	var nonce [nonceSize]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return "", err
	}
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	header := headerString(p)
	gcm, err := wrapCipher(passphrase, salt[:], p)
	if err != nil {
		return "", err
	}
	ct := gcm.Seal(nil, nonce[:], k.b[:], aad(header, salt[:]))
	enc := base64.RawURLEncoding
	return wrappedPrefix + header + ":" + enc.EncodeToString(salt[:]) + ":" + enc.EncodeToString(nonce[:]) + ":" + enc.EncodeToString(ct), nil
}

// ParseOptions tunes Parse.
type ParseOptions struct {
	// AllowWeakParams accepts Argon2id parameters below the production floor (tests only).
	AllowWeakParams bool
}

// Parse reads a key file's contents. A wrapped key needs passphrase; a plain key ignores it.
func Parse(data []byte, passphrase string, opt ParseOptions) (Key, error) {
	line := strings.TrimSpace(string(data))
	switch {
	case strings.HasPrefix(line, plainPrefix):
		raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(line, plainPrefix))
		if err != nil {
			return Key{}, fmt.Errorf("%w: %v", ErrBadFormat, err)
		}
		k, err := FromBytes(raw)
		if err != nil {
			return Key{}, fmt.Errorf("%w: %v", ErrBadFormat, err)
		}
		return k, nil
	case strings.HasPrefix(line, wrappedPrefix):
		return parseWrapped(strings.TrimPrefix(line, wrappedPrefix), passphrase, opt)
	default:
		return Key{}, ErrBadFormat
	}
}

// IsWrapped reports whether the file contents are a passphrase-wrapped key.
func IsWrapped(data []byte) bool {
	return strings.HasPrefix(strings.TrimSpace(string(data)), wrappedPrefix)
}

func parseWrapped(rest, passphrase string, opt ParseOptions) (Key, error) {
	parts := strings.Split(rest, ":")
	// argon2id : t=..,m=..,p=.. : salt : nonce : ciphertext
	if len(parts) != 5 || parts[0] != "argon2id" {
		return Key{}, fmt.Errorf("%w: wrapped key has the wrong shape", ErrBadFormat)
	}
	p, err := parseParams(parts[1])
	if err != nil {
		return Key{}, fmt.Errorf("%w: %v", ErrBadFormat, err)
	}
	if !opt.AllowWeakParams && !p.floorOK() {
		return Key{}, fmt.Errorf("%w: t=%d m=%d p=%d", ErrWeakParams, p.Time, p.MemKiB, p.Threads)
	}
	enc := base64.RawURLEncoding
	salt, e1 := enc.DecodeString(parts[2])
	nonce, e2 := enc.DecodeString(parts[3])
	ct, e3 := enc.DecodeString(parts[4])
	if e1 != nil || e2 != nil || e3 != nil || len(salt) != saltSize || len(nonce) != nonceSize || len(ct) != KeySize+16 {
		return Key{}, fmt.Errorf("%w: wrapped key fields are malformed", ErrBadFormat)
	}
	if passphrase == "" {
		return Key{}, ErrNeedsPassphrase
	}
	gcm, err := wrapCipher(passphrase, salt, p)
	if err != nil {
		return Key{}, err
	}
	pt, err := gcm.Open(nil, nonce, ct, aad(headerString(p), salt))
	if err != nil {
		return Key{}, ErrWrongPassphrase
	}
	k, err := FromBytes(pt)
	for i := range pt {
		pt[i] = 0
	}
	return k, err
}

func headerString(p Params) string {
	return fmt.Sprintf("argon2id:t=%d,m=%d,p=%d", p.Time, p.MemKiB, p.Threads)
}

func parseParams(s string) (Params, error) {
	var p Params
	for _, kv := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return Params{}, fmt.Errorf("bad parameter %q", kv)
		}
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return Params{}, fmt.Errorf("bad parameter %q", kv)
		}
		switch k {
		case "t":
			p.Time = uint32(n)
		case "m":
			p.MemKiB = uint32(n)
		case "p":
			if n == 0 || n > 255 {
				return Params{}, fmt.Errorf("bad parameter %q", kv)
			}
			p.Threads = uint8(n)
		default:
			return Params{}, fmt.Errorf("unknown parameter %q", k)
		}
	}
	if p.Time == 0 || p.MemKiB == 0 || p.Threads == 0 {
		return Params{}, errors.New("missing parameter")
	}
	return p, nil
}

func wrapCipher(passphrase string, salt []byte, p Params) (cipher.AEAD, error) {
	kek := argon2.IDKey([]byte(passphrase), salt, p.Time, p.MemKiB, p.Threads, KeySize)
	defer func() {
		for i := range kek {
			kek[i] = 0
		}
	}()
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// aad binds the algorithm, parameters and salt, so editing any of them fails authentication.
func aad(header string, salt []byte) []byte {
	return append([]byte("alcove-key-1w|"+header+"|"), salt...)
}

// WriteNew writes line to path, refusing to overwrite an existing file (the key is the one thing
// that cannot be recovered). The file is created owner-read-only where the platform supports it.
func WriteNew(path, line string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrExists
		}
		return err
	}
	if _, err := f.WriteString(line + "\n"); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// Load reads the key file at path.
func Load(path, passphrase string, opt ParseOptions) (Key, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Key{}, err
	}
	defer func() {
		for i := range data {
			data[i] = 0
		}
	}()
	return Parse(data, passphrase, opt)
}

// CheckOutside returns ErrInsideData if keyPath is the data directory or anywhere beneath it.
// Symlinks are resolved on the parts that exist, and the comparison is case-insensitive on
// Windows and macOS, so a link or a different spelling cannot hide the key inside the data.
func CheckOutside(keyPath, dataDir string) error {
	k, err := canonical(keyPath)
	if err != nil {
		return err
	}
	d, err := canonical(dataDir)
	if err != nil {
		return err
	}
	if k == d || strings.HasPrefix(k, d+string(filepath.Separator)) {
		return fmt.Errorf("%w: %s is inside %s", ErrInsideData, keyPath, dataDir)
	}
	return nil
}

// canonical returns an absolute, symlink-resolved path. The path may not exist yet: the longest
// existing ancestor is resolved and the remainder is appended.
func canonical(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	existing, rest := abs, ""
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			break
		}
		rest = filepath.Join(filepath.Base(existing), rest)
		existing = parent
	}
	if resolved, err := filepath.EvalSymlinks(existing); err == nil {
		existing = resolved
	}
	out := filepath.Join(existing, rest)
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		out = strings.ToLower(out)
	}
	return out, nil
}

// Fingerprint returns a short, non-secret identifier of the key (the first 8 hex characters of a
// hash of it). Two key files that print the same fingerprint hold the same key, so a second copy
// can be proved without showing the key (CRITICAL_ASSETS: prove it by opening it).
func Fingerprint(k Key) string {
	sum := sha256.Sum256(append([]byte("alcove-key-fingerprint|"), k.b[:]...))
	return hex.EncodeToString(sum[:4])
}
