package store

import (
	"database/sql"
	"fmt"
)

// migrateV10 is M5 slice 1 (D-42): the change records of an edit. An edit is a revision of kind
// 'edit' (the column has existed since v3); its content is these rows. The row cache is the upload
// plus every live edit replayed in order, so a rebuild never loses an edit. Values are JSON text.
func migrateV10(tx *sql.Tx) error {
	stmts := []string{
		`CREATE TABLE change (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			revision_id TEXT NOT NULL REFERENCES revision(id),
			owner_id TEXT NOT NULL,
			tbl TEXT NOT NULL,
			rid TEXT NOT NULL,
			field TEXT NOT NULL,
			old_value TEXT NOT NULL,
			new_value TEXT NOT NULL,
			op TEXT NOT NULL CHECK (op IN ('set','add','delete','restore'))
		)`,
		`CREATE INDEX change_revision ON change(revision_id)`,
		`CREATE INDEX change_row ON change(owner_id, tbl, rid)`,
	}
	for _, q := range stmts {
		if _, err := tx.Exec(q); err != nil {
			return fmt.Errorf("%w: %s", err, q)
		}
	}
	return nil
}
