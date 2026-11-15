package edit

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Errors of adding, deleting and restoring whole entries (D-42, slice 3).
var (
	ErrExpired    = errors.New("edit: too long ago to restore")
	ErrNoTemplate = errors.New("edit: there is no entry to copy the layout from")
	ErrExists     = errors.New("edit: that entry is already there")
)

// newEntryID makes a random UUID (version 4), the shape Tryst uses for its own ids.
func newEntryID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

func nextPos(tx *sql.Tx, owner, table string) (int, error) {
	var n int
	err := tx.QueryRow(`SELECT COALESCE(MAX(pos), -1) + 1 FROM cache_row WHERE owner_id = ? AND tbl = ?`, owner, table).Scan(&n)
	return n, err
}

// blank builds a new entry row in the layout of the person's own rows: every column their backup
// has, in its order. A column some of their rows leave empty is left empty; any other gets the
// empty value of its type; the fields the form offers are set from what was typed. The ids and
// times are Alcove's.
func blank(tx *sql.Tx, owner string, id string, atMs int64, vals map[string]json.RawMessage) (row, error) {
	rows, err := tx.Query(`SELECT data FROM cache_row WHERE owner_id = ? AND tbl = 'encounters' ORDER BY pos`, owner)
	if err != nil {
		return row{}, err
	}
	defer rows.Close()
	var tmpl row
	nullable := map[string]bool{}
	first := true
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return row{}, err
		}
		r, err := parseRow(data)
		if err != nil {
			continue
		}
		if first {
			tmpl, first = r, false
		}
		for k, v := range r.vals {
			if string(v) == "null" {
				nullable[k] = true
			}
		}
		for _, k := range r.keys { // a column some rows have and others lack is kept
			if _, ok := tmpl.vals[k]; !ok {
				tmpl.keys = append(tmpl.keys, k)
				tmpl.vals[k] = r.vals[k]
			}
		}
	}
	if err := rows.Err(); err != nil {
		return row{}, err
	}
	if first {
		return row{}, ErrNoTemplate
	}
	out := row{vals: map[string]json.RawMessage{}}
	for _, k := range tmpl.keys {
		var v json.RawMessage
		switch {
		case nullable[k]:
			v = json.RawMessage("null")
		case len(tmpl.vals[k]) > 0 && tmpl.vals[k][0] == '"':
			v = json.RawMessage(`""`)
		case len(tmpl.vals[k]) > 0 && (tmpl.vals[k][0] == '-' || tmpl.vals[k][0] >= '0' && tmpl.vals[k][0] <= '9'):
			v = json.RawMessage("0")
		case string(tmpl.vals[k]) == "true" || string(tmpl.vals[k]) == "false":
			v = json.RawMessage("false")
		default:
			v = json.RawMessage("null")
		}
		out.keys = append(out.keys, k)
		out.vals[k] = v
	}
	ms := json.RawMessage(strconv.FormatInt(atMs, 10))
	out.set("id", quote(id))
	out.set("createdAt", ms)
	out.set("updatedAt", ms)
	for k, v := range vals {
		out.set(k, v)
	}
	return out, nil
}

// Add records a new entry as a new revision. It is added to the person's current data only: it has
// a new id, so a restore on the phone adds it. in is what the form offered, like Apply.
func Add(tx *sql.Tx, now time.Time, actor, owner, baseRev string, in map[string]string) (revID, entryID string, err error) {
	head, uploadID, err := Head(tx, owner)
	if err != nil {
		return "", "", err
	}
	if baseRev != head {
		return "", "", ErrStale
	}
	vals := map[string]json.RawMessage{}
	for _, f := range Fields {
		s, ok := in[f.Name]
		if !ok {
			continue
		}
		var v json.RawMessage
		if cat := CatalogTable(f); cat != "" {
			v, err = cleanSet(tx, owner, f, cat, s, json.RawMessage("null"))
		} else {
			v, err = clean(Fields, f.Name, s)
		}
		if err != nil {
			return "", "", err
		}
		vals[f.Name] = v
	}
	if _, ok := vals["startAt"]; !ok {
		return "", "", &BadField{"startAt", "must be a real date and time"}
	}
	if entryID, err = newEntryID(); err != nil {
		return "", "", err
	}
	at := now.Unix() * 1000
	r, err := blank(tx, owner, entryID, at, vals)
	if err != nil {
		return "", "", err
	}
	if revID, err = newRevID(); err != nil {
		return "", "", err
	}
	if _, err := tx.Exec(`INSERT INTO revision(id, owner_id, parent_id, kind, upload_id, created_by, created_at) VALUES (?,?,?,?,?,?,?)`,
		revID, owner, head, "edit", uploadID, actor, now.Unix()); err != nil {
		return "", "", err
	}
	if _, err := tx.Exec(`INSERT INTO change(revision_id, owner_id, tbl, rid, field, old_value, new_value, op) VALUES (?,?,?,?,?,?,?,'add')`,
		revID, owner, "encounters", entryID, "", "", r.String()); err != nil {
		return "", "", err
	}
	if err := insertRow(tx, owner, encounters, entryID, r.String(), -1); err != nil {
		return "", "", err
	}
	if _, err := tx.Exec(`UPDATE cache_head SET revision_id = ? WHERE owner_id = ?`, revID, owner); err != nil {
		return "", "", err
	}
	return revID, entryID, nil
}

