package totp

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/herbiewalker/alcove/internal/auth"
	"github.com/herbiewalker/alcove/internal/keyfile"
	"github.com/herbiewalker/alcove/internal/store"
)

// RFC 6238 appendix B, SHA-1, secret "12345678901234567890". The RFC lists 8-digit codes; the 6-digit
// code is the last six digits of the same number.
func TestRFC6238Vectors(t *testing.T) {
	secret := []byte("12345678901234567890")
	for unix, want := range map[int64]string{
		59:          "287082",
		1111111109:  "081804",
		1111111111:  "050471",
		1234567890:  "005924",
		2000000000:  "279037",
		20000000000: "353130",
	} {
		if got := Code(secret, Step(time.Unix(unix, 0))); got != want {
			t.Errorf("t=%d: %s, want %s", unix, got, want)
		}
	}
}

func TestVerifyAcceptsOneStepOfDriftAndNeverAReplay(t *testing.T) {
	secret := []byte("12345678901234567890")
	now := time.Unix(1111111111, 0)
	cur := Step(now)

	if s, ok := Verify(secret, Code(secret, cur), now, 0); !ok || s != cur {
		t.Fatalf("the current code: %d %v", s, ok)
	}
	for _, d := range []int64{-1, 1} {
		if _, ok := Verify(secret, Code(secret, cur+d), now, 0); !ok {
			t.Errorf("a code %d step away was refused", d)
		}
	}
	for _, d := range []int64{-2, 2, 10} {
		if _, ok := Verify(secret, Code(secret, cur+d), now, 0); ok {
			t.Errorf("a code %d steps away was accepted", d)
		}
	}
	// A code is good once: its own step, and anything older, is refused afterwards.
	if _, ok := Verify(secret, Code(secret, cur), now, cur); ok {
		t.Error("a used code was accepted again")
	}
	if _, ok := Verify(secret, Code(secret, cur-1), now, cur); ok {
		t.Error("an older code was accepted after a newer one")
	}
	if s, ok := Verify(secret, Code(secret, cur+1), now, cur); !ok || s != cur+1 {
		t.Error("the next code was refused after the current one was used")
	}
	// Spaces and a dash are fine; wrong shapes are not.
	c := Code(secret, cur)
	if _, ok := Verify(secret, c[:3]+" "+c[3:], now, 0); !ok {
		t.Error("a code typed with a space was refused")
	}
	for _, bad := range []string{"", "12345", "1234567", "abcdef", strings.Repeat("0", 6)} {
		if bad == Code(secret, cur) || bad == Code(secret, cur-1) || bad == Code(secret, cur+1) {
			continue
		}
		if _, ok := Verify(secret, bad, now, 0); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestNewSecretURIAndBase32(t *testing.T) {
	a, _ := NewSecret()
	b, _ := NewSecret()
	if len(a) != SecretBytes || string(a) == string(b) {
		t.Fatal("secrets must be 20 random bytes")
	}
	if got := Base32([]byte("12345678901234567890")); got != "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ" {
		t.Errorf("base32: %s", got)
	}
	u := URI("Alcove", "owner", []byte("12345678901234567890"))
	for _, want := range []string{"otpauth://totp/Alcove:owner?", "secret=GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", "issuer=Alcove", "algorithm=SHA1", "digits=6", "period=30"} {
		if !strings.Contains(u, want) {
			t.Errorf("the link lacks %q: %s", want, u)
		}
	}
	// Odd account names cannot break out of the link.
	if u := URI("Alcove", "a b/c?d=e&f", a); strings.Count(u, "?") != 1 || strings.Contains(u, " ") {
		t.Errorf("an odd name changed the link's shape: %s", u)
	}
}

func TestRecoveryCodes(t *testing.T) {
	codes, hashes, err := NewRecoveryCodes()
	if err != nil || len(codes) != RecoveryCount || len(hashes) != RecoveryCount {
		t.Fatalf("codes: %d %d %v", len(codes), len(hashes), err)
	}
	seen := map[string]bool{}
	for i, c := range codes {
		if len(c) != 14 || c[4] != '-' || c[9] != '-' || seen[c] {
			t.Errorf("odd or repeated code %q", c)
		}
		seen[c] = true
		if strings.ContainsAny(c, "0o1li") {
			t.Errorf("a look-alike character in %q", c)
		}
		if HashRecovery(strings.ToUpper(strings.ReplaceAll(c, "-", " "))) != hashes[i] {
			t.Errorf("typing %q another way gives another hash", c)
		}
		if !LooksLikeRecovery(c) || LooksLikeRecovery("123456") {
			t.Errorf("shape test wrong for %q", c)
		}
		if strings.Contains(hashes[i], c) {
			t.Error("a hash contains its code")
		}
	}
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	k, _, _ := keyfile.Generate()
	root := t.TempDir()
	s, err := store.Open(store.Config{DataDir: filepath.Join(root, "d"), TmpDir: filepath.Join(root, "t"),
		Key: k, AllowNonTmpfsTmp: true, SealEvery: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestStoredStateEnableUseRecoveryAndDisable(t *testing.T) {
	s := openStore(t)
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	upd := func(fn func(*sql.Tx) error) error { return s.Update(context.Background(), true, fn) }

	var uid string
	if err := upd(func(tx *sql.Tx) error {
		u, err := auth.Create(tx, now, "owner", "Owner", auth.RoleAdmin, "hash")
		uid = u.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	secret, _ := NewSecret()
	codes, hashes, _ := NewRecoveryCodes()

	if err := upd(func(tx *sql.Tx) error {
		if on, _ := Enabled(tx, uid); on {
			t.Error("enabled before anything was set up")
		}
		if _, _, ok, _ := Secret(tx, uid); ok {
			t.Error("a secret exists before setup")
		}
		return Enable(tx, now, uid, secret, 100, hashes)
	}); err != nil {
		t.Fatal(err)
	}
	if err := upd(func(tx *sql.Tx) error {
		got, last, ok, err := Secret(tx, uid)
		if err != nil || !ok || string(got) != string(secret) || last != 100 {
			t.Errorf("secret read back: %v %d %v %v", got, last, ok, err)
		}
		// A step is accepted once; the same or an older one is a replay.
		if err := AcceptStep(tx, uid, 101); err != nil {
			t.Errorf("a newer step: %v", err)
		}
		if err := AcceptStep(tx, uid, 101); !errors.Is(err, ErrReplay) {
			t.Errorf("the same step again: %v", err)
		}
		if err := AcceptStep(tx, uid, 50); !errors.Is(err, ErrReplay) {
			t.Errorf("an older step: %v", err)
		}
		// A recovery code works once, however it is typed.
		if left, _ := RecoveryLeft(tx, uid); left != RecoveryCount {
			t.Errorf("codes left: %d", left)
		}
		ok, left, err := UseRecovery(tx, now, uid, strings.ToUpper(codes[0]))
		if err != nil || !ok || left != RecoveryCount-1 {
			t.Errorf("first use: %v %d %v", ok, left, err)
		}
		if ok, left, _ := UseRecovery(tx, now, uid, codes[0]); ok || left != RecoveryCount-1 {
			t.Errorf("second use: %v %d", ok, left)
		}
		if ok, _, _ := UseRecovery(tx, now, uid, "aaaa-aaaa-aaaa"); ok {
			t.Error("a made-up code worked")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// New recovery codes replace the old ones, used or not.
	newCodes, newHashes, _ := NewRecoveryCodes()
	if err := upd(func(tx *sql.Tx) error {
		if err := SetRecovery(tx, uid, newHashes); err != nil {
			return err
		}
		if ok, _, _ := UseRecovery(tx, now, uid, codes[1]); ok {
			t.Error("an old code worked after new ones were made")
		}
		if ok, _, _ := UseRecovery(tx, now, uid, newCodes[0]); !ok {
			t.Error("a new code did not work")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Turning it off forgets everything.
	if err := upd(func(tx *sql.Tx) error { return Disable(tx, uid) }); err != nil {
		t.Fatal(err)
	}
	if err := upd(func(tx *sql.Tx) error {
		if on, _ := Enabled(tx, uid); on {
			t.Error("still enabled after Disable")
		}
		if n, _ := RecoveryLeft(tx, uid); n != 0 {
			t.Errorf("%d recovery codes remain after Disable", n)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// Removing an account removes its second factor with it.
func TestRemovingAnAccountRemovesItsSecondFactor(t *testing.T) {
	s := openStore(t)
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	var uid string
	_ = s.Update(context.Background(), true, func(tx *sql.Tx) error {
		u, err := auth.Create(tx, now, "kim", "Kim", auth.RoleAdmin, "hash")
		uid = u.ID
		secret, _ := NewSecret()
		_, hashes, _ := NewRecoveryCodes()
		if err != nil {
			return err
		}
		return Enable(tx, now, uid, secret, 0, hashes)
	})
	if err := s.Update(context.Background(), true, func(tx *sql.Tx) error { return auth.Remove(tx, uid) }); err != nil {
		t.Fatalf("remove: %v", err)
	}
	_ = s.View(context.Background(), func(tx *sql.Tx) error {
		var a, b int
		_ = tx.QueryRow(`SELECT COUNT(*) FROM user_totp`).Scan(&a)
		_ = tx.QueryRow(`SELECT COUNT(*) FROM user_recovery`).Scan(&b)
		if a != 0 || b != 0 {
			t.Errorf("left behind: %d secrets, %d recovery rows", a, b)
		}
		return nil
	})
}
