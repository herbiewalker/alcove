package store

import (
	"database/sql"
	"fmt"
)

// migrateV2 is M2b: accounts and the admin log. Settings live in meta under "setting.*".
func migrateV2(tx *sql.Tx) error {
	stmts := []string{
		`CREATE TABLE user (
			id TEXT PRIMARY KEY,
			username TEXT NOT NULL UNIQUE COLLATE NOCASE,
			display_name TEXT NOT NULL,
			role TEXT NOT NULL CHECK (role IN ('admin','member')),
			status TEXT NOT NULL CHECK (status IN ('active','disabled')) DEFAULT 'active',
			pw_hash TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			last_login_at INTEGER NOT NULL DEFAULT 0
		)`,
		// Append-only, hash-chained (D-6, N5). The chain is built and verified by internal/adminlog.
		`CREATE TABLE admin_log (
			seq INTEGER PRIMARY KEY,
			ts INTEGER NOT NULL,
			actor TEXT NOT NULL,
			action TEXT NOT NULL,
			target_type TEXT NOT NULL,
			target_id TEXT NOT NULL,
			fields TEXT NOT NULL,
			prev_hash BLOB NOT NULL,
			hash BLOB NOT NULL
		)`,
		// Defence in depth: the database itself refuses to edit or delete a log row.
		`CREATE TRIGGER admin_log_no_update BEFORE UPDATE ON admin_log BEGIN SELECT RAISE(ABORT, 'admin_log is append-only'); END`,
		`CREATE TRIGGER admin_log_no_delete BEFORE DELETE ON admin_log BEGIN SELECT RAISE(ABORT, 'admin_log is append-only'); END`,
	}
	for _, q := range stmts {
		if _, err := tx.Exec(q); err != nil {
			return fmt.Errorf("%w: %s", err, q)
		}
	}
	return nil
}