// insertRow puts a row into the cache and indexes its text. pos < 0 means at the end.
func insertRow(tx *sql.Tx, owner string, k kind, id, data string, pos int) error {
	if pos < 0 {
		var err error
		if pos, err = nextPos(tx, owner, k.table); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO cache_row(owner_id, tbl, rid, pos, data) VALUES (?,?,?,?,?)`, owner, k.table, id, pos, data); err != nil {
		return err
	}
	r, err := parseRow(data)
	if err != nil {
		return err
	}
	return reindex(tx, owner, k, id, r, k.searchCols)
}

// Delete removes one entry, with the rows that hang on it (its partner links and photo rows), from
// the person's current data. Every removed row is kept in the change records so Restore can put it
// back. Nothing else is touched.
func Delete(tx *sql.Tx, now time.Time, actor, owner, entryID, baseRev string) (string, error) {
	head, uploadID, err := Head(tx, owner)
	if err != nil {
		return "", err
	}
	if _, err := load(tx, owner, encounters, entryID); err != nil {
		return "", err
	}
	if baseRev != head {
		return "", ErrStale
	}
	type gone struct {
		tbl, rid, data string
		pos            int
	}
	var list []gone
	rows, err := tx.Query(`SELECT tbl, rid, pos, data FROM cache_row WHERE owner_id = ? AND ((tbl = 'encounters' AND rid = ?)
		OR (tbl <> 'encounters' AND json_valid(data) AND json_extract(data, '$.encounterId') = ?)) ORDER BY (tbl <> 'encounters'), tbl, pos`, owner, entryID, entryID)
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var g gone
		if err := rows.Scan(&g.tbl, &g.rid, &g.pos, &g.data); err != nil {
			_ = rows.Close()
			return "", err
		}
		list = append(list, g)
	}
	if err := rows.Close(); err != nil {
		return "", err
	}
	revID, err := newRevID()
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(`INSERT INTO revision(id, owner_id, parent_id, kind, upload_id, created_by, created_at) VALUES (?,?,?,?,?,?,?)`,
		revID, owner, head, "edit", uploadID, actor, now.Unix()); err != nil {
		return "", err
	}
	for _, g := range list {
		if _, err := tx.Exec(`INSERT INTO change(revision_id, owner_id, tbl, rid, field, old_value, new_value, op) VALUES (?,?,?,?,?,?,?,'delete')`,
			revID, owner, g.tbl, g.rid, "", g.data, strconv.Itoa(g.pos)); err != nil {
			return "", err
		}
		if err := removeRow(tx, owner, g.tbl, g.rid); err != nil {
			return "", err
		}
	}
	if _, err := tx.Exec(`UPDATE cache_head SET revision_id = ? WHERE owner_id = ?`, revID, owner); err != nil {
		return "", err
	}
	return revID, nil
}

func removeRow(tx *sql.Tx, owner, tbl, rid string) error {
	if _, err := tx.Exec(`DELETE FROM cache_row WHERE owner_id = ? AND tbl = ? AND rid = ?`, owner, tbl, rid); err != nil {
		return err
	}
	if k, ok := kinds[tbl]; ok {
		_, err := tx.Exec(`DELETE FROM search WHERE owner_id = ? AND kind = ? AND ref = ?`, owner, k.searchKind, rid)
		return err
	}
	return nil
}

// DeletedEntry is an entry that was deleted and not yet restored.
type DeletedEntry struct {
	ID          string
	DeletedAt   time.Time
	StartMs     int64
	Restorable  bool // the window is still open for the person
	RestoreByMs int64
}

// Deleted lists the entries the person deleted and has not restored, newest deletion first, whose
// stored values have not been purged. window is how long the person may restore.
func Deleted(tx *sql.Tx, owner string, now time.Time, window time.Duration) ([]DeletedEntry, error) {
	rows, err := tx.Query(`SELECT c.rid, c.op, c.old_value, r.created_at FROM change c JOIN revision r ON r.id = c.revision_id
		WHERE c.owner_id = ? AND c.tbl = 'encounters' AND c.op IN ('add','delete','restore') AND r.deleted_at IS NULL
		ORDER BY r.created_at, r.rowid, c.id`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	last := map[string]DeletedEntry{}
	op := map[string]string{}
	for rows.Next() {
		var rid, o, old string
		var at int64
		if err := rows.Scan(&rid, &o, &old, &at); err != nil {
			return nil, err
		}
		op[rid] = o
		if o != "delete" {
			delete(last, rid)
			continue
		}
		d := DeletedEntry{ID: rid, DeletedAt: time.Unix(at, 0).UTC()}
		if old != "" {
			var m map[string]json.RawMessage
			if json.Unmarshal([]byte(old), &m) == nil {
				if n, err := strconv.ParseInt(strings.TrimSpace(string(m["startAt"])), 10, 64); err == nil {
					d.StartMs = n
				}
			}
			d.Restorable = now.Sub(d.DeletedAt) <= window
			d.RestoreByMs = d.DeletedAt.Add(window).UnixMilli()
			last[rid] = d
		} else {
			delete(last, rid) // purged: it can no longer be restored, so it is no longer listed
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []DeletedEntry
	for _, d := range last {
		out = append(out, d)
	}
	sortDeleted(out)
	return out, nil
}

func sortDeleted(l []DeletedEntry) {
	for i := 1; i < len(l); i++ {
		for j := i; j > 0 && (l[j].DeletedAt.After(l[j-1].DeletedAt) || l[j].DeletedAt.Equal(l[j-1].DeletedAt) && l[j].ID < l[j-1].ID); j-- {
			l[j], l[j-1] = l[j-1], l[j]
		}
	}
}

// Restore puts a deleted entry back, with the rows it took with it, as a new revision. It refuses an
// entry whose window (the person's own, or the administrator's) has passed or whose values were
// purged.
func Restore(tx *sql.Tx, now time.Time, actor, owner, entryID, baseRev string, window time.Duration) (string, error) {
	head, uploadID, err := Head(tx, owner)
	if err != nil {
		return "", err
	}
	if _, err := load(tx, owner, encounters, entryID); err == nil {
		return "", ErrExists
	} else if !errors.Is(err, ErrNoEntry) {
		return "", err
	}
	var delRev string
	var delAt int64
	err = tx.QueryRow(`SELECT r.id, r.created_at FROM change c JOIN revision r ON r.id = c.revision_id
		WHERE c.owner_id = ? AND c.tbl = 'encounters' AND c.rid = ? AND c.op = 'delete' AND r.deleted_at IS NULL
		ORDER BY r.created_at DESC, r.rowid DESC LIMIT 1`, owner, entryID).Scan(&delRev, &delAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNoEntry
	}
	if err != nil {
		return "", err
	}
	if baseRev != head {
		return "", ErrStale
	}
	if now.Sub(time.Unix(delAt, 0)) > window {
		return "", ErrExpired
	}
	type back struct {
		tbl, rid, data string
		pos            int
	}
	var list []back
	rows, err := tx.Query(`SELECT tbl, rid, old_value, new_value FROM change WHERE revision_id = ? AND op = 'delete' ORDER BY id`, delRev)
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var b back
		var pos string
		if err := rows.Scan(&b.tbl, &b.rid, &b.data, &pos); err != nil {
			_ = rows.Close()
			return "", err
		}
		b.pos, _ = strconv.Atoi(pos)
		list = append(list, b)
	}
	if err := rows.Close(); err != nil {
		return "", err
	}
	for _, b := range list {
		if b.data == "" {
			return "", ErrExpired // purged
		}
	}
	revID, err := newRevID()
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(`INSERT INTO revision(id, owner_id, parent_id, kind, upload_id, created_by, created_at) VALUES (?,?,?,?,?,?,?)`,
		revID, owner, head, "edit", uploadID, actor, now.Unix()); err != nil {
		return "", err
	}
	for _, b := range list {
		if _, err := tx.Exec(`INSERT INTO change(revision_id, owner_id, tbl, rid, field, old_value, new_value, op) VALUES (?,?,?,?,?,?,?,'restore')`,
			revID, owner, b.tbl, b.rid, "", strconv.Itoa(b.pos), b.data); err != nil {
			return "", err
		}
		if k, ok := kinds[b.tbl]; ok {
			if err := insertRow(tx, owner, k, b.rid, b.data, b.pos); err != nil {
				return "", err
			}
		} else if _, err := tx.Exec(`INSERT OR REPLACE INTO cache_row(owner_id, tbl, rid, pos, data) VALUES (?,?,?,?,?)`, owner, b.tbl, b.rid, b.pos, b.data); err != nil {
			return "", err
		}
	}
	if _, err := tx.Exec(`UPDATE cache_head SET revision_id = ? WHERE owner_id = ?`, revID, owner); err != nil {
		return "", err
	}
	return revID, nil
}

// PurgeDeleted wipes the stored values of entries deleted at or before cutoff (unix seconds). The
// fact that an entry was deleted stays in the history; the entry itself can no longer be restored.
// It returns how many entries were wiped.
func PurgeDeleted(tx *sql.Tx, cutoff int64) (int, error) {
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM change c JOIN revision r ON r.id = c.revision_id
		WHERE c.op = 'delete' AND c.tbl = 'encounters' AND c.old_value <> '' AND r.created_at <= ?`, cutoff).Scan(&n); err != nil {
		return 0, err
	}
	_, err := tx.Exec(`UPDATE change SET old_value = '' WHERE op = 'delete' AND old_value <> ''
		AND revision_id IN (SELECT id FROM revision WHERE created_at <= ?)`, cutoff)
	return n, err
}
