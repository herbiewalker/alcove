package edit

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

const (
	table          = "encounters"
	maxIndexedBody = 8 << 10 // the same bound the ingest code uses for search text
)

// row is one cached table row with its columns in file order, so an edit changes only the columns
// it names and leaves every other column and its position exactly as the backup had it.
type row struct {
	keys []string
	vals map[string]json.RawMessage
}

func parseRow(s string) (row, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return row{}, errors.New("edit: a stored row is not an object")
	}
	r := row{vals: map[string]json.RawMessage{}}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return row{}, err
		}
		k, _ := kt.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return row{}, err
		}
		r.keys = append(r.keys, k)
		r.vals[k] = v
	}
	return r, nil
}

func (r row) String() string {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range r.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		b.Write(kb)
		b.WriteByte(':')
		b.Write(r.vals[k])
	}
	b.WriteByte('}')
	return b.String()
}

func (r *row) set(k string, v json.RawMessage) {
	if _, ok := r.vals[k]; !ok {
		r.keys = append(r.keys, k)
	}
	r.vals[k] = v
}

func (r row) get(k string) json.RawMessage {
	if v, ok := r.vals[k]; ok {
		return v
	}
	return json.RawMessage("null")
}

func newRevID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "ed" + hex.EncodeToString(b[:]), nil
}

// Head is the owner's current revision and the upload it sits on.
func Head(tx *sql.Tx, owner string) (revID, uploadID string, err error) {
	err = tx.QueryRow(`SELECT id, upload_id FROM revision WHERE owner_id = ? AND deleted_at IS NULL
		ORDER BY created_at DESC, rowid DESC LIMIT 1`, owner).Scan(&revID, &uploadID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNoHead
	}
	return
}

// Current returns the editable values of one of the owner's entries as raw JSON, and the revision
// they were read at (the form carries it back, so a stale save is refused).
func Current(tx *sql.Tx, owner, entryID string) (map[string]json.RawMessage, string, error) {
	head, _, err := Head(tx, owner)
	if err != nil {
		return nil, "", err
	}
	r, err := load(tx, owner, entryID)
	if err != nil {
		return nil, "", err
	}
	out := map[string]json.RawMessage{}
	for _, f := range Fields {
		out[f.Name] = r.get(f.Name)
	}
	return out, head, nil
}

func load(tx *sql.Tx, owner, entryID string) (row, error) {
	var data string
	err := tx.QueryRow(`SELECT data FROM cache_row WHERE owner_id = ? AND tbl = ? AND rid = ?
		AND json_type(data, '$.startAt') IN ('integer','real')`, owner, table, entryID).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return row{}, ErrNoEntry
	}
	if err != nil {
		return row{}, err
	}
	return parseRow(data)
}

// Apply records one person's edit of one of the owner's entries as a new revision. in holds what
// was typed for each field the form offered; a field whose cleaned value equals the current one is
// not a change. baseRev is the revision the form was opened on. It returns the new revision's id and
// the names of the fields that changed. Content never appears in an error.
func Apply(tx *sql.Tx, now time.Time, actor, owner, entryID, baseRev string, in map[string]string) (string, []string, error) {
	vals := map[string]json.RawMessage{}
	for _, f := range Fields {
		s, ok := in[f.Name]
		if !ok {
			continue
		}
		v, err := Clean(f.Name, s)
		if err != nil {
			return "", nil, err
		}
		vals[f.Name] = v
	}
	return applyRaw(tx, now, actor, owner, entryID, baseRev, vals)
}

func applyRaw(tx *sql.Tx, now time.Time, actor, owner, entryID, baseRev string, vals map[string]json.RawMessage) (string, []string, error) {
	head, uploadID, err := Head(tx, owner)
	if err != nil {
		return "", nil, err
	}
	if baseRev != head {
		return "", nil, ErrStale
	}
	r, err := load(tx, owner, entryID)
	if err != nil {
		return "", nil, err
	}
	type diff struct {
		field    string
		old, new json.RawMessage
	}
	var diffs []diff
	for _, f := range Fields {
		nv, ok := vals[f.Name]
		if !ok {
			continue
		}
		if ov := r.get(f.Name); !same(ov, nv) {
			diffs = append(diffs, diff{f.Name, ov, nv})
		}
	}
	if len(diffs) == 0 {
		return "", nil, ErrNoChange
	}
	id, err := newRevID()
	if err != nil {
		return "", nil, err
	}
	if _, err := tx.Exec(`INSERT INTO revision(id, owner_id, parent_id, kind, upload_id, created_by, created_at) VALUES (?,?,?,?,?,?,?)`,
		id, owner, head, "edit", uploadID, actor, now.Unix()); err != nil {
		return "", nil, err
	}
	var names []string
	for _, d := range diffs {
		if _, err := tx.Exec(`INSERT INTO change(revision_id, owner_id, tbl, rid, field, old_value, new_value, op) VALUES (?,?,?,?,?,?,?,'set')`,
			id, owner, table, entryID, d.field, string(d.old), string(d.new)); err != nil {
			return "", nil, err
		}
		r.set(d.field, d.new)
		names = append(names, d.field)
	}
	if err := store(tx, owner, entryID, &r, now.Unix(), names); err != nil {
		return "", nil, err
	}
	if _, err := tx.Exec(`UPDATE cache_head SET revision_id = ? WHERE owner_id = ?`, id, owner); err != nil {
		return "", nil, err
	}
	return id, names, nil
}

