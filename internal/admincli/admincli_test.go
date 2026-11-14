package admincli

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/herbiewalker/alcove/internal/adminlog"
	"github.com/herbiewalker/alcove/internal/auth"
	"github.com/herbiewalker/alcove/internal/config"
	"github.com/herbiewalker/alcove/internal/keyfile"
	"github.com/herbiewalker/alcove/internal/store"
)

var fast = auth.Params{Memory: 8, Time: 1, Threads: 1}

type rig struct {
	t   *testing.T
	cfg config.Config
	key keyfile.Key
}

func newRig(t *testing.T) *rig {
	t.Helper()
	Params = fast
	t.Cleanup(func() { Params = auth.DefaultParams })
	root := t.TempDir()
	k, line, err := keyfile.Generate()
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(root, "keys", "alcove.key")
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := keyfile.WriteNew(keyPath, line); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{DataDir: filepath.Join(root, "data"), KeyFile: keyPath, TmpDir: filepath.Join(root, "tmp"), AllowNonTmpfsTmp: true, SealEvery: time.Hour}
	st, err := store.Open(store.Config{DataDir: cfg.DataDir, TmpDir: cfg.TmpDir, Key: k, AllowNonTmpfsTmp: true, SealEvery: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	h, _ := auth.Hash("old password here", fast)
	if err := st.Update(context.Background(), true, func(tx *sql.Tx) error {
		if _, err := auth.Create(tx, time.Now(), "alice", "Alice", auth.RoleAdmin, h); err != nil {
			return err
		}
		u, err := auth.Create(tx, time.Now(), "gone", "Gone", auth.RoleMember, h)
		if err != nil {
			return err
		}
		if err := auth.SetStatus(tx, u.ID, auth.StatusDisabled); err != nil {
			return err
		}
		return auth.MarkDeleted(tx, u.ID, time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return &rig{t: t, cfg: cfg, key: k}
}

func (r *rig) user(name string) (u auth.User, logActions []string) {
	r.t.Helper()
	st, err := store.Open(store.Config{DataDir: r.cfg.DataDir, TmpDir: r.cfg.TmpDir, Key: r.key, AllowNonTmpfsTmp: true, SealEvery: time.Hour})
	if err != nil {
		r.t.Fatal(err)
	}
	defer st.Close()
	_ = st.View(context.Background(), func(tx *sql.Tx) error {
		u, _ = auth.ByUsername(tx, name)
		es, _ := adminlog.List(tx, 50, 0)
		for _, e := range es {
			logActions = append(logActions, e.Action+"|"+e.Actor+"|"+e.Fields)
		}
		return nil
	})
	return
}

func run(r *rig, stdin string, args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := ResetPassword(args, r.cfg, strings.NewReader(stdin), &out, &errb)
	return code, out.String(), errb.String()
}

func TestResetFromStandardInput(t *testing.T) {
	r := newRig(t)
	code, out, errs := run(r, "a brand new password\n", "-user", "alice")
	if code != 0 || !strings.Contains(out, "must be changed at the next sign-in") {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	if strings.Contains(out, "brand new") {
		t.Error("the password was echoed")
	}
	u, acts := r.user("alice")
	if ok, _ := auth.Verify("a brand new password", u.PWHash, fast); !ok || !u.MustChange {
		t.Fatalf("the new password does not work or is not forced to change: %+v", u)
	}
	if ok, _ := auth.Verify("old password here", u.PWHash, fast); ok {
		t.Error("the old password still works")
	}
	found := false
	for _, a := range acts {
		if a == "user.password.reset.cli|system|password" {
			found = true
		}
		if strings.Contains(a, "brand new") {
			t.Error("the password is in the log")
		}
	}
	if !found {
		t.Errorf("the reset is not in the admin log: %v", acts)
	}
}

func TestResetGenerate(t *testing.T) {
	r := newRig(t)
	code, out, errs := run(r, "", "-user", "ALICE", "-generate")
	if code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	var temp string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "temporary password") {
			temp = strings.TrimSpace(l[strings.LastIndex(l, ":")+1:])
		}
	}
	u, _ := r.user("alice")
	if ok, _ := auth.Verify(temp, u.PWHash, fast); !ok || temp == "" {
		t.Fatalf("the printed temporary password %q does not work", temp)
	}
}

func TestResetRefusals(t *testing.T) {
	r := newRig(t)
	for label, c := range map[string]struct {
		stdin string
		args  []string
		code  int
		want  string
	}{
		"no user flag":    {"x\n", []string{}, 2, "-user is required"},
		"unknown account": {"a long enough password\n", []string{"-user", "nobody"}, 1, "no account named"},
		"deleted account": {"a long enough password\n", []string{"-user", "gone"}, 1, "deleted"},
		"weak password":   {"short\n", []string{"-user", "alice"}, 2, "at least"},
		"no password":     {"", []string{"-user", "alice"}, 2, "standard input"},
	} {
		code, _, errs := run(r, c.stdin, c.args...)
		if code != c.code || !strings.Contains(errs, c.want) {
			t.Errorf("%s: %d %q", label, code, errs)
		}
	}
	// None of the refusals changed anything.
	u, _ := r.user("alice")
	if ok, _ := auth.Verify("old password here", u.PWHash, fast); !ok || u.MustChange {
		t.Errorf("a refused reset changed the account: %+v", u)
	}
}

func TestResetWithAWrappedKey(t *testing.T) {
	r := newRig(t)
	wrapped, err := keyfile.Wrap(r.key, "key passphrase here", keyfile.TestParams())
	if err != nil {
		t.Fatal(err)
	}
	r.cfg.KeyFile = filepath.Join(t.TempDir(), "wrapped.key")
	if err := keyfile.WriteNew(r.cfg.KeyFile, wrapped); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := run(r, "", "-user", "alice", "-generate"); code != 2 || !strings.Contains(errs, "passphrase") {
		t.Fatalf("no passphrase: %d %s", code, errs)
	}
	if code, _, _ := run(r, "wrong passphrase!!\n", "-user", "alice", "-generate"); code != 1 {
		t.Fatalf("wrong passphrase: %d", code)
	}
}
