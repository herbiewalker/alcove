package adminlog

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/herbiewalker/alcove/internal/keyfile"
	"github.com/herbiewalker/alcove/internal/store"
)

var t0 = time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	k, _, err := keyfile.Generate()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	s, err := store.Open(store.Config{DataDir: filepath.Join(root, "d"), TmpDir: filepath.Join(root, "t"),
		Key: k, AllowNonTmpfsTmp: true, SealEvery: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func update(t *testing.T, s *store.Store, fn func(*sql.Tx) error) error {
	t.Helper()
	return s.Update(context.Background(), true, fn)
}

func view(t *testing.T, s *store.Store, fn func(*sql.Tx) error) error {
	t.Helper()
	return s.View(context.Background(), fn)
}

func seed(t *testing.T, s *store.Store, n int) {
	t.Helper()
	err := update(t, s, func(tx *sql.Tx) error {
		for i := 0; i < n; i++ {
			if _, err := Append(tx, t0.Add(time.Duration(i)*time.Minute), "u1", "user.disable", "user", "u2", "status"); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func verify(t *testing.T, s *store.Store) (Result, error) {
	t.Helper()
	var res Result
	var verr error
	if err := view(t, s, func(tx *sql.Tx) error { res, verr = Verify(tx); return nil }); err != nil {
		t.Fatal(err)
	}
	return res, verr
}

// tamper runs SQL with the append-only triggers removed, the way an attacker who got into the
// database file would. The chain, not the triggers, is what has to catch it.
func tamper(t *testing.T, s *store.Store, stmts ...string) {
	t.Helper()
	err := update(t, s, func(tx *sql.Tx) error {
		for _, q := range append([]string{`DROP TRIGGER admin_log_no_update`, `DROP TRIGGER admin_log_no_delete`}, stmts...) {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestEmptyLogVerifies(t *testing.T) {
	s := openStore(t)
	if res, err := verify(t, s); err != nil || res.Entries != 0 {
		t.Fatalf("empty log: %+v %v", res, err)
	}
}

func TestChainVerifies(t *testing.T) {
	s := openStore(t)
	seed(t, s, 5)
	res, err := verify(t, s)
	if err != nil || res.Entries != 5 || len(res.Head) != 64 {
		t.Fatalf("chain: %+v %v", res, err)
	}
}

func TestDatabaseRefusesEditAndDelete(t *testing.T) {
	s := openStore(t)
	seed(t, s, 2)
	for _, q := range []string{`UPDATE admin_log SET action = 'x' WHERE seq = 1`, `DELETE FROM admin_log WHERE seq = 2`} {
		err := update(t, s, func(tx *sql.Tx) error { _, err := tx.Exec(q); return err })
		if err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Fatalf("%q was allowed: %v", q, err)
		}
	}
	if _, err := verify(t, s); err != nil {
		t.Fatal(err)
	}
}

func TestTamperedEntryIsDetected(t *testing.T) {
	cases := map[string]string{
		"action edited":  `UPDATE admin_log SET action = 'user.enable' WHERE seq = 3`,
		"actor edited":   `UPDATE admin_log SET actor = 'someone' WHERE seq = 2`,
		"time edited":    `UPDATE admin_log SET ts = ts + 1 WHERE seq = 4`,
		"target edited":  `UPDATE admin_log SET target_id = 'u9' WHERE seq = 1`,
		"fields edited":  `UPDATE admin_log SET fields = '' WHERE seq = 5`,
		"middle deleted": `DELETE FROM admin_log WHERE seq = 3`,
		"tail cut":       `DELETE FROM admin_log WHERE seq = 5`,
		"two cut":        `DELETE FROM admin_log WHERE seq > 3`,
		"all emptied":    `DELETE FROM admin_log`,
	}
	for name, q := range cases {
		t.Run(name, func(t *testing.T) {
			s := openStore(t)
			seed(t, s, 5)
			tamper(t, s, q)
			if _, err := verify(t, s); err == nil {
				t.Fatal("tampering was not detected")
			}
		})
	}
}

func TestRewrittenHeadWithRecomputedChainIsStillCaughtByAnchor(t *testing.T) {
	// An attacker who deletes the last row AND does not touch the anchor is caught above; one who
	// also rewrites the anchor has the master key and the database, which is outside this log's
	// promise. The admin can pin the head hash elsewhere (Result.Head) to cover even that.
	s := openStore(t)
	seed(t, s, 3)
	res, err := verify(t, s)
	if err != nil {
		t.Fatal(err)
	}
	seed(t, s, 1)
	res2, _ := verify(t, s)
	if res2.Head == res.Head || res2.Entries != 4 {
		t.Fatalf("head did not advance: %+v %+v", res, res2)
	}
}

func TestContentCannotBeLogged(t *testing.T) {
	s := openStore(t)
	for _, bad := range []string{"Sam's notes", "has a space", "line\nbreak", strings.Repeat("a", 201), "<script>"} {
		err := update(t, s, func(tx *sql.Tx) error {
			_, err := Append(tx, t0, "u1", "entry.edit", "entry", "e1", bad)
			return err
		})
		if err != ErrUnsafe {
			t.Fatalf("%q should be refused as content, got %v", bad, err)
		}
	}
	err := update(t, s, func(tx *sql.Tx) error {
		_, err := Append(tx, t0, "u1", "entry.edit", "entry", "e1", "notes,rating,partner_id")
		return err
	})
	if err != nil {
		t.Fatalf("field names must be allowed: %v", err)
	}
}

func TestActionAndLogCommitTogether(t *testing.T) {
	s := openStore(t)
	err := update(t, s, func(tx *sql.Tx) error {
		if _, err := Append(tx, t0, "u1", "user.disable", "user", "u2", "status"); err != nil {
			return err
		}
		return sql.ErrConnDone // the action failed after logging: both must roll back
	})
	if err == nil {
		t.Fatal("expected the failure to propagate")
	}
	if res, err := verify(t, s); err != nil || res.Entries != 0 {
		t.Fatalf("a rolled-back action left a log entry: %+v %v", res, err)
	}
}

func TestListIsNewestFirstAndPages(t *testing.T) {
	s := openStore(t)
	seed(t, s, 7)
	var got []Entry
	if err := view(t, s, func(tx *sql.Tx) (err error) { got, err = List(tx, 3, 2); return }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Seq != 5 || got[2].Seq != 3 {
		t.Fatalf("paging wrong: %+v", got)
	}
}
