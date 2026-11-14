package share

import (
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// DetailField is one labelled value on an entry's page.
type DetailField struct{ Label, Value string }

// DetailGroup is a labelled list, such as the acts or the toys.
type DetailGroup struct {
	Label string
	Items []string
}

// Detail is one whole entry as a person is allowed to see it (D-37). The same struct serves the
// owner (everything) and a space member (what the owner shares).
type Detail struct {
	Entry
	Fields  []DetailField
	Groups  []DetailGroup
	Partner []string // names, when allowed
	// PartnersHidden counts people who were with the owner but are not named to this viewer.
	PartnersHidden int
	NoteHidden     bool // the owner keeps notes out of the space
	Private        bool // the owner's own view: this entry is private
}

type options struct {
	notes, partnerNames bool
}

// OwnDetail returns one of the person's own entries in full. It reads only that person's rows.
func OwnDetail(tx *sql.Tx, owner, entryID string) (Detail, error) {
	var data string
	err := tx.QueryRow(`SELECT data FROM cache_row WHERE owner_id = ? AND tbl = 'encounters' AND rid = ?
		AND json_type(data, '$.startAt') IN ('integer','real')`, owner, entryID).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return Detail{}, ErrNoEntry
	}
	if err != nil {
		return Detail{}, err
	}
	var name string
	_ = tx.QueryRow(`SELECT CASE WHEN display_name = '' THEN username ELSE display_name END FROM user WHERE id = ?`, owner).Scan(&name)
	d, err := build(tx, owner, entryID, data, options{notes: true, partnerNames: true})
	if err != nil {
		return Detail{}, err
	}
	d.OwnerID, d.OwnerName, d.Mine = owner, name, true
	var priv int
	_ = tx.QueryRow(`SELECT private FROM entry_flag WHERE owner_id = ? AND entry_id = ?`, owner, entryID).Scan(&priv)
	d.Private = priv == 1
	return d, nil
}

// EntryDetail returns one entry of a member of the space, but only if Entries would list it for
// this viewer: the same member conditions, the same private flag. The owner's choices decide the
// rest: a note is left out unless they include notes, partners are not named unless they show
// partner names. Anything else, including an entry that does not exist, is ErrNoEntry, so the
// caller cannot tell them apart.
func EntryDetail(tx *sql.Tx, spaceID, viewerID string, consent int, ownerID, entryID string) (Detail, error) {
	if err := check(tx, spaceID, viewerID, consent); err != nil {
		return Detail{}, err
	}
	where, args := scope(spaceID, consent, Query{})
	where += ` AND c.owner_id = ? AND c.rid = ?`
	args = append(args, ownerID, entryID)
	var data, display, username string
	var notes, partners int
	err := tx.QueryRow(`SELECT c.data, u.display_name, u.username, m.include_notes, m.show_partner_names`+where, args...).Scan(&data, &display, &username, &notes, &partners)
	if errors.Is(err, sql.ErrNoRows) {
		return Detail{}, ErrNoEntry
	}
	if err != nil {
		return Detail{}, err
	}
	d, err := build(tx, ownerID, entryID, data, options{notes: notes == 1, partnerNames: partners == 1})
	if err != nil {
		return Detail{}, err
	}
	d.OwnerID, d.OwnerName, d.Mine = ownerID, display, ownerID == viewerID
	if d.OwnerName == "" {
		d.OwnerName = username
	}
	return d, nil
}

var setLabels = map[string]string{
	"practicesPerformed": "Acts done", "practicesReceived": "Acts received", "positions": "Positions", "kinks": "Kinks",
	"contexts": "Settings", "occasions": "Occasions", "toys": "Toys",
}

// catalogOf is the owner's table that holds the labels for a set column ("" for plain enums).
var catalogOf = map[string]string{
	"practicesPerformed": "acts", "practicesReceived": "acts", "positions": "positions", "kinks": "kinks", "toys": "toys", "occasions": "occasions",
}

