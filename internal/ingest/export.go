package ingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/herbiewalker/alcove/internal/edit"
	"github.com/herbiewalker/alcove/internal/export"
)

// ErrCacheStale means the owner's rows are not those of their current revision, so an export would
// not say what the pages show. A rebuild brings them back in step.
var ErrCacheStale = errors.New("the stored rows are out of date; rebuild first")

// Export writes the owner's current data as a new backup sealed under password (D-43). The rows are
// the cache, which is the head upload with every live edit replayed; the photos, settings and
// anything Alcove does not edit come from the stored upload itself, opened with uploadPassword (the
// remembered one when it is empty). The upload is never changed, and nothing is written to disk.
func (s *Service) Export(ctx context.Context, owner, uploadPassword, password string) (export.Result, error) {
	var uploadID, sum string
	var tables []export.Table
	err := s.St.View(ctx, func(tx *sql.Tx) error {
		revID, up, err := edit.Head(tx, owner)
		if errors.Is(err, edit.ErrNoHead) {
			return ErrNoRevision
		}
		if err != nil {
			return err
		}
		uploadID = up
		if err := tx.QueryRow(`SELECT sha256 FROM upload WHERE id = ?`, up).Scan(&sum); err != nil {
			return err
		}
		var cacheRev, order string
		err = tx.QueryRow(`SELECT revision_id, table_order FROM cache_head WHERE owner_id = ?`, owner).Scan(&cacheRev, &order)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && cacheRev != revID) {
			return ErrCacheStale
		}
		if err != nil {
			return err
		}
		var names []string
		if err := json.Unmarshal([]byte(order), &names); err != nil {
			return err
		}
		for _, n := range names {
			t := export.Table{Name: n}
			rows, err := tx.Query(`SELECT data FROM cache_row WHERE owner_id = ? AND tbl = ? ORDER BY pos`, owner, n)
			if err != nil {
				return err
			}
			for rows.Next() {
				var d string
				if err := rows.Scan(&d); err != nil {
					rows.Close()
					return err
				}
				t.Rows = append(t.Rows, d)
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return err
			}
			rows.Close()
			tables = append(tables, t)
		}
		return nil
	})
	if err != nil {
		return export.Result{}, err
	}
	if uploadPassword == "" {
		if uploadPassword, err = s.StoredPassword(ctx, owner); err != nil {
			return export.Result{}, err
		}
	}
	bk, err := s.read(ctx, owner, uploadID, sum, uploadPassword)
	if err != nil {
		return export.Result{}, err
	}
	defer bk.Close()
	return export.Build(export.Plan{Base: bk, Tables: tables, Password: password})
}
