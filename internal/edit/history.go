package edit

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"time"
)

// Change is one field of one edit: what it was and what it became (raw JSON).
type Change struct {
	Field    string
	Old, New json.RawMessage
}

// Revision is one edit of an entry, with the fields it changed.
type Revision struct {
	ID      string
	At      time.Time
	ByID    string // who made it
	ByOwner bool
	Kind    string // "" for a change of fields; "add", "delete" or "restore" for a whole entry
	Changes []Change
}

// History lists the edits of one entry, newest first, from live revisions only. It reads only the
// owner's rows.
func History(tx *sql.Tx, owner, entryID string) ([]Revision, error) {
	return historyOf(tx, owner, "encounters", entryID)
}

// PartnerHistory is History for one of the owner's partners.
func PartnerHistory(tx *sql.Tx, owner, partnerID string) ([]Revision, error) {
	return historyOf(tx, owner, "partners", partnerID)
}

func historyOf(tx *sql.Tx, owner, table, entryID string) ([]Revision, error) {
	rows, err := tx.Query(`SELECT r.id, r.created_at, r.created_by, c.field, c.old_value, c.new_value, c.op
		FROM change c JOIN revision r ON r.id = c.revision_id
		WHERE c.owner_id = ? AND c.tbl = ? AND c.rid = ? AND r.deleted_at IS NULL
		ORDER BY r.created_at DESC, r.rowid DESC, c.id`, owner, table, entryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Revision
	for rows.Next() {
		var id, by, field, o, n, op string
		var at int64
		if err := rows.Scan(&id, &at, &by, &field, &o, &n, &op); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].ID != id {
			out = append(out, Revision{ID: id, At: time.Unix(at, 0).UTC(), ByID: by, ByOwner: by == owner})
		}
		last := &out[len(out)-1]
		if op != "set" {
			last.Kind = op
			continue
		}
		last.Changes = append(last.Changes, Change{field, json.RawMessage(o), json.RawMessage(n)})
	}
	return out, rows.Err()
}

// Revert sets back what one earlier edit of this entry changed. It is an ordinary new edit, so
// history keeps both. The edit being reverted must belong to this owner and entry.
func Revert(tx *sql.Tx, now time.Time, actor, owner, entryID, baseRev, revisionID string) (string, []string, error) {
	return revertOf(tx, now, actor, owner, encounters, entryID, baseRev, revisionID)
}

// PartnerRevert is Revert for one of the owner's partners.
func PartnerRevert(tx *sql.Tx, now time.Time, actor, owner, partnerID, baseRev, revisionID string) (string, []string, error) {
	return revertOf(tx, now, actor, owner, partners, partnerID, baseRev, revisionID)
}

func revertOf(tx *sql.Tx, now time.Time, actor, owner string, k kind, entryID, baseRev, revisionID string) (string, []string, error) {
	rows, err := tx.Query(`SELECT c.field, c.old_value FROM change c JOIN revision r ON r.id = c.revision_id
		WHERE c.revision_id = ? AND c.owner_id = ? AND c.tbl = ? AND c.rid = ? AND c.op = 'set' AND r.deleted_at IS NULL ORDER BY c.id`,
		revisionID, owner, k.table, entryID)
	if err != nil {
		return "", nil, err
	}
	vals := map[string]json.RawMessage{}
	for rows.Next() {
		var f, o string
		if err := rows.Scan(&f, &o); err != nil {
			_ = rows.Close()
			return "", nil, err
		}
		vals[f] = json.RawMessage(o)
	}
	if err := rows.Close(); err != nil {
		return "", nil, err
	}
	if len(vals) == 0 {
		return "", nil, ErrNoEntry
	}
	return applyRaw(tx, now, actor, owner, k, entryID, baseRev, vals)
}

// Describe turns a raw stored value into text for a person: nothing for null, the text of a
// string. A start time is formatted by the caller, which knows the person's zone.
func Describe(raw json.RawMessage) string {
	s := string(bytes.TrimSpace(raw))
	if s == "" || s == "null" {
		return ""
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return str
	}
	return s
}