func build(tx *sql.Tx, owner, entryID, data string, opt options) (Detail, error) {
	e, ok := parse(data)
	if !ok {
		return Detail{}, ErrNoEntry
	}
	e.ID = entryID
	zs := zones{}
	e.Local = e.Start.In(zs.get(ZoneName(tx, owner)))
	e.Day = e.Local.Format("2006-01-02")
	var raw map[string]json.RawMessage
	_ = json.Unmarshal([]byte(data), &raw)
	str := func(k string) string {
		var s string
		if json.Unmarshal(raw[k], &s) == nil {
			return strings.TrimSpace(s)
		}
		return ""
	}
	intv := func(k string) (int, bool) {
		var n float64
		if json.Unmarshal(raw[k], &n) == nil {
			return int(n), true
		}
		return 0, false
	}
	d := Detail{Entry: e}
	d.Entry.Sets = nil // the page lists them below, resolved; nothing raw travels on
	add := func(label, v string) {
		if v != "" {
			d.Fields = append(d.Fields, DetailField{label, v})
		}
	}
	if e.Duration != nil {
		add("Length", strconv.Itoa(*e.Duration)+" min")
	}
	if e.Rating != nil {
		add("Rating", strconv.Itoa(*e.Rating)+" of 5")
	}
	add("Mood", pretty(str("mood")))
	add("Who started it", pretty(str("initiator")))
	add("Protection", prettyList(str("protectionUsed")))
	if n, ok := intv("orgasmCountSelf"); ok {
		add("Orgasms", strconv.Itoa(n))
	}

	catalogs := map[string]map[string]string{}
	label := func(table, token string) string {
		if table == "" {
			return pretty(token)
		}
		if !strings.HasPrefix(token, "custom:") {
			return pretty(token)
		}
		id := strings.TrimPrefix(token, "custom:")
		m, ok := catalogs[table]
		if !ok {
			m = map[string]string{}
			rows, err := tx.Query(`SELECT rid, COALESCE(json_extract(data, '$.label'), '') FROM cache_row WHERE owner_id = ? AND tbl = ?`, owner, table)
			if err == nil {
				for rows.Next() {
					var rid, l string
					if rows.Scan(&rid, &l) == nil {
						m[rid] = l
					}
				}
				rows.Close()
			}
			catalogs[table] = m
		}
		if l := m[id]; l != "" {
			return l
		}
		return "(a removed item)"
	}
	for _, col := range SetColumns {
		toks := splitTokens(str(col))
		if len(toks) == 0 {
			continue
		}
		var items []string
		for _, t := range toks {
			items = append(items, label(catalogOf[col], t))
		}
		sort.Strings(items)
		d.Groups = append(d.Groups, DetailGroup{Label: setLabels[col], Items: items})
	}
	if toks := ejaculationLabels(str("ejaculationLocations")); len(toks) > 0 {
		var items []string
		for _, t := range toks {
			items = append(items, label("ejaculation_locations", t))
		}
		sort.Strings(items)
		d.Groups = append(d.Groups, DetailGroup{Label: "Finish locations", Items: items})
	}

	// Partners: from the cross reference table, named only when allowed.
	rows, err := tx.Query(`SELECT COALESCE(json_extract(p.data, '$.displayName'), ''), COALESCE(json_extract(p.data, '$.isAnonymous'), 0)
		FROM cache_row ep JOIN cache_row p ON p.owner_id = ep.owner_id AND p.tbl = 'partners' AND p.rid = json_extract(ep.data, '$.partnerId')
		WHERE ep.owner_id = ? AND ep.tbl = 'encounter_partner' AND json_extract(ep.data, '$.encounterId') = ?
		ORDER BY p.pos`, owner, entryID)
	if err != nil {
		return Detail{}, err
	}
	for rows.Next() {
		var name string
		var anon int
		if err := rows.Scan(&name, &anon); err != nil {
			rows.Close()
			return Detail{}, err
		}
		if !opt.partnerNames {
			d.PartnersHidden++
			continue
		}
		if name == "" || anon == 1 {
			name = "An anonymous partner"
		}
		d.Partner = append(d.Partner, name)
	}
	rows.Close()

	if opt.notes {
		d.Note = e.Note
	} else {
		d.Note = ""
		d.NoteHidden = e.Note != ""
	}
	return d, nil
}

func splitTokens(s string) []string {
	var out []string
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// ejaculationLabels reads Tryst's "index=A|B,index=C" map and returns the distinct locations.
func ejaculationLabels(s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, pair := range splitTokens(s) {
		_, v, ok := strings.Cut(pair, "=")
		if !ok {
			v = pair
		}
		for _, t := range strings.Split(v, "|") {
			if t = strings.TrimSpace(t); t != "" && !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	return out
}

// pretty turns an enum name such as ONE_NIGHT into "One night"; an empty value stays empty.
func pretty(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.ToLower(strings.ReplaceAll(s, "_", " "))
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

func prettyList(s string) string {
	var out []string
	for _, t := range splitTokens(s) {
		out = append(out, pretty(t))
	}
	return strings.Join(out, ", ")
}
