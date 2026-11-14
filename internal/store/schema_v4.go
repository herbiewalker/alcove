package store

import (
	"database/sql"
	"fmt"
)

// migrateV4 is M3 piece C (D-30). Both changes are additive columns, never a table rebuild:
// a deleted account is a disabled one with deleted_at set, and must_change forces a password
// change at the next sign-in (a new account's temporary password, or a break-glass reset).
func migrateV4(tx *sql.Tx) error {
	stmts := []string{
		`ALTER TABLE user ADD COLUMN deleted_at INTEGER`,
		`ALTER TABLE user ADD COLUMN must_change INTEGER NOT NULL DEFAULT 0`,
		`CREATE INDEX revision_deleted ON revision(deleted_at) WHERE deleted_at IS NOT NULL`,
	}
	for _, q := range stmts {
		if _, err := tx.Exec(q); err != nil {
			return fmt.Errorf("%w: %s", err, q)
		}
	}
	return nil
}
