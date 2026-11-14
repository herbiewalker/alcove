package auth

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/herbiewalker/alcove/internal/keyfile"
	"github.com/herbiewalker/alcove/internal/store"
)

var fast = Params{Memory: 8, Time: 1, Threads: 1}

func TestHashVerify(t *testing.T) {
	h, err := Hash("correct horse battery", fast)
	if err != nil || !strings.HasPrefix(h, "$argon2id$v=19$m=8,t=1,p=1$") {
		t.Fatalf("hash: %q %v", h, err)
	}
	if ok, rehash := Verify("correct horse battery", h, fast); !ok || rehash {
		t.Fatalf("right password: ok=%v rehash=%v", ok, rehash)
	}
	if ok, _ := Verify("correct horse batterY", h, fast); ok {
		t.Fatal("wrong password verified")
	}
	h2, _ := Hash("correct horse battery", fast)
	if h == h2 {
		t.Fatal("two hashes of the same password must differ (random salt)")
	}
}

func TestVerifyFlagsWeakerStoredParams(t *testing.T) {
	h, _ := Hash("a long enough password", fast)
	ok, rehash := Verify("a long enough password", h, Params{Memory: 16, Time: 2, Threads: 1})
	if !ok || !rehash {
		t.Fatalf("a hash made with weaker settings must verify and ask for a rehash: ok=%v rehash=%v", ok, rehash)
	}
}

func TestVerifyRejectsMalformedAndAbsurdHashes(t *testing.T) {
	for _, bad := range []string{
		"", "plaintext", "$argon2id$", "$bcrypt$v=19$m=8,t=1,p=1$AAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"$argon2id$v=19$m=99999999,t=1,p=1$AAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", // would eat all RAM
		"$argon2id$v=19$m=8,t=9999,p=1$AAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"$argon2id$v=19$m=8,t=1,p=1$!!$!!",
	} {
		if ok, _ := Verify("x", bad, fast); ok {
			t.Fatalf("%q verified", bad)
		}
	}
}

func TestVerifyBoundsPasswordLength(t *testing.T) {
	h, _ := Hash("a long enough password", fast)
	if ok, _ := Verify(strings.Repeat("a", MaxPasswordLen+1), h, fast); ok {
		t.Fatal("an oversize password must be refused without hashing")
	}
}

func TestPasswordPolicy(t *testing.T) {
	for _, tc := range []struct {
		pw, user string
		ok       bool
	}{
		{"short", "", false},
		{"123456789", "", false},
		{"exactly-10", "", true},
		{"ten chars!", "", true},
		{"alexander99", "alexander99", false},
		{"ALEXANDER99", "alexander99", false},
		{strings.Repeat("x", MaxPasswordLen), "", true},
		{strings.Repeat("x", MaxPasswordLen+1), "", false},
		{"päßwörd-längér", "", true},
	} {
		err := CheckPassword(tc.pw, tc.user)
		if (err == nil) != tc.ok {
			t.Errorf("%q: err=%v want ok=%v", tc.pw, err, tc.ok)
		}
		if err != nil && !errors.Is(err, ErrPasswordPolicy) {
			t.Errorf("%q: not a policy error: %v", tc.pw, err)
		}
	}
}

