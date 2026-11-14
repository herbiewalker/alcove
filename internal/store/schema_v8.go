package store

import (
	"database/sql"
	"fmt"
)

// migrateV8 is M4 piece D (D-35): a per-member time zone. No row means the server's default zone.
func migrateV8(tx *sql.Tx) error {
	stmts := []string{
		`CREATE TABLE user_tz (
			user_id TEXT PRIMARY KEY,
			tz TEXT NOT NULL
		) WITHOUT ROWID`,
	}
	for _, q := range stmts {
		if _, err := tx.Exec(q); err != nil {
			return fmt.Errorf("%w: %s", err, q)
		}
	}
	return nil
}
