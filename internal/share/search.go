package share

import (
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Search (D-38). A member searches the notes and partner names of their own entries, or the notes
// of the entries a space shows them. The index is the FTS5 table filled at upload time; the
// space search goes through the same gate as every other shared view, plus the owner's
// include-notes choice, because what it matches is the note.

// Errors a page can show.
var (
	ErrQueryShort = errors.New("share: type at least two characters")
	ErrQueryLong  = errors.New("share: that search is too long")
)

const (
	minQuery   = 2
	maxQuery   = 100
	maxTerms   = 8
	maxTermLen = 40
)

// Match turns what a person typed into a safe FTS5 expression. Every word becomes a quoted
// string (so no operator, column filter or wildcard from the person can reach the engine), the
// words are ANDed, and the last one matches as a prefix so results appear while typing.
func Match(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if utf8.RuneCountInString(raw) < minQuery {
		return "", ErrQueryShort
	}
	if utf8.RuneCountInString(raw) > maxQuery {
		return "", ErrQueryLong
	}
	words := strings.FieldsFunc(raw, func(r rune) bool {
		return r < 0x20 || r == 0x7f || r == ' ' || r == '"' || r == '*' || r == ':' || r == '(' || r == ')' || r == '^' || r == '+' || r == '-' || r == ',' || r == '.' || r == ';'
	})
	var terms []string
	for _, w := range words {
		if w == "" {
			continue
		}
		if utf8.RuneCountInString(w) > maxTermLen {
			w = string([]rune(w)[:maxTermLen])
		}
		terms = append(terms, `"`+strings.ReplaceAll(w, `"`, `""`)+`"`)
		if len(terms) == maxTerms {
			break
		}
	}
	if len(terms) == 0 {
		return "", ErrQueryShort
	}
	terms[len(terms)-1] += "*"
	return "body : " + strings.Join(terms, " AND "), nil
}

// Seg is a piece of a result line; Hit says it is a matched word.
type Seg struct {
	Text string
	Hit  bool
}

// Found is one entry that matched.
type Found struct {
	OwnerID, OwnerName, EntryID string
	Local                       time.Time
	Segs                        []Seg
	Partner                     bool // matched a partner's name, not a note
	Mine                        bool
}

const snip = `snippet(search, 3, char(1), char(2), '...', 14)`

// segs splits an FTS snippet marked with control characters into plain pieces: nothing from the
// text is ever treated as markup.
func segs(s string) []Seg {
	var out []Seg
	hit := false
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, Seg{Text: cur.String(), Hit: hit})
			cur.Reset()
		}
	}
	for _, r := range s {
		switch r {
		case 1:
			flush()
			hit = true
		case 2:
			flush()
			hit = false
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

// SearchOwn searches one person's own entries: their notes and the names and notes of their partners.
// It reads only that person's rows. At most limit entries come back, newest first.
func SearchOwn(tx *sql.Tx, owner, raw string, limit int) ([]Found, error) {
	match, err := Match(raw)
	if err != nil {
		return nil, err
	}
	loc := zones{}.get(ZoneName(tx, owner))
	var name string
	_ = tx.QueryRow(`SELECT CASE WHEN display_name = '' THEN username ELSE display_name END FROM user WHERE id = ?`, owner).Scan(&name)
	seen := map[string]bool{}
	var out []Found
	collect := func(q string, partner bool) error {
		rows, err := tx.Query(q, match, owner, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, data, sn string
			if err := rows.Scan(&id, &data, &sn); err != nil {
				return err
			}
			if seen[id] {
				continue
			}
			e, ok := parse(data)
			if !ok {
				continue
			}
			seen[id] = true
			out = append(out, Found{OwnerID: owner, OwnerName: name, EntryID: id, Local: e.Start.In(loc), Segs: segs(sn), Partner: partner, Mine: true})
		}
		return rows.Err()
	}
	if err := collect(`SELECT c.rid, c.data, `+snip+` FROM search JOIN cache_row c ON c.owner_id = search.owner_id AND c.tbl = 'encounters' AND c.rid = search.ref
		WHERE search MATCH ? AND search.owner_id = ? AND search.kind = 'encounter' AND json_type(c.data, '$.startAt') IN ('integer','real')
		ORDER BY json_extract(c.data, '$.startAt') DESC LIMIT ?`, false); err != nil {
		return nil, err
	}
	if err := collect(`SELECT c.rid, c.data, `+snip+` FROM search
		JOIN cache_row ep ON ep.owner_id = search.owner_id AND ep.tbl = 'encounter_partner' AND json_extract(ep.data, '$.partnerId') = search.ref
		JOIN cache_row c ON c.owner_id = ep.owner_id AND c.tbl = 'encounters' AND c.rid = json_extract(ep.data, '$.encounterId')
		WHERE search MATCH ? AND search.owner_id = ? AND search.kind = 'partner' AND json_type(c.data, '$.startAt') IN ('integer','real')
		ORDER BY json_extract(c.data, '$.startAt') DESC LIMIT ?`, true); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Local.After(out[j].Local) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// SearchSpace searches the notes of the entries the space shows this viewer. An entry is a result
// only if Entries would list it for the viewer AND its owner includes notes: what is matched is the
// note, so an owner who keeps notes out cannot be found by them. Partner names are never searched
// here, because other members are not shown them by default.
func SearchSpace(tx *sql.Tx, spaceID, viewerID string, consent int, raw string, limit int) ([]Found, error) {
	match, err := Match(raw)
	if err != nil {
		return nil, err
	}
	if err := check(tx, spaceID, viewerID, consent); err != nil {
		return nil, err
	}
	where, args := scope(spaceID, consent, Query{})
	// The same member and entry conditions, with the index joined on and the note choice applied.
	const joinUser = "JOIN user u ON u.id = m.user_id"
	if !strings.Contains(where, joinUser) {
		return nil, errors.New("share: scope changed shape")
	}
	where = strings.Replace(where, joinUser, joinUser+" JOIN search ON search.owner_id = c.owner_id AND search.ref = c.rid AND search.kind = 'encounter'", 1)
	where += ` AND m.include_notes = 1 AND search MATCH ?`
	args = append(args, match, limit)
	rows, err := tx.Query(`SELECT c.owner_id, u.display_name, u.username, c.rid, c.data, COALESCE((SELECT tz FROM user_tz z WHERE z.user_id = c.owner_id), ''), `+snip+where+
		` ORDER BY json_extract(c.data, '$.startAt') DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	zs := zones{}
	var out []Found
	for rows.Next() {
		var owner, display, username, id, data, tz, sn string
		if err := rows.Scan(&owner, &display, &username, &id, &data, &tz, &sn); err != nil {
			return nil, err
		}
		e, ok := parse(data)
		if !ok {
			continue
		}
		f := Found{OwnerID: owner, OwnerName: display, EntryID: id, Segs: segs(sn), Mine: owner == viewerID, Local: e.Start.In(zs.get(tz))}
		if f.OwnerName == "" {
			f.OwnerName = username
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Segments splits an FTS snippet (marked with control characters by SnippetExpr) into plain pieces.
// It and SnippetExpr are exported for the administrator's search (D-46), which reads outside the
// sharing gate on purpose and so lives in its own package.
func Segments(snippet string) []Seg { return segs(snippet) }

// SnippetExpr is the SQL that cuts a short marked excerpt out of a matched note.
const SnippetExpr = snip
