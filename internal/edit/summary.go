package edit

import (
	"database/sql"
	"sort"
)

// Summary is what the live edits on the owner's current upload add up to, for the export page. It
// counts rows and names fields; it never carries a value.
type Summary struct {
	Edited         int      // entries that exist in the upload and were changed
	Added          int      // entries that are not in the upload and are still there
	Deleted        int      // entries that are in the upload and are deleted now
	PartnersEdited int      // partners changed
	Fields         []string // the fields touched on entries and partners, as names, sorted
	Edits          int      // how many saves there were
}

// Summarize replays the change records of the head upload's live edits, oldest first, in the same
// order Replay uses, and keeps only the net effect per row.
func Summarize(tx *sql.Tx, owner string) (Summary, error) {
	var s Summary
	_, uploadID, err := Head(tx, owner)
	if err != nil {
		return s, err
	}
	rows, err := tx.Query(`SELECT c.tbl, c.rid, c.field, c.op FROM change c JOIN revision r ON r.id = c.revision_id
		WHERE r.owner_id = ? AND r.upload_id = ? AND r.kind = 'edit' AND r.deleted_at IS NULL
		ORDER BY r.created_at, r.rowid, c.id`, owner, uploadID)
	if err != nil {
		return s, err
	}
	defer rows.Close()
	type state struct {
		tbl            string
		added, deleted bool
		edited         bool
	}
	seen := map[string]*state{}
	fields := map[string]bool{}
	for rows.Next() {
		var tbl, rid, field, op string
		if err := rows.Scan(&tbl, &rid, &field, &op); err != nil {
			return s, err
		}
		key := tbl + "\x00" + rid
		st := seen[key]
		if st == nil {
			st = &state{tbl: tbl}
			seen[key] = st
		}
		switch op {
		case "add":
			st.added, st.deleted = true, false
		case "delete":
			st.deleted = true
		case "restore":
			st.deleted = false
		case "set":
			st.edited = true
			fields[field] = true
		}
	}
	if err := rows.Err(); err != nil {
		return s, err
	}
	for _, st := range seen {
		switch {
		case st.tbl == "partners":
			if st.edited {
				s.PartnersEdited++
			}
		case st.tbl != "encounters":
			// the links and photo rows that go with an entry are not counted as entries
		case st.added && !st.deleted:
			s.Added++
		case st.added:
			// added and deleted again: nothing left
		case st.deleted:
			s.Deleted++
		case st.edited:
			s.Edited++
		}
	}
	for f := range fields {
		s.Fields = append(s.Fields, f)
	}
	sort.Strings(s.Fields)
	err = tx.QueryRow(`SELECT COUNT(*) FROM revision WHERE owner_id = ? AND upload_id = ? AND kind = 'edit' AND deleted_at IS NULL`, owner, uploadID).Scan(&s.Edits)
	return s, err
}