// store writes the changed row back to the cache, stamps Tryst's updatedAt (derived from the
// revision time, so a replay produces the same value) and refreshes the search text.
func store(tx *sql.Tx, owner, entryID string, r *row, at int64, changed []string) error {
	if _, ok := r.vals["updatedAt"]; ok {
		r.set("updatedAt", json.RawMessage(strconv.FormatInt(at*1000, 10)))
	}
	if _, err := tx.Exec(`UPDATE cache_row SET data = ? WHERE owner_id = ? AND tbl = ? AND rid = ?`, r.String(), owner, table, entryID); err != nil {
		return err
	}
	for _, c := range changed {
		if c != "note" {
			continue
		}
		if _, err := tx.Exec(`DELETE FROM search WHERE owner_id = ? AND kind = 'encounter' AND ref = ?`, owner, entryID); err != nil {
			return err
		}
		var note string
		if json.Unmarshal(r.get("note"), &note) == nil && strings.TrimSpace(note) != "" {
			if len(note) > maxIndexedBody {
				note = note[:maxIndexedBody]
			}
			if _, err := tx.Exec(`INSERT INTO search(owner_id, kind, ref, body) VALUES (?,?,?,?)`, owner, "encounter", entryID, note); err != nil {
				return err
			}
		}
	}
	return nil
}

// Replay applies every live edit of the upload the owner's head sits on, oldest first, to the
// cache that was just rebuilt from that upload. It is the only other way a cache row changes, and
// the property test proves it gives exactly what Apply gave.
func Replay(tx *sql.Tx, owner, uploadID string) error {
	revs, err := tx.Query(`SELECT id, created_at FROM revision WHERE owner_id = ? AND upload_id = ? AND kind = 'edit' AND deleted_at IS NULL
		ORDER BY created_at, rowid`, owner, uploadID)
	if err != nil {
		return err
	}
	type rev struct {
		id string
		at int64
	}
	var list []rev
	for revs.Next() {
		var v rev
		if err := revs.Scan(&v.id, &v.at); err != nil {
			_ = revs.Close()
			return err
		}
		list = append(list, v)
	}
	if err := revs.Close(); err != nil {
		return err
	}
	for _, v := range list {
		cs, err := tx.Query(`SELECT tbl, rid, field, new_value, op FROM change WHERE revision_id = ? ORDER BY id`, v.id)
		if err != nil {
			return err
		}
		type ch struct{ tbl, rid, field, val, op string }
		var cl []ch
		for cs.Next() {
			var c ch
			if err := cs.Scan(&c.tbl, &c.rid, &c.field, &c.val, &c.op); err != nil {
				_ = cs.Close()
				return err
			}
			cl = append(cl, c)
		}
		if err := cs.Close(); err != nil {
			return err
		}
		touched := map[string]*row{}
		fields := map[string][]string{}
		for _, c := range cl {
			if c.tbl != table || c.op != "set" {
				continue
			}
			r, ok := touched[c.rid]
			if !ok {
				loaded, err := load(tx, owner, c.rid)
				if errors.Is(err, ErrNoEntry) {
					continue // the row is not in this upload; nothing to apply
				}
				if err != nil {
					return err
				}
				r = &loaded
				touched[c.rid] = r
			}
			r.set(c.field, json.RawMessage(c.val))
			fields[c.rid] = append(fields[c.rid], c.field)
		}
		for rid, r := range touched {
			if err := store(tx, owner, rid, r, v.at, fields[rid]); err != nil {
				return err
			}
		}
	}
	return nil
}
