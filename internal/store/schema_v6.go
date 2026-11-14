package store

import (
	"database/sql"
	"fmt"
)

// migrateV6 is M4 piece A (D-32): the per-entry private flag. It is keyed by owner and entry id,
// not by revision, so it survives new uploads and edits.
func migrateV6(tx *sql.Tx) error {
	stmts := []string{
		`CREATE TABLE entry_flag (
			owner_id TEXT NOT NULL,
			entry_id TEXT NOT NULL,
			private INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY (owner_id, entry_id)
		) WITHOUT ROWID`,
	}
	for _, q := range stmts {
		if _, err := tx.Exec(q); err != nil {
			return fmt.Errorf("%w: %s", err, q)
		}
	}
	return nil
}
