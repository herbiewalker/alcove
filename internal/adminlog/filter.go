package adminlog

import (
	"database/sql"
	"strings"
	"time"
)

// Filter narrows the log for the viewer and the export (F18). Every field is optional. Values are
// only ever bound as query parameters, never put into the SQL text.
type Filter struct {
	Actor      string    // an exact user id, or System
	Action     string    // an action or the start of one: "data.edit" also matches "data.edit.x"; "data." matches the group
	TargetType string    // an exact target type
	From, To   time.Time // inclusive bounds; the zero time means no bound
}

// ValidFilterText reports whether a filter word could appear in the log at all. The log only holds
// the safe alphabet, so anything else can never match and is refused up front.
func ValidFilterText(s string) bool { return safe.MatchString(s) }

// where builds the condition and its arguments.
func (f Filter) where() (string, []any) {
	var conds []string
	var args []any
	if f.Actor != "" {
		conds = append(conds, "actor = ?")
		args = append(args, f.Actor)
	}
	if f.Action != "" {
		esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(f.Action)
		conds = append(conds, `(action = ? OR action LIKE ? ESCAPE '\')`)
		args = append(args, f.Action, esc+"%")
	}
	if f.TargetType != "" {
		conds = append(conds, "target_type = ?")
		args = append(args, f.TargetType)
	}
	if !f.From.IsZero() {
		conds = append(conds, "ts >= ?")
		args = append(args, f.From.Unix())
	}
	if !f.To.IsZero() {
		conds = append(conds, "ts <= ?")
		args = append(args, f.To.Unix())
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// Find returns the entries that match, newest first. A limit of 0 or less means no limit.
func Find(tx *sql.Tx, f Filter, limit, offset int) ([]Entry, error) {
	w, args := f.where()
	q := `SELECT seq, ts, actor, action, target_type, target_id, fields FROM admin_log` + w + ` ORDER BY seq DESC`
	if limit > 0 {
		q += ` LIMIT ? OFFSET ?`
		args = append(args, limit, offset)
	}
	rows, err := tx.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		var ts int64
		if err := rows.Scan(&e.Seq, &ts, &e.Actor, &e.Action, &e.TargetType, &e.TargetID, &e.Fields); err != nil {
			return nil, err
		}
		e.Time = time.Unix(ts, 0).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

// Count is how many entries match.
func Count(tx *sql.Tx, f Filter) (n int, err error) {
	w, args := f.where()
	err = tx.QueryRow(`SELECT COUNT(*) FROM admin_log`+w, args...).Scan(&n)
	return
}
