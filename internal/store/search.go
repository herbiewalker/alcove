package store

import (
	"context"
	"strings"
)

// Hit is one full-text search result.
type Hit struct {
	OwnerID string
	Kind    string
	Ref     string
}

// IndexText records body as searchable text for (owner, kind, ref), replacing any earlier text for
// the same triple. The text is stored only in the sealed database (ADR D-17).
func (s *Store) IndexText(ctx context.Context, owner, kind, ref, body string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM search WHERE owner_id = ? AND kind = ? AND ref = ?`, owner, kind, ref); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO search(owner_id, kind, ref, body) VALUES (?, ?, ?, ?)`, owner, kind, ref, body); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.MarkDirty()
	return nil
}

// RemoveText deletes the text for (owner, kind, ref).
func (s *Store) RemoveText(ctx context.Context, owner, kind, ref string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM search WHERE owner_id = ? AND kind = ? AND ref = ?`, owner, kind, ref)
	if err == nil {
		s.MarkDirty()
	}
	return err
}

// RemoveOwnerText deletes everything indexed for owner (used by a purge).
func (s *Store) RemoveOwnerText(ctx context.Context, owner string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM search WHERE owner_id = ?`, owner)
	if err == nil {
		s.MarkDirty()
	}
	return err
}

// Search returns hits for q, best first. owners limits the search to those owners; a nil slice
// searches everything and is for the admin only (the caller logs every admin search).
// Query words are matched literally: each is quoted, so FTS5 operators in user input are inert.
func (s *Store) Search(ctx context.Context, owners []string, q string, limit int) ([]Hit, error) {
	match := matchExpr(q)
	if match == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	query := `SELECT owner_id, kind, ref FROM search WHERE search MATCH ?`
	args := []any{match}
	if owners != nil {
		if len(owners) == 0 {
			return nil, nil
		}
		query += ` AND owner_id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(owners)), ",") + `)`
		for _, o := range owners {
			args = append(args, o)
		}
	}
	query += ` ORDER BY rank LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hits []Hit
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.OwnerID, &h.Kind, &h.Ref); err != nil {
			return nil, err
		}
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

// matchExpr turns free text into an FTS5 expression of quoted words (an implicit AND).
func matchExpr(q string) string {
	var parts []string
	for _, w := range strings.Fields(q) {
		w = strings.ReplaceAll(w, `"`, `""`)
		if strings.Trim(w, `"`) == "" {
			continue
		}
		parts = append(parts, `"`+w+`"`)
	}
	return strings.Join(parts, " ")
}
