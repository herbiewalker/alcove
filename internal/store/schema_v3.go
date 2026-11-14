package store

import (
	"database/sql"
	"fmt"
)

// migrateV3 is M3 piece B (D-29): uploads, revisions, the sealed backup password and the row
// cache. Everything except `upload`, `revision` and `backup_password` is derived and can be rebuilt
// from the head revision's stored upload.
func migrateV3(tx *sql.Tx) error {
	stmts := []string{
		// One row per stored upload. The blob lives in the blob store under the same id.
		`CREATE TABLE upload (
			id TEXT PRIMARY KEY,
			owner_id TEXT NOT NULL,
			filename TEXT NOT NULL,
			sha256 TEXT NOT NULL,
			size INTEGER NOT NULL,
			tryst_schema INTEGER NOT NULL,
			schema_status INTEGER NOT NULL,
			rows_total INTEGER NOT NULL,
			encounters INTEGER NOT NULL,
			media_count INTEGER NOT NULL,
			uploaded_at INTEGER NOT NULL,
			UNIQUE (owner_id, sha256)
		)`,
		`CREATE INDEX upload_owner ON upload(owner_id, uploaded_at)`,
		// The history. An edit (piece later) is a revision of kind 'edit' over a parent.
		`CREATE TABLE revision (
			id TEXT PRIMARY KEY,
			owner_id TEXT NOT NULL,
			parent_id TEXT,
			kind TEXT NOT NULL CHECK (kind IN ('upload','edit')),
			upload_id TEXT NOT NULL REFERENCES upload(id),
			created_by TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			deleted_at INTEGER
		)`,
		`CREATE INDEX revision_owner ON revision(owner_id, created_at)`,
		// The member's Tryst backup password, sealed under their data key (AES-256-GCM, owner id as AAD).
		`CREATE TABLE backup_password (
			owner_id TEXT PRIMARY KEY,
			sealed BLOB NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		// Derived: the head revision's tables, as raw JSON rows, for fast reads. Rebuildable.
		`CREATE TABLE cache_head (
			owner_id TEXT PRIMARY KEY,
			revision_id TEXT NOT NULL,
			tryst_schema INTEGER NOT NULL,
			table_order TEXT NOT NULL,
			built_at INTEGER NOT NULL
		)`,
		`CREATE TABLE cache_row (
			owner_id TEXT NOT NULL,
			tbl TEXT NOT NULL,
			rid TEXT NOT NULL,
			pos INTEGER NOT NULL,
			data TEXT NOT NULL,
			PRIMARY KEY (owner_id, tbl, rid)
		) WITHOUT ROWID`,
	}
	for _, q := range stmts {
		if _, err := tx.Exec(q); err != nil {
			return fmt.Errorf("%w: %s", err, q)
		}
	}
	return nil
}
