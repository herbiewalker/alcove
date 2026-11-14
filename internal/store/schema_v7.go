package store

import (
	"database/sql"
	"fmt"
)

// migrateV7 is M4 piece B (D-33): the member profile, one row per field, each with its own
// share flag. A field with no row is empty; shared defaults to private.
func migrateV7(tx *sql.Tx) error {
	stmts := []string{
		`CREATE TABLE profile (
			owner_id TEXT NOT NULL,
			field TEXT NOT NULL,
			value TEXT NOT NULL,
			shared INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY (owner_id, field)
		) WITHOUT ROWID`,
	}
	for _, q := range stmts {
		if _, err := tx.Exec(q); err != nil {
			return fmt.Errorf("%w: %s", err, q)
		}
	}
	return nil
}
