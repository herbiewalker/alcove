package store

import (
	"database/sql"
	"fmt"
)

// migrateV12 is M7 (D-52): the profile picture and the partner pictures copied out of an owner's
// current upload. Like media_blob it is derived and rebuildable; the files themselves are in the blob
// store under their media id, encrypted under the owner's data key. subject is "profile" or
// "partner:<partner id>".
func migrateV12(tx *sql.Tx) error {
	stmts := []string{
		`CREATE TABLE avatar (
			owner_id TEXT NOT NULL,
			subject TEXT NOT NULL,
			media_id TEXT NOT NULL,
			kind INTEGER NOT NULL DEFAULT 0,
			size INTEGER NOT NULL,
			PRIMARY KEY (owner_id, subject)
		) WITHOUT ROWID`,
	}
	for _, q := range stmts {
		if _, err := tx.Exec(q); err != nil {
			return fmt.Errorf("%w: %s", err, q)
		}
	}
	return nil
}
