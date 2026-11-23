package ingest

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/herbiewalker/alcove/internal/trystbk/synth"
)

// The one slot is held for as long as a decrypted backup is in memory, not just while it decrypts
// (delta audit 2026-11-23): a second reader waits until the first has released.
func TestOnlyOneBackupIsHeldInMemoryAtATime(t *testing.T) {
	r := newRig(t)
	res := build(t, synth.Options{Seed: 5, Encounters: 5})
	if _, err := r.upload(res, true); err != nil {
		t.Fatal(err)
	}
	var id, sum string
	if err := r.st.View(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT id, sha256 FROM upload LIMIT 1`).Scan(&id, &sum)
	}); err != nil {
		t.Fatal(err)
	}

	bk, release, err := r.svc.read(context.Background(), owner, id, sum, res.Password)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, _, err := r.svc.read(ctx, owner, id, sum, res.Password); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a second backup was read while the first was still held: %v", err)
	}
	bk.Close()
	release()
	release() // a second call is harmless
	bk2, release2, err := r.svc.read(context.Background(), owner, id, sum, res.Password)
	if err != nil {
		t.Fatalf("the slot was not given back: %v", err)
	}
	bk2.Close()
	release2()

	// A refused read gives the slot back too.
	if _, _, err := r.svc.read(context.Background(), owner, id, sum, "not the password"); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("wrong password: %v", err)
	}
	bk3, release3, err := r.svc.read(context.Background(), owner, id, sum, res.Password)
	if err != nil {
		t.Fatalf("a refused read kept the slot: %v", err)
	}
	bk3.Close()
	release3()
}
