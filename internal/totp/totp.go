// Package totp is optional two-step sign-in for administrators (F20, D-49): time-based one-time
// codes (RFC 6238) and one-time recovery codes. It uses only the standard library.
//
// Nothing here logs. A secret, a code or a recovery code must never reach a log, the admin log or an
// error message (constraint 8), so no function in this package formats one into an error.
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" // #nosec G505 -- RFC 6238 authenticator apps all use HMAC-SHA1
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"
)

const (
	// Period is the length of one code's life in seconds.
	Period = 30
	// Digits is the length of a code.
	Digits = 6
	// SecretBytes is the size of a new secret (160 bits, the size the RFC recommends).
	SecretBytes = 20
	// RecoveryCount is how many recovery codes a person gets.
	RecoveryCount = 10
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewSecret makes a random secret.
func NewSecret() ([]byte, error) {
	s := make([]byte, SecretBytes)
	if _, err := rand.Read(s); err != nil {
		return nil, err
	}
	return s, nil
}

// Base32 is the secret as an authenticator app wants it typed.
func Base32(secret []byte) string { return b32.EncodeToString(secret) }

// URI is the otpauth link an authenticator app reads from a QR code. It contains the secret, so it
// is shown once, on the page that sets the feature up, and never stored or logged.
func URI(issuer, account string, secret []byte) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{}
	q.Set("secret", Base32(secret))
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprint(Digits))
	q.Set("period", fmt.Sprint(Period))
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// Step is the 30-second counter a moment falls in.
func Step(t time.Time) int64 { return t.Unix() / Period }

// Code is the code for one step (HOTP of RFC 4226, with the step as the counter).
func Code(secret []byte, step int64) string {
	var ctr [8]byte
	binary.BigEndian.PutUint64(ctr[:], uint64(step))
	m := hmac.New(sha1.New, secret)
	m.Write(ctr[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	n := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", Digits, n%1000000)
}

// Verify checks a typed code against the steps one either side of now. It returns the step the code
// belongs to, which the caller must store, and refuses any step that is not newer than lastStep, so a
// code that was already used (or an older one) cannot be replayed (RFC 6238 section 5.2). It looks
// at all three steps whatever the answer, and compares in constant time.
func Verify(secret []byte, typed string, now time.Time, lastStep int64) (step int64, ok bool) {
	typed = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || r == '-' {
			return -1
		}
		return r
	}, typed)
	if len(typed) != Digits {
		return 0, false
	}
	cur := Step(now)
	for s := cur - 1; s <= cur+1; s++ {
		if subtle.ConstantTimeCompare([]byte(Code(secret, s)), []byte(typed)) == 1 && s > lastStep && !ok {
			step, ok = s, true
		}
	}
	return step, ok
}

// ---- recovery codes ----

// A code is 12 characters, about 59 bits, shown as xxxx-xxxx-xxxx. The alphabet leaves out
// characters that look alike (0 o 1 l i).
const recoveryAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

// NewRecoveryCodes makes RecoveryCount codes. It returns the codes to show once and the hashes to keep.
func NewRecoveryCodes() (codes, hashes []string, err error) {
	for len(codes) < RecoveryCount {
		out := make([]byte, 0, 14)
		for n := 0; n < 12; {
			var raw [16]byte
			if _, err = rand.Read(raw[:]); err != nil {
				return nil, nil, err
			}
			for _, b := range raw {
				if int(b) >= 248 { // 248 = 31 * 8, so every character is equally likely
					continue
				}
				if n > 0 && n%4 == 0 {
					out = append(out, '-')
				}
				out = append(out, recoveryAlphabet[int(b)%len(recoveryAlphabet)])
				if n++; n == 12 {
					break
				}
			}
		}
		codes = append(codes, string(out))
		hashes = append(hashes, HashRecovery(string(out)))
	}
	return codes, hashes, nil
}

// NormalizeRecovery lower-cases a typed code and drops spaces and dashes.
func NormalizeRecovery(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '-' || unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, s)
}

// HashRecovery is what is stored for a recovery code: SHA-256 of its normalized form.
func HashRecovery(code string) string {
	h := sha256.Sum256([]byte(NormalizeRecovery(code)))
	return hex.EncodeToString(h[:])
}

// LooksLikeRecovery reports whether typed text has the shape of a recovery code rather than a
// 6-digit code, so the sign-in page can accept either in one box.
func LooksLikeRecovery(s string) bool { return len(NormalizeRecovery(s)) == 12 }
