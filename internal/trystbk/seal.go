package trystbk

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"fmt"

	"github.com/tink-crypto/tink-go/v2/streamingaead/subtle"
	"golang.org/x/crypto/pbkdf2"
)

// SealOptions controls how a container is written.
type SealOptions struct {
	// Version is the container version byte: FormatVersion1 or FormatVersion2.
	// Zero means FormatVersion2, what Tryst writes today.
	Version int
	// Iterations is the PBKDF2 count, within MinIterations..MaxIterations. Zero means
	// DefaultIterations. Tests use MinIterations to stay fast.
	Iterations int
	// Salt, when set, is used instead of a random one (deterministic tests only).
	Salt *[SaltSize]byte
}

// Seal wraps zipBody (the plaintext ZIP) in a TRYSTBK1 container encrypted under password.
//
// It uses exactly the primitives the reader uses, with the same parameters, and refuses
// any header Tryst itself would refuse. The export flow (M6) builds on this, and its
// output must be proven by restoring it in Tryst on an emulator (N8).
func Seal(password string, zipBody []byte, opt SealOptions) ([]byte, error) {
	if opt.Version == 0 {
		opt.Version = FormatVersion2
	}
	if opt.Iterations == 0 {
		opt.Iterations = DefaultIterations
	}
	h := Header{Version: opt.Version, Iterations: opt.Iterations}
	if opt.Salt != nil {
		h.Salt = *opt.Salt
	} else if _, err := rand.Read(h.Salt[:]); err != nil {
		return nil, fmt.Errorf("trystbk: salt: %w", err)
	}
	hdr, err := h.Marshal()
	if err != nil {
		return nil, err
	}

	key := pbkdf2.Key([]byte(password), h.Salt[:], h.Iterations, KeySizeBytes, sha256.New)
	aead, err := subtle.NewAESGCMHKDF(key, HKDFAlg, KeySizeBytes, SegmentSize, FirstSegmentOffset)
	if err != nil {
		return nil, fmt.Errorf("trystbk: build AEAD: %w", err)
	}

	var out bytes.Buffer
	out.Write(hdr)
	w, err := aead.NewEncryptingWriter(&out, []byte(AssociatedData))
	if err != nil {
		return nil, fmt.Errorf("trystbk: encrypting writer: %w", err)
	}
	if _, err := w.Write(zipBody); err != nil {
		return nil, fmt.Errorf("trystbk: encrypt: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("trystbk: encrypt close: %w", err)
	}
	return out.Bytes(), nil
}
