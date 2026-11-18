package store

import (
	"database/sql"
	"fmt"
)

// migrateV11 is M8 slice 5 (D-49): optional two-step sign-in. The secret has to be readable to check
// a code, so it is stored as is, inside this sealed database (and so inside the encrypted instance
// backup). Recovery codes are stored only as SHA-256 hashes. Both tables go when the account does.
func migrateV11(tx *sql.Tx) error {
	stmts := []string{
		`CREATE TABLE user_totp (
			user_id TEXT PRIMARY KEY REFERENCES user(id) ON DELETE CASCADE,
			secret BLOB NOT NULL,
			enabled_at INTEGER NOT NULL,
			last_step INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE user_recovery (
			user_id TEXT NOT NULL REFERENCES user(id) ON DELETE CASCADE,
			hash TEXT NOT NULL,
			used_at INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (user_id, hash)
		)`,
	}
	for _, q := range stmts {
		if _, err := tx.Exec(q); err != nil {
			return fmt.Errorf("%w: %s", err, q)
		}
	}
	return nil
}
