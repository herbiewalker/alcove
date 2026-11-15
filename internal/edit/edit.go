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

const maxIndexedBody = 8 << 10 // the same bound the ingest code uses for search text

// kind says how one editable table behaves: its fields, how a valid row is recognised, and how its
// text is indexed for search (the same columns the upload path indexes).
type kind struct {
	table      string
	fields     []Field
	valid      string
	searchKind string
	searchCols []string
	derive     func(r row, vals map[string]json.RawMessage)
}

var (
	encounters = kind{table: "encounters", fields: Fields, valid: `AND json_type(data, '$.startAt') IN ('integer','real')`,
		searchKind: "encounter", searchCols: []string{"note"}}
	partners = kind{table: "partners", fields: PartnerFields, searchKind: "partner", searchCols: []string{"displayName", "note"},
		derive: func(_ row, vals map[string]json.RawMessage) {
			// A named partner is not anonymous; a partner with no name is, as in Tryst.
			if n, ok := vals["displayName"]; ok {
				if string(n) == "null" {
					vals["isAnonymous"] = json.RawMessage("1")
				} else {
					vals["isAnonymous"] = json.RawMessage("0")
				}
			}
		}}
	kinds = map[string]kind{"encounters": encounters, "partners": partners}
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

func load(tx *sql.Tx, owner string, k kind, id string) (row, error) {
	var data string
	err := tx.QueryRow(`SELECT data FROM cache_row WHERE owner_id = ? AND tbl = ? AND rid = ? `+k.valid, owner, k.table, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return row{}, ErrNoEntry
	}
	if err != nil {
		return row{}, err
	}
	return parseRow(data)
}

func currentOf(tx *sql.Tx, owner string, k kind, id string) (map[string]json.RawMessage, string, error) {
	head, _, err := Head(tx, owner)
	if err != nil {
		return nil, "", err
	}
	r, err := load(tx, owner, k, id)
	if err != nil {
		return nil, "", err
	}
	out := map[string]json.RawMessage{}
	for _, f := range k.fields {
		out[f.Name] = r.get(f.Name)
	}
	return out, head, nil
}

// Current returns the editable values of one of the owner's entries as raw JSON, and the revision
// they were read at (the form carries it back, so a stale save is refused).
func Current(tx *sql.Tx, owner, entryID string) (map[string]json.RawMessage, string, error) {
	return currentOf(tx, owner, encounters, entryID)
}

// PartnerCurrent is Current for one of the owner's partners.
func PartnerCurrent(tx *sql.Tx, owner, partnerID string) (map[string]json.RawMessage, string, error) {
	return currentOf(tx, owner, partners, partnerID)
}

// Item is one row of a catalog, or one partner.
type Item struct{ ID, Label string }

// Catalog lists the labels of one of the owner's catalogs (acts, positions, kinks, toys, occasions),
// in file order. A person may only choose from these (D-19).
func Catalog(tx *sql.Tx, owner, table string) ([]Item, error) {
	switch table {
	case "acts", "positions", "kinks", "toys", "occasions":
	default:
		return nil, &BadField{table, "is not a catalog"}
	}
	rows, err := tx.Query(`SELECT rid, COALESCE(json_extract(data, '$.label'), '') FROM cache_row WHERE owner_id = ? AND tbl = ? ORDER BY pos`, owner, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Item
	for rows.Next() {
		var it Item
		if err := rows.Scan(&it.ID, &it.Label); err != nil {
			return nil, err
		}
		if strings.TrimSpace(it.Label) != "" {
			out = append(out, it)
		}
	}
	return out, rows.Err()
}

// Partners lists the owner's partners (name empty for an anonymous one), in file order.
func Partners(tx *sql.Tx, owner string) ([]Item, error) {
	rows, err := tx.Query(`SELECT rid, COALESCE(json_extract(data, '$.displayName'), '') FROM cache_row WHERE owner_id = ? AND tbl = 'partners' ORDER BY pos`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Item
	for rows.Next() {
		var it Item
		if err := rows.Scan(&it.ID, &it.Label); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// Apply records one person's edit of one of the owner's entries as a new revision. in holds what
// was typed for each field the form offered; a field whose cleaned value equals the current one is
// not a change. baseRev is the revision the form was opened on. It returns the new revision's id and
// the names of the fields that changed. Content never appears in an error.
func Apply(tx *sql.Tx, now time.Time, actor, owner, entryID, baseRev string, in map[string]string) (string, []string, error) {
	return applyTo(tx, now, actor, owner, encounters, entryID, baseRev, in)
}

// PartnerApply is Apply for one of the owner's partners (a name and a note).
func PartnerApply(tx *sql.Tx, now time.Time, actor, owner, partnerID, baseRev string, in map[string]string) (string, []string, error) {
	return applyTo(tx, now, actor, owner, partners, partnerID, baseRev, in)
}

func applyTo(tx *sql.Tx, now time.Time, actor, owner string, k kind, id, baseRev string, in map[string]string) (string, []string, error) {
	r, err := load(tx, owner, k, id)
	if err != nil {
		return "", nil, err
	}
	vals := map[string]json.RawMessage{}
	for _, f := range k.fields {
		s, ok := in[f.Name]
		if !ok || f.Hidden {
			continue
		}
		var v json.RawMessage
		if cat := CatalogTable(f); cat != "" {
			v, err = cleanSet(tx, owner, f, cat, s, r.get(f.Name))
		} else {
			v, err = clean(k.fields, f.Name, s)
		}
		if err != nil {
			return "", nil, err
		}
		vals[f.Name] = v
	}
	return applyRaw(tx, now, actor, owner, k, id, baseRev, vals)
}

// cleanSet checks a catalog set: every token must be one of the owner's own catalog rows, or one
// the entry already holds (so an item removed from the catalog is not forced off by an unrelated
// edit). Empty means null.
func cleanSet(tx *sql.Tx, owner string, f Field, table, input string, current json.RawMessage) (json.RawMessage, error) {
	items, err := Catalog(tx, owner, table)
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	for _, it := range items {
		allowed["custom:"+it.ID] = true
	}
	var cur string
	if json.Unmarshal(current, &cur) == nil {
		for _, t := range strings.Split(cur, ",") {
			if t = strings.TrimSpace(t); t != "" {
				allowed[t] = true
			}
		}
	}
	keep, err := tokens(f.Name, strings.TrimSpace(input), func(t string) bool { return allowed[t] })
	if err != nil {
		return nil, err
	}
	if len(keep) == 0 {
		return json.RawMessage("null"), nil
	}
	return quote(strings.Join(keep, ",")), nil
}

func applyRaw(tx *sql.Tx, now time.Time, actor, owner string, k kind, id, baseRev string, vals map[string]json.RawMessage) (string, []string, error) {
	head, uploadID, err := Head(tx, owner)
	if err != nil {
		return "", nil, err
	}
	if baseRev != head {
		return "", nil, ErrStale
	}
	r, err := load(tx, owner, k, id)
	if err != nil {
		return "", nil, err
	}
	if k.derive != nil {
		k.derive(r, vals)
	}
	type diff struct {
		field    string
		old, new json.RawMessage
	}
	var diffs []diff
	for _, f := range k.fields {
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
	revID, err := newRevID()
	if err != nil {
		return "", nil, err
	}
	if _, err := tx.Exec(`INSERT INTO revision(id, owner_id, parent_id, kind, upload_id, created_by, created_at) VALUES (?,?,?,?,?,?,?)`,
		revID, owner, head, "edit", uploadID, actor, now.Unix()); err != nil {
		return "", nil, err
	}
	var names []string
	for _, d := range diffs {
		if _, err := tx.Exec(`INSERT INTO change(revision_id, owner_id, tbl, rid, field, old_value, new_value, op) VALUES (?,?,?,?,?,?,?,'set')`,
			revID, owner, k.table, id, d.field, string(d.old), string(d.new)); err != nil {
			return "", nil, err
		}
		r.set(d.field, d.new)
		names = append(names, d.field)
	}
	if err := store(tx, owner, k, id, &r, now.Unix(), names); err != nil {
		return "", nil, err
	}
	if _, err := tx.Exec(`UPDATE cache_head SET revision_id = ? WHERE owner_id = ?`, revID, owner); err != nil {
		return "", nil, err
	}
	return revID, names, nil
}

// store writes the changed row back to the cache, stamps Tryst's updatedAt (derived from the
// revision time, so a replay produces the same value) and refreshes the search text.
func store(tx *sql.Tx, owner string, k kind, id string, r *row, at int64, changed []string) error {
	if _, ok := r.vals["updatedAt"]; ok {
		r.set("updatedAt", json.RawMessage(strconv.FormatInt(at*1000, 10)))
	}
	if _, err := tx.Exec(`UPDATE cache_row SET data = ? WHERE owner_id = ? AND tbl = ? AND rid = ?`, r.String(), owner, k.table, id); err != nil {
		return err
	}
	touched := false
	for _, c := range changed {
		for _, sc := range k.searchCols {
			touched = touched || c == sc
		}
	}
	if !touched {
		return nil
	}
	if _, err := tx.Exec(`DELETE FROM search WHERE owner_id = ? AND kind = ? AND ref = ?`, owner, k.searchKind, id); err != nil {
		return err
	}
	var parts []string
	for _, c := range k.searchCols {
		var s string
		if json.Unmarshal(r.get(c), &s) == nil && strings.TrimSpace(s) != "" {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return nil
	}
	body := strings.Join(parts, "\n")
	if len(body) > maxIndexedBody {
		body = body[:maxIndexedBody]
	}
	_, err := tx.Exec(`INSERT INTO search(owner_id, kind, ref, body) VALUES (?,?,?,?)`, owner, k.searchKind, id, body)
	return err
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
		type target struct{ tbl, rid string }
		touched := map[target]*row{}
		fields := map[target][]string{}
		var order []target
		for _, c := range cl {
			k, known := kinds[c.tbl]
			if !known || c.op != "set" {
				continue
			}
			t := target{c.tbl, c.rid}
			r, ok := touched[t]
			if !ok {
				loaded, err := load(tx, owner, k, c.rid)
				if errors.Is(err, ErrNoEntry) {
					continue // the row is not in this upload; nothing to apply
				}
				if err != nil {
					return err
				}
				r = &loaded
				touched[t] = r
				order = append(order, t)
			}
			r.set(c.field, json.RawMessage(c.val))
			fields[t] = append(fields[t], c.field)
		}
		for _, t := range order {
			if err := store(tx, owner, kinds[t.tbl], t.rid, touched[t], v.at, fields[t]); err != nil {
				return err
			}
		}
	}
	return nil
}
