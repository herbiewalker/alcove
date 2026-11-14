package store

import (
	"database/sql"
	"fmt"
)

// migrateV5 is M3 piece D (D-31): spaces, memberships and invites. Sharing settings live on the
// membership from the start (D-21 defaults) even though nothing reads them until M4.
func migrateV5(tx *sql.Tx) error {
	stmts := []string{
		`CREATE TABLE space (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			created_by TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			join_approval TEXT NOT NULL DEFAULT 'owner' CHECK (join_approval IN ('owner','all_members')),
			join_code_hash TEXT,
			join_code_at INTEGER,
			deleted_at INTEGER
		)`,
		`CREATE UNIQUE INDEX space_code ON space(join_code_hash) WHERE join_code_hash IS NOT NULL`,
		`CREATE TABLE membership (
			space_id TEXT NOT NULL REFERENCES space(id),
			user_id TEXT NOT NULL,
			role TEXT NOT NULL CHECK (role IN ('owner','member')),
			state TEXT NOT NULL CHECK (state IN ('requested','active','left','removed')),
			created_at INTEGER NOT NULL,
			joined_at INTEGER,
			ended_at INTEGER,
			consent_version INTEGER NOT NULL DEFAULT 0,
			consent_at INTEGER,
			include_notes INTEGER NOT NULL DEFAULT 1,
			include_photos INTEGER NOT NULL DEFAULT 1,
			show_partner_names INTEGER NOT NULL DEFAULT 0,
			paused INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (space_id, user_id)
		)`,
		`CREATE INDEX membership_user ON membership(user_id)`,
		// Only the hash of an invite code is stored. "Expired" is derived from expires_at.
		`CREATE TABLE invite (
			id TEXT PRIMARY KEY,
			code_hash TEXT NOT NULL UNIQUE,
			kind TEXT NOT NULL CHECK (kind IN ('space','server')),
			space_id TEXT,
			created_by TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL,
			state TEXT NOT NULL CHECK (state IN ('open','used','revoked')),
			label TEXT NOT NULL DEFAULT '',
			used_by TEXT,
			used_at INTEGER
		)`,
		`CREATE INDEX invite_space ON invite(space_id)`,
	}
	for _, q := range stmts {
		if _, err := tx.Exec(q); err != nil {
			return fmt.Errorf("%w: %s", err, q)
		}
	}
	return nil
}
