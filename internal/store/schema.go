package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// SchemaVersion is the store schema this build writes. Migrations are forward-only and additive
// (DATA_MODEL "Schema / migration rules"): never drop or repurpose a column.
const SchemaVersion = 12

var migrations = []func(*sql.Tx) error{migrateV1, migrateV2, migrateV3, migrateV4, migrateV5, migrateV6, migrateV7, migrateV8, migrateV9, migrateV10, migrateV11, migrateV12}

// migrateV1 is the M2a foundation. The domain tables (user, space, revision and the rest) arrive
// with the milestones that need them, each as its own forward migration.
func migrateV1(tx *sql.Tx) error {
	stmts := []string{
		`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		// Each owner's data key, wrapped by the master key (AAD = the owner id).
		`CREATE TABLE data_key (owner_id TEXT PRIMARY KEY, wrapped BLOB NOT NULL, created_at INTEGER NOT NULL)`,
		// Full-text search (ADR D-17). The index lives in this sealed database, so no plaintext
		// index ever sits on disk. owner_id, kind and ref are filters, not searchable text.
		`CREATE VIRTUAL TABLE search USING fts5(owner_id UNINDEXED, kind UNINDEXED, ref UNINDEXED, body, tokenize='unicode61')`,
	}
	for _, q := range stmts {
		if _, err := tx.Exec(q); err != nil {
			return fmt.Errorf("%w: %s", err, q)
		}
	}
	return nil
}

func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_info (id INTEGER PRIMARY KEY CHECK (id = 1), version INTEGER NOT NULL)`); err != nil {
		return err
	}
	var cur int
	err := s.db.QueryRow(`SELECT version FROM schema_info WHERE id = 1`).Scan(&cur)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if cur > len(migrations) {
		return fmt.Errorf("store: database schema v%d is newer than this build understands (v%d)", cur, len(migrations))
	}
	for v := cur; v < len(migrations); v++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if err := migrations[v](tx); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: migrate to v%d: %w", v+1, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_info(id, version) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET version = excluded.version`, v+1); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		s.MarkDirty()
	}
	return nil
}

// SchemaVersionInDB returns the schema version recorded in the open database.
func (s *Store) SchemaVersionInDB() (int, error) {
	var v int
	err := s.db.QueryRow(`SELECT version FROM schema_info WHERE id = 1`).Scan(&v)
	return v, err
}

// SetMeta stores a small string fact (for example the last admin-log anchor).
func (s *Store) SetMeta(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO meta(key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	if err == nil {
		s.MarkDirty()
	}
	return err
}

// GetMeta reads a fact stored with SetMeta.
func (s *Store) GetMeta(key string) (string, bool, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return v, err == nil, err
}
