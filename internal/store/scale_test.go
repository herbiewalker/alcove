package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"
)

// TestScale_RAMAndSealTime measures what D-16 assumes: a sealed in-memory database stays small and
// fast to seal at household scale. It is skipped unless ALCOVE_SCALE is set to a row count, for
// example `ALCOVE_SCALE=50000 go test ./internal/store/ -run TestScale -v`.
func TestScale_RAMAndSealTime(t *testing.T) {
	raw := os.Getenv("ALCOVE_SCALE")
	if raw == "" {
		t.Skip("set ALCOVE_SCALE=<rows> to measure RAM and seal time")
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		t.Fatalf("ALCOVE_SCALE=%q", raw)
	}
	d, k := newDirs(t), newKey(t)
	s := open(t, d, k)
	ctx := context.Background()

	start := time.Now()
	err = s.Update(ctx, false, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`CREATE TABLE bench (id TEXT PRIMARY KEY, owner_id TEXT NOT NULL, start_at INTEGER NOT NULL, note TEXT, rating INTEGER, body TEXT)`); err != nil {
			return err
		}
		ins, err := tx.Prepare(`INSERT INTO bench(id, owner_id, start_at, note, rating, body) VALUES (?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer ins.Close()
		fts, err := tx.Prepare(`INSERT INTO search(owner_id, kind, ref, body) VALUES (?,?,?,?)`)
		if err != nil {
			return err
		}
		defer fts.Close()
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
			owner := fmt.Sprintf("user%d", i%5)
			note := fmt.Sprintf("Synthetic entry %d with some ordinary words to index and search later", i)
			row := fmt.Sprintf(`{"id":"%s","startAt":%d,"durationMin":%d,"rating":%d,"note":"%s"}`, id, 1_600_000_000_000+int64(i)*3_600_000, i%120, i%5+1, note)
			if _, err := ins.Exec(id, owner, 1_600_000_000_000+int64(i)*3_600_000, note, i%5+1, row); err != nil {
				return err
			}
			if _, err := fts.Exec(owner, "note", id, note); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	loadTime := time.Since(start)

	sealStart := time.Now()
	if err := s.SealNow(true); err != nil {
		t.Fatal(err)
	}
	sealTime := time.Since(sealStart)

	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	live, _ := os.Stat(s.dbPath)
	sealed, _ := os.Stat(d.data + string(os.PathSeparator) + "alcove.sealed")

	searchStart := time.Now()
	hits, err := s.Search(ctx, nil, "ordinary words", 50)
	if err != nil {
		t.Fatal(err)
	}
	searchTime := time.Since(searchStart)

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopenStart := time.Now()
	s2 := open(t, d, k)
	reopenTime := time.Since(reopenStart)
	_ = s2.Close()

	t.Logf("rows=%d (plus %d search entries)", n, n)
	t.Logf("load %v | seal %v | reopen (unseal+open) %v | search %v (%d hits)", loadTime, sealTime, reopenTime, searchTime, len(hits))
	t.Logf("live database %.1f MiB | sealed file %.1f MiB | Go heap in use %.1f MiB", float64(live.Size())/(1<<20), float64(sealed.Size())/(1<<20), float64(ms.HeapAlloc)/(1<<20))
}
