// Package auth holds Alcove's accounts, password hashing, sessions and login rate limit (M2b).
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Params are the Argon2id cost settings. They are stored inside every hash, so they can be raised
// later: a hash made with weaker settings still verifies and is flagged for a rehash on login.
type Params struct {
	Memory  uint32 // KiB
	Time    uint32
	Threads uint8
}

// DefaultParams is 64 MiB, three passes, two lanes: well above the OWASP minimum, and a login
// takes roughly 100 to 200 ms on a small home server.
var DefaultParams = Params{Memory: 64 * 1024, Time: 3, Threads: 2}

// Password length limits. The ceiling bounds the work an attacker can force; the floor is a
// minimum, not a quality meter (a long passphrase is encouraged on the form).
const (
	MinPasswordLen = 10
	MaxPasswordLen = 256 // bytes
)

// ErrPasswordPolicy explains a rejected new password in words the form can show.
var ErrPasswordPolicy = errors.New("password policy")

// CheckPassword applies the policy to a NEW password. Existing passwords are never re-checked.
func CheckPassword(pw, username string) error {
	switch {
	case len(pw) > MaxPasswordLen:
		return fmt.Errorf("%w: use at most %d characters", ErrPasswordPolicy, MaxPasswordLen)
	case utf8.RuneCountInString(pw) < MinPasswordLen:
		return fmt.Errorf("%w: use at least %d characters (a few words make a good passphrase)", ErrPasswordPolicy, MinPasswordLen)
	case username != "" && strings.EqualFold(pw, username):
		return fmt.Errorf("%w: the password cannot be the username", ErrPasswordPolicy)
	}
	return nil
}

// sem caps concurrent hashing so a burst of logins cannot exhaust the container's memory.
var sem = make(chan struct{}, 2)

// Hash returns a PHC-style string: $argon2id$v=19$m=..,t=..,p=..$salt$hash.
func Hash(pw string, p Params) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	sem <- struct{}{}
	key := argon2.IDKey([]byte(pw), salt, p.Time, p.Memory, p.Threads, 32)
	<-sem
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, p.Memory, p.Time, p.Threads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// Verify checks pw against an encoded hash in constant time. rehash is true when the stored hash
// uses weaker settings than want and the caller should store a fresh one.
func Verify(pw, encoded string, want Params) (ok, rehash bool) {
	if len(pw) > MaxPasswordLen {
		return false, false
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, false
	}
	var ver int
	var p Params
	var threads uint
	if _, err := fmt.Sscanf(parts[2], "v=%d", &ver); err != nil || ver != argon2.Version {
		return false, false
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Time, &threads); err != nil {
		return false, false
	}
	p.Threads = uint8(threads)
	// Refuse absurd stored settings rather than burn memory on a corrupted or planted hash.
	if p.Memory < 8 || p.Memory > 1<<20 || p.Time < 1 || p.Time > 20 || p.Threads < 1 || p.Threads > 16 {
		return false, false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[4])
	hash, err2 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil || len(hash) < 16 || len(salt) < 8 {
		return false, false
	}
	sem <- struct{}{}
	got := argon2.IDKey([]byte(pw), salt, p.Time, p.Memory, p.Threads, uint32(len(hash)))
	<-sem
	if subtle.ConstantTimeCompare(got, hash) != 1 {
		return false, false
	}
	return true, p.Memory < want.Memory || p.Time < want.Time || p.Threads < want.Threads
}

// dummy is verified against when the username does not exist, so "no such user" and "wrong
// password" take the same time and give the same answer.
var (
	dummy     string
	dummyOnce sync.Once
)

// DummyVerify burns the same work as a real verification.
func DummyVerify(pw string, p Params) {
	dummyOnce.Do(func() {
		dummy, _ = Hash("alcove-timing-equaliser", p)
	})
	Verify(pw, dummy, p)
}
