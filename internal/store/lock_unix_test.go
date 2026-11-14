//go:build unix

package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// D-30: two processes must never share a data directory. The second is refused, and it must not
// disturb the first (it would otherwise delete the first one's live database from tmpfs).
func TestDataDirectoryLock(t *testing.T) {
	d, k := newDirs(t), newKey(t)
	s := open(t, d, k)
	if err := s.Update(context.Background(), true, func(tx *sql.Tx) error { return nil }); err != nil {
		t.Fatal(err)
	}
	_, err := Open(cfg(d, k))
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("a second Open on the same data directory: %v", err)
	}
	// The first store is untouched and still works.
	if err := s.View(context.Background(), func(tx *sql.Tx) error {
		var n int
		return tx.QueryRow(`SELECT COUNT(*) FROM schema_info`).Scan(&n)
	}); err != nil {
		t.Fatalf("the first store was disturbed by the refused second open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(cfg(d, k)) // released on Close
	if err != nil {
		t.Fatalf("Open after Close: %v", err)
	}
	_ = s2.Close()
}

func TestFailedOpenReleasesTheLock(t *testing.T) {
	d, k := newDirs(t), newKey(t)
	s := open(t, d, k)
	_ = s.Close()
	if _, err := Open(cfg(d, newKey(t))); err == nil { // wrong key
		t.Fatal("wrong key accepted")
	}
	s2, err := Open(cfg(d, k))
	if err != nil {
		t.Fatalf("a failed Open left the directory locked: %v", err)
	}
	_ = s2.Close()
}
