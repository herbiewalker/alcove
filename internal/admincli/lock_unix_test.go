//go:build unix

package admincli

import (
	"strings"
	"testing"
	"time"

	"github.com/herbiewalker/alcove/internal/store"
)

// The server must be stopped: with it running, the command is refused and changes nothing.
func TestResetRefusedWhileTheServerHoldsTheDataDirectory(t *testing.T) {
	r := newRig(t)
	st, err := store.Open(store.Config{DataDir: r.cfg.DataDir, TmpDir: r.cfg.TmpDir, Key: r.key, AllowNonTmpfsTmp: true, SealEvery: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	code, _, errs := run(r, "a long enough password\n", "-user", "alice")
	_ = st.Close()
	if code != 1 || !strings.Contains(errs, "Stop it first") {
		t.Fatalf("%d %s", code, errs)
	}
	u, _ := r.user("alice")
	if u.MustChange {
		t.Error("the refused command changed the account")
	}
}
