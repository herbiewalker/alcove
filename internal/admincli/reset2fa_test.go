package admincli

import (
	"bytes"
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/herbiewalker/alcove/internal/store"
	"github.com/herbiewalker/alcove/internal/totp"
)

func run2fa(r *rig, stdin string, args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := ResetTwoStep(args, r.cfg, strings.NewReader(stdin), &out, &errb)
	return code, out.String(), errb.String()
}

// turnOn gives a person two-step sign-in directly in the store, as the setup page would.
func (r *rig) turnOn(name string) {
	r.t.Helper()
	st, err := store.Open(store.Config{DataDir: r.cfg.DataDir, TmpDir: r.cfg.TmpDir, Key: r.key, AllowNonTmpfsTmp: true, SealEvery: time.Hour})
	if err != nil {
		r.t.Fatal(err)
	}
	defer st.Close()
	secret, _ := totp.NewSecret()
	_, hashes, _ := totp.NewRecoveryCodes()
	if err := st.Update(context.Background(), true, func(tx *sql.Tx) error {
		var uid string
		if err := tx.QueryRow(`SELECT id FROM user WHERE username = ?`, name).Scan(&uid); err != nil {
			return err
		}
		return totp.Enable(tx, time.Now(), uid, secret, 0, hashes)
	}); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) twoOn(name string) (on bool, left int) {
	r.t.Helper()
	st, err := store.Open(store.Config{DataDir: r.cfg.DataDir, TmpDir: r.cfg.TmpDir, Key: r.key, AllowNonTmpfsTmp: true, SealEvery: time.Hour})
	if err != nil {
		r.t.Fatal(err)
	}
	defer st.Close()
	_ = st.View(context.Background(), func(tx *sql.Tx) error {
		var uid string
		_ = tx.QueryRow(`SELECT id FROM user WHERE username = ?`, name).Scan(&uid)
		on, _ = totp.Enabled(tx, uid)
		left, _ = totp.RecoveryLeft(tx, uid)
		return nil
	})
	return
}

func TestReset2FATurnsItOffAndLeavesThePasswordAlone(t *testing.T) {
	r := newRig(t)
	r.turnOn("alice")
	if on, left := r.twoOn("alice"); !on || left != totp.RecoveryCount {
		t.Fatalf("setup: on=%v left=%d", on, left)
	}
	before, _ := r.user("alice")

	code, out, errs := run2fa(r, "", "-user", "alice")
	if code != 0 || !strings.Contains(out, "is off for @alice") || !strings.Contains(out, "password is unchanged") {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	if on, left := r.twoOn("alice"); on || left != 0 {
		t.Errorf("after the reset: on=%v left=%d", on, left)
	}
	after, acts := r.user("alice")
	if after.PWHash != before.PWHash || after.MustChange != before.MustChange {
		t.Error("the reset touched the password")
	}
	found := false
	for _, a := range acts {
		if strings.HasPrefix(a, "user.totp.reset.cli|") {
			found = true
			if !strings.Contains(a, "|system|") {
				t.Errorf("the log line should be by the system actor: %s", a)
			}
		}
	}
	if !found {
		t.Errorf("the reset is not in the admin log: %v", acts)
	}
	// A second run changes nothing and says so.
	if code, out, _ := run2fa(r, "", "-user", "alice"); code != 0 || !strings.Contains(out, "was not on") {
		t.Errorf("second run: %d %s", code, out)
	}
}

func TestReset2FARefusalsChangeNothing(t *testing.T) {
	r := newRig(t)
	r.turnOn("alice")
	for label, c := range map[string]struct {
		args []string
		code int
		want string
	}{
		"no user flag":    {[]string{}, 2, "-user is required"},
		"unknown account": {[]string{"-user", "nobody"}, 1, "no account named"},
		"deleted account": {[]string{"-user", "gone"}, 1, "deleted"},
	} {
		code, _, errs := run2fa(r, "", c.args...)
		if code != c.code || !strings.Contains(errs, c.want) {
			t.Errorf("%s: %d %q", label, code, errs)
		}
	}
	if on, _ := r.twoOn("alice"); !on {
		t.Error("a refused reset turned two-step off")
	}
}