func TestUsernames(t *testing.T) {
	for in, want := range map[string]string{"Alex": "alex", "  sam.j  ": "sam.j", "a_b-c.9": "a_b-c.9"} {
		if got, err := NormalizeUsername(in); err != nil || got != want {
			t.Errorf("%q -> %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "ab", "-lead", "has space", "semi;colon", "Zoë", strings.Repeat("a", 33), "x\n"} {
		if _, err := NormalizeUsername(bad); err == nil {
			t.Errorf("%q accepted", bad)
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

func upd(t *testing.T, s *store.Store, fn func(*sql.Tx) error) error {
	t.Helper()
	return s.Update(context.Background(), true, fn)
}

func TestUserLifecycle(t *testing.T) {
	s := openStore(t)
	now := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	var u User
	if err := upd(t, s, func(tx *sql.Tx) (err error) {
		u, err = Create(tx, now, "Alex", "Alex", RoleAdmin, "hash")
		return
	}); err != nil || u.Username != "alex" || u.Status != StatusActive || u.ID == "" {
		t.Fatalf("create: %+v %v", u, err)
	}
	// Duplicate, case-insensitive.
	if err := upd(t, s, func(tx *sql.Tx) error { _, err := Create(tx, now, "ALEX", "", RoleMember, "h"); return err }); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("duplicate: %v", err)
	}
	// Bad role and bad name never reach the table.
	if err := upd(t, s, func(tx *sql.Tx) error { _, err := Create(tx, now, "sam", "", "root", "h"); return err }); err == nil {
		t.Fatal("unknown role accepted")
	}
	err := upd(t, s, func(tx *sql.Tx) error {
		got, err := ByUsername(tx, "  ALEX ")
		if err != nil || got.ID != u.ID || got.DisplayName != "Alex" {
			t.Errorf("by username: %+v %v", got, err)
		}
		if _, err := ByUsername(tx, "nobody"); !errors.Is(err, ErrNoUser) {
			t.Errorf("missing user: %v", err)
		}
		if err := TouchLogin(tx, u.ID, now.Add(time.Hour)); err != nil {
			return err
		}
		if err := SetStatus(tx, u.ID, StatusDisabled); err != nil {
			return err
		}
		if err := SetPassword(tx, u.ID, "newhash"); err != nil {
			return err
		}
		got, _ = ByID(tx, u.ID)
		if got.Status != StatusDisabled || got.PWHash != "newhash" || !got.LastLoginAt.Equal(now.Add(time.Hour)) {
			t.Errorf("after updates: %+v", got)
		}
		if err := SetStatus(tx, "missing", StatusActive); !errors.Is(err, ErrNoUser) {
			t.Errorf("update of a missing user: %v", err)
		}
		if err := SetStatus(tx, u.ID, "banished"); err == nil {
			t.Error("unknown status accepted")
		}
		n, _ := Count(tx)
		if n != 1 {
			t.Errorf("count %d", n)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSessionsExpireAndRevoke(t *testing.T) {
	clock := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	ss := NewSessions(func() time.Time { return clock })
	a, _ := ss.Issue("u1", "10.0.0.5")
	b, _ := ss.Issue("u1", "10.0.0.6")
	c, _ := ss.Issue("u2", "10.0.0.7")
	if a.ID == b.ID || a.CSRF == b.CSRF || a.ID == a.CSRF {
		t.Fatal("ids and csrf tokens must be distinct random values")
	}
	if _, err := ss.Get(a.ID); err != nil {
		t.Fatal(err)
	}
	// Idle timeout rolls with use.
	clock = clock.Add(IdleTTL - time.Minute)
	if _, err := ss.Get(a.ID); err != nil {
		t.Fatalf("still within idle: %v", err)
	}
	clock = clock.Add(IdleTTL - time.Minute)
	if _, err := ss.Get(a.ID); err != nil {
		t.Fatalf("idle timer should have rolled: %v", err)
	}
	// b was last seen at the start, so it is idle-expired by now.
	if _, err := ss.Get(b.ID); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("idle expiry: %v", err)
	}
	// Absolute cap does not roll.
	for i := 0; i < 30; i++ {
		clock = clock.Add(25 * time.Minute)
		if _, err := ss.Get(a.ID); err != nil {
			if !errors.Is(err, ErrSessionExpired) {
				t.Fatal(err)
			}
			if clock.Sub(a.CreatedAt) <= MaxTTL {
				t.Fatalf("expired too early at %v", clock.Sub(a.CreatedAt))
			}
			goto capped
		}
	}
	t.Fatal("the absolute cap never fired")
capped:
	if _, err := ss.Get("nonsense"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("unknown id: %v", err)
	}
	c2, _ := ss.Issue("u2", "x")
	if n := ss.RevokeUserExcept("u2", c2.ID); n != 1 {
		t.Fatalf("revoke except: %d", n)
	}
	_ = c
	if n := ss.RevokeUser("u2"); n != 1 || ss.Count() != 0 {
		t.Fatalf("revoke: %d, left %d", n, ss.Count())
	}
}

func TestStepUpExpires(t *testing.T) {
	clock := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	ss := NewSessions(func() time.Time { return clock })
	s, _ := ss.Issue("u1", "x")
	if s.SteppedUp(clock) {
		t.Fatal("a new session must not be stepped up")
	}
	ss.StepUp(s.ID)
	got, _ := ss.Get(s.ID)
	if !got.SteppedUp(clock) || !got.SteppedUp(clock.Add(StepUpTTL-time.Second)) {
		t.Fatal("step-up not honoured")
	}
	if got.SteppedUp(clock.Add(StepUpTTL + time.Second)) {
		t.Fatal("step-up must lapse")
	}
}

func TestLimiter(t *testing.T) {
	clock := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	l := &Limiter{Capacity: 3, RefillEvery: time.Minute, Now: func() time.Time { return clock }}
	for i := 0; i < 3; i++ {
		if !l.Allow("a") {
			t.Fatalf("burst attempt %d refused", i)
		}
	}
	if l.Allow("a") {
		t.Fatal("fourth attempt allowed")
	}
	if !l.Allow("b") {
		t.Fatal("another key must have its own bucket")
	}
	clock = clock.Add(61 * time.Second)
	if !l.Allow("a") || l.Allow("a") {
		t.Fatal("exactly one token should have refilled")
	}
	l.Reset("a")
	if !l.Allow("a") {
		t.Fatal("reset should forgive")
	}
}

func TestTempPassword(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		p, err := TempPassword()
		if err != nil {
			t.Fatal(err)
		}
		if len(p) != 19 || strings.Count(p, "-") != 3 || seen[p] {
			t.Fatalf("bad or repeated temporary password %q", p)
		}
		if strings.ContainsAny(p, "ilo01") {
			t.Fatalf("look-alike characters in %q", p)
		}
		if err := CheckPassword(p, "someone"); err != nil {
			t.Fatalf("a temporary password must satisfy the policy: %v", err)
		}
		seen[p] = true
	}
}

func TestDeletionFlagsAndAdminCount(t *testing.T) {
	s := openStore(t)
	now := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	var a, b, m User
	if err := upd(t, s, func(tx *sql.Tx) (err error) {
		if a, err = Create(tx, now, "admina", "A", RoleAdmin, "h"); err != nil {
			return err
		}
		if b, err = Create(tx, now, "adminb", "B", RoleAdmin, "h"); err != nil {
			return err
		}
		m, err = Create(tx, now, "member", "M", RoleMember, "h")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	err := upd(t, s, func(tx *sql.Tx) error {
		if n, _ := CountActiveAdmins(tx); n != 2 {
			t.Errorf("admins = %d", n)
		}
		// Deleting requires disabled first.
		if err := MarkDeleted(tx, m.ID, now); !errors.Is(err, ErrNoUser) {
			t.Errorf("deleting an active account must be refused: %v", err)
		}
		if err := SetStatus(tx, m.ID, StatusDisabled); err != nil {
			return err
		}
		if err := MarkDeleted(tx, m.ID, now); err != nil {
			return err
		}
		got, _ := ByID(tx, m.ID)
		if !got.Deleted() || !got.DeletedAt.Equal(now) {
			t.Errorf("deleted flags: %+v", got)
		}
		if err := Undelete(tx, m.ID); err != nil {
			return err
		}
		if got, _ = ByID(tx, m.ID); got.Deleted() {
			t.Error("undelete did not clear the flag")
		}
		// A disabled or deleted admin does not count.
		_ = SetStatus(tx, a.ID, StatusDisabled)
		if n, _ := CountActiveAdmins(tx); n != 1 {
			t.Errorf("admins after disabling one = %d", n)
		}
		// Forced change survives a login-time rehash but not the person's own change.
		if err := SetPasswordForced(tx, b.ID, "temp", true); err != nil {
			return err
		}
		_ = SetPassword(tx, b.ID, "rehashed")
		if got, _ = ByID(tx, b.ID); !got.MustChange || got.PWHash != "rehashed" {
			t.Errorf("rehash cleared the forced change: %+v", got)
		}
		_ = ChangePassword(tx, b.ID, "mine")
		if got, _ = ByID(tx, b.ID); got.MustChange || got.PWHash != "mine" {
			t.Errorf("own change did not clear it: %+v", got)
		}
		list, err := List(tx)
		if err != nil || len(list) != 3 {
			t.Errorf("list: %d %v", len(list), err)
		}
		if err := Remove(tx, m.ID); err != nil {
			return err
		}
		if _, err := ByID(tx, m.ID); !errors.Is(err, ErrNoUser) {
			t.Errorf("removed account still there: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
