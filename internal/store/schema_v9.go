package store

import (
	"database/sql"
	"fmt"
)

// migrateV9 is M7 (D-39): the photos copied out of an owner's current upload, and the member's
// choice to show where their photos were taken. media_blob is derived and rebuildable; the files
// themselves are in the blob store, encrypted under the owner's data key.
func migrateV9(tx *sql.Tx) error {
	stmts := []string{
		`CREATE TABLE media_blob (
			owner_id TEXT NOT NULL,
			media_id TEXT NOT NULL,
			entry_id TEXT NOT NULL,
			mime TEXT NOT NULL,
			kind INTEGER NOT NULL DEFAULT 0,
			size INTEGER NOT NULL,
			PRIMARY KEY (owner_id, media_id)
		) WITHOUT ROWID`,
		`CREATE INDEX media_blob_entry ON media_blob(owner_id, entry_id)`,
		`ALTER TABLE membership ADD COLUMN show_photo_location INTEGER NOT NULL DEFAULT 0`,
	}
	for _, q := range stmts {
		if _, err := tx.Exec(q); err != nil {
			return fmt.Errorf("%w: %s", err, q)
		}
	}
	return nil
}
