package adminlog

import (
	"database/sql"
	"strings"
	"time"
)

// About returns the lines that name one person as their target and were made by someone else (an
// administrator, or the system), newest first, restricted to the listed actions, with the total.
// A person's own acts are not "about" them in this sense. A target is either the person's id (an
// account action) or "<id>:<row>" (a table row of theirs); ids are fixed-alphabet, so the prefix
// test below cannot match another person's lines (D-51).
func About(tx *sql.Tx, person string, actions []string, limit, offset int) ([]Entry, int, error) {
	if person == "" || len(actions) == 0 {
		return nil, 0, nil
	}
	in := strings.TrimSuffix(strings.Repeat("?,", len(actions)), ",")
	where := ` WHERE actor != ? AND action IN (` + in + `) AND (target_id = ? OR substr(target_id, 1, ?) = ?)`
	args := []any{person}
	for _, a := range actions {
		args = append(args, a)
	}
	prefix := person + ":"
	args = append(args, person, len(prefix), prefix)

	var total int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM admin_log`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := tx.Query(`SELECT seq, ts, actor, action, target_type, target_id, fields FROM admin_log`+where+` ORDER BY seq DESC LIMIT ? OFFSET ?`,
		append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		var ts int64
		if err := rows.Scan(&e.Seq, &ts, &e.Actor, &e.Action, &e.TargetType, &e.TargetID, &e.Fields); err != nil {
			return nil, 0, err
		}
		e.Time = time.Unix(ts, 0).UTC()
		out = append(out, e)
	}
	return out, total, rows.Err()
}
