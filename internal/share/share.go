// Package share is the ONE read gate for other people's entries (D-32, M4 piece A).
//
// Every view that shows a member's entries to anyone, in a space, goes through Entries. It asks
// spaces.Role first, so a person outside the space gets nothing, and then lets through only the
// entries of members who are active, whose account is active, who agreed to the CURRENT consent
// wording, and who are not paused; an entry its owner marked private never passes. Nothing is
// copied: the rows come straight from the owner's head-revision cache, so leaving, removal,
// pausing, disabling or deleting take effect on the next request.
//
// Hard constraints: 5 (nothing leaks outside a space); 8 (no entry content in errors or logs).
package share

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/herbiewalker/alcove/internal/profile"
	"github.com/herbiewalker/alcove/internal/spaces"
)

// ErrNotMember is returned when the viewer is not an active member of a live space. Callers show
// the same page as for a space that does not exist.
var ErrNotMember = errors.New("share: not a member of that space")

// ErrNeedConsent is returned when the viewer is a member who has not yet agreed to the current
// wording. Until they do, they see nobody else's entries, just as nobody sees theirs.
var ErrNeedConsent = errors.New("share: agree to the current wording first")

// Entry is the neutral subset of an encounter that shared views use. Fields beyond these stay in
// the owner's data until a view needs them (and passes the same gate).
type Entry struct {
	OwnerID   string
	OwnerName string
	ID        string
	Start     time.Time // the moment, in UTC
	Local     time.Time // the same moment in the owner's own time zone (D-35)
	Day       string    // the owner's calendar day, 2006-01-02
	Duration  *int      // minutes
	Rating    *int      // 1 to 5
	Note      string
	Mine      bool
	// Sets holds, per Tryst set column (acts, positions, kinks, ...), the members the entry names.
	// Compare views count them; they are labels or ids, never shown as text (D-35).
	Sets map[string][]string
}

// SetColumns are the Tryst columns that hold a comma separated set, in the order compare views list them.
var SetColumns = []string{"practicesPerformed", "practicesReceived", "positions", "kinks", "contexts", "occasions", "toys"}

// Query narrows the entries. A zero From or To is open on that side. Limit 0 means no limit.
type Query struct {
	From, To      time.Time
	Limit, Offset int
	// Owner narrows the result to one member (still inside the same gate); empty means every member the viewer may see.
	Owner string
}

// Entries returns the entries the viewer may see in the space, newest first, and the total that
// match (ignoring Limit and Offset). consent is the wording version members must have agreed to;
// production passes spaces.ConsentVersion.
func Entries(tx *sql.Tx, spaceID, viewerID string, consent int, q Query) ([]Entry, int, error) {
	if err := check(tx, spaceID, viewerID, consent); err != nil {
		return nil, 0, err
	}
	where, args := scope(spaceID, consent, q)
	var total int
	if err := tx.QueryRow(`SELECT COUNT(*)`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	sel := `SELECT c.owner_id, u.display_name, u.username, c.rid, c.data, m.include_notes, COALESCE((SELECT tz FROM user_tz z WHERE z.user_id = c.owner_id), '')` + where +
		` ORDER BY json_extract(c.data, '$.startAt') DESC, c.owner_id, c.rid`
	if q.Limit > 0 {
		sel += ` LIMIT ? OFFSET ?`
		args = append(args, q.Limit, q.Offset)
	}
	rows, err := tx.Query(sel, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Entry
	zs := zones{}
	for rows.Next() {
		var owner, display, username, rid, data, tz string
		var notes int
		if err := rows.Scan(&owner, &display, &username, &rid, &data, &notes, &tz); err != nil {
			return nil, 0, err
		}
		e, ok := parse(data)
		if !ok {
			continue
		}
		e.OwnerID, e.ID, e.Mine = owner, rid, owner == viewerID
		e.Local = e.Start.In(zs.get(tz))
		e.Day = e.Local.Format("2006-01-02")
		e.OwnerName = display
		if e.OwnerName == "" {
			e.OwnerName = username
		}
		if notes == 0 {
			e.Note = ""
		}
		out = append(out, e)
	}
	return out, total, rows.Err()
}

// check is the viewer half of the gate: an active member of a live space, on the current wording.
func check(tx *sql.Tx, spaceID, viewerID string, consent int) error {
	if _, ok := spaces.Role(tx, spaceID, viewerID); !ok {
		return ErrNotMember
	}
	var mine int
	if err := tx.QueryRow(`SELECT consent_version FROM membership WHERE space_id = ? AND user_id = ?`, spaceID, viewerID).Scan(&mine); err != nil {
		return err
	}
	if mine < consent {
		return ErrNeedConsent
	}
	return nil
}

// scope is the owner half of the gate, as SQL: which cached encounters may be shown in the space.
// Entries and Days both build on it so there is exactly one definition.
func scope(spaceID string, consent int, q Query) (string, []any) {
	where := ` FROM cache_row c
		JOIN membership m ON m.user_id = c.owner_id AND m.space_id = ?
		JOIN space s ON s.id = m.space_id
		JOIN user u ON u.id = m.user_id
		WHERE c.tbl = 'encounters'
		  AND m.state = 'active' AND m.paused = 0 AND m.consent_version >= ?
		  AND s.deleted_at IS NULL AND u.status = 'active' AND u.deleted_at IS NULL
		  AND NOT EXISTS (SELECT 1 FROM entry_flag f WHERE f.owner_id = c.owner_id AND f.entry_id = c.rid AND f.private = 1)
		  AND json_type(c.data, '$.startAt') IN ('integer','real')`
	args := []any{spaceID, consent}
	if !q.From.IsZero() {
		where += ` AND json_extract(c.data, '$.startAt') >= ?`
		args = append(args, q.From.UnixMilli())
	}
	if q.Owner != "" {
		where += ` AND c.owner_id = ?`
		args = append(args, q.Owner)
	}
	if !q.To.IsZero() {
		where += ` AND json_extract(c.data, '$.startAt') < ?`
		args = append(args, q.To.UnixMilli())
	}
	return where, args
}

// Day is one calendar day and how many entries each member has on it, each counted in its
// owner's own time zone.
type Day struct {
	Date   string // 2006-01-02
	ByUser map[string]int
}

// Days returns the days from the date of `from` up to but not including the date of `to`, that have
// at least one shown entry, with a count per member. It goes through the same gate as Entries.
func Days(tx *sql.Tx, spaceID, viewerID string, consent int, from, to time.Time) ([]Day, error) {
	if err := check(tx, spaceID, viewerID, consent); err != nil {
		return nil, err
	}
	// Zones differ by up to a day from UTC, so ask for a day more on each side and cut by date.
	where, args := scope(spaceID, consent, Query{From: from.Add(-26 * time.Hour), To: to.Add(26 * time.Hour)})
	rows, err := tx.Query(`SELECT c.owner_id, json_extract(c.data, '$.startAt'), COALESCE((SELECT tz FROM user_tz z WHERE z.user_id = c.owner_id), '')`+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	lo, hi := from.Format("2006-01-02"), to.Format("2006-01-02")
	zs := zones{}
	counts := map[string]map[string]int{}
	for rows.Next() {
		var owner, tz string
		var ms float64
		if err := rows.Scan(&owner, &ms, &tz); err != nil {
			return nil, err
		}
		d := time.UnixMilli(int64(ms)).In(zs.get(tz)).Format("2006-01-02")
		if d < lo || d >= hi {
			continue
		}
		if counts[d] == nil {
			counts[d] = map[string]int{}
		}
		counts[d][owner]++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []Day
	for d := from; d.Before(to); d = d.AddDate(0, 0, 1) {
		if m, ok := counts[d.Format("2006-01-02")]; ok {
			out = append(out, Day{Date: d.Format("2006-01-02"), ByUser: m})
		}
	}
	return out, nil
}

// Mine is one of the viewer's own entries, as the owner sees them on "My entries".
type Mine struct {
	Entry
	Private bool
}

// Own returns the person's own entries, newest first, with the private flag. It reads only the
// person's own cache and is never a way to see anyone else's.
func Own(tx *sql.Tx, owner string, limit, offset int) ([]Mine, int, error) {
	var total int
	const where = ` FROM cache_row c WHERE c.owner_id = ? AND c.tbl = 'encounters' AND json_type(c.data, '$.startAt') IN ('integer','real')`
	if err := tx.QueryRow(`SELECT COUNT(*)`+where, owner).Scan(&total); err != nil {
		return nil, 0, err
	}
	loc := zones{}.get(ZoneName(tx, owner))
	rows, err := tx.Query(`SELECT c.rid, c.data, COALESCE((SELECT f.private FROM entry_flag f WHERE f.owner_id = c.owner_id AND f.entry_id = c.rid), 0)`+where+
		` ORDER BY json_extract(c.data, '$.startAt') DESC, c.rid LIMIT ? OFFSET ?`, owner, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Mine
	for rows.Next() {
		var rid, data string
		var priv int
		if err := rows.Scan(&rid, &data, &priv); err != nil {
			return nil, 0, err
		}
		e, ok := parse(data)
		if !ok {
			continue
		}
		e.OwnerID, e.ID, e.Mine = owner, rid, true
		e.Local = e.Start.In(loc)
		e.Day = e.Local.Format("2006-01-02")
		out = append(out, Mine{Entry: e, Private: priv == 1})
	}
	return out, total, rows.Err()
}

// SetPrivate marks one of the owner's own entries private or shared again. The entry must exist in
// the owner's current data, so the flag table cannot be filled with ids that mean nothing.
func SetPrivate(tx *sql.Tx, now time.Time, owner, entryID string, private bool) error {
	var one int
	if err := tx.QueryRow(`SELECT 1 FROM cache_row WHERE owner_id = ? AND tbl = 'encounters' AND rid = ?`, owner, entryID).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNoEntry
		}
		return err
	}
	v := 0
	if private {
		v = 1
	}
	_, err := tx.Exec(`INSERT INTO entry_flag(owner_id, entry_id, private, updated_at) VALUES (?,?,?,?)
		ON CONFLICT(owner_id, entry_id) DO UPDATE SET private = excluded.private, updated_at = excluded.updated_at`,
		owner, entryID, v, now.Unix())
	return err
}

// ErrNoEntry means the entry is not in the person's current data.
var ErrNoEntry = errors.New("share: no such entry")

func parse(data string) (Entry, bool) {
	var r struct {
		StartAt  *float64 `json:"startAt"`
		Duration *float64 `json:"durationMin"`
		Rating   *float64 `json:"satisfactionRating"`
		Note     *string  `json:"note"`
	}
	if json.Unmarshal([]byte(data), &r) != nil || r.StartAt == nil {
		return Entry{}, false
	}
	var sets map[string]*string
	// Not every column is a string. Unmarshal still fills the ones that are and reports the first mismatch, which is fine here.
	_ = json.Unmarshal([]byte(data), &sets)
	e := Entry{Start: time.UnixMilli(int64(*r.StartAt)).UTC()}
	if r.Duration != nil {
		d := int(*r.Duration)
		e.Duration = &d
	}
	if r.Rating != nil {
		d := int(*r.Rating)
		e.Rating = &d
	}
	if r.Note != nil {
		e.Note = *r.Note
	}
	for _, col := range SetColumns {
		if v := sets[col]; v != nil && *v != "" {
			for _, m := range strings.Split(*v, ",") {
				if m = strings.TrimSpace(m); m != "" {
					if e.Sets == nil {
						e.Sets = map[string][]string{}
					}
					e.Sets[col] = append(e.Sets[col], m)
				}
			}
		}
	}
	return e, true
}

// Card is one member as the space shows them: the name always, the shared profile fields only
// while the member passes the same conditions as their entries (D-33).
type Card struct {
	UserID, Name, Username, Role string
	Fields                       []CardField
	Hidden                       bool // paused or on older wording: nothing but the name is shown
	Mine                         bool
	Photo                        bool // a profile picture can be shown on this card (D-52)
}

// CardField is a label and the text to show.
type CardField struct{ Label, Value string }

// Cards returns a card for every active member of the space the viewer belongs to, in the order
// owner first, then by name. Profile fields appear only for members who are not paused, have
// agreed to the current wording, and have shared that field; a birth date is shown as an age.
func Cards(tx *sql.Tx, spaceID, viewerID string, consent int, now time.Time) ([]Card, error) {
	if _, ok := spaces.Role(tx, spaceID, viewerID); !ok {
		return nil, ErrNotMember
	}
	var mine int
	if err := tx.QueryRow(`SELECT consent_version FROM membership WHERE space_id = ? AND user_id = ?`, spaceID, viewerID).Scan(&mine); err != nil {
		return nil, err
	}
	if mine < consent {
		return nil, ErrNeedConsent
	}
	rows, err := tx.Query(`SELECT m.user_id, u.display_name, u.username, m.role, (m.paused = 0 AND m.consent_version >= ?)
		FROM membership m JOIN space s ON s.id = m.space_id JOIN user u ON u.id = m.user_id
		WHERE m.space_id = ? AND m.state = 'active' AND s.deleted_at IS NULL AND u.status = 'active' AND u.deleted_at IS NULL
		ORDER BY (m.role = 'owner') DESC, u.display_name COLLATE NOCASE, m.user_id`, consent, spaceID)
	if err != nil {
		return nil, err
	}
	var cards []Card
	for rows.Next() {
		var c Card
		var ok int
		if err := rows.Scan(&c.UserID, &c.Name, &c.Username, &c.Role, &ok); err != nil {
			rows.Close()
			return nil, err
		}
		c.Hidden, c.Mine = ok == 0, c.UserID == viewerID
		cards = append(cards, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range cards {
		if cards[i].Hidden {
			continue
		}
		pr, err := tx.Query(`SELECT field, value FROM profile WHERE owner_id = ? AND shared = 1`, cards[i].UserID)
		if err != nil {
			return nil, err
		}
		got := map[string]string{}
		for pr.Next() {
			var k, v string
			if err := pr.Scan(&k, &v); err != nil {
				pr.Close()
				return nil, err
			}
			got[k] = v
		}
		pr.Close()
		cards[i].Photo = hasProfilePhoto(tx, spaceID, cards[i].UserID, cards[i].Mine)
		for _, f := range profile.Fields {
			if v, ok := got[f.Key]; ok && v != "" {
				if shown := profile.Display(f.Key, v, now); shown != "" {
					cards[i].Fields = append(cards[i].Fields, CardField{f.Label, shown})
				}
			}
		}
	}
	return cards, nil
}

// Member is a person the compare views may include: they pass every owner condition of the gate.
type Member struct {
	UserID, Name string
	Schema       int // the Tryst database version of their current backup, 0 if unknown
	Mine         bool
}

// Roster lists the members whose entries the viewer may see in the space (not paused, current
// wording, live account), owner first, then by name. It is the member list for compare views.
func Roster(tx *sql.Tx, spaceID, viewerID string, consent int) ([]Member, error) {
	if err := check(tx, spaceID, viewerID, consent); err != nil {
		return nil, err
	}
	rows, err := tx.Query(`SELECT m.user_id, u.display_name, u.username, COALESCE((SELECT h.tryst_schema FROM cache_head h WHERE h.owner_id = m.user_id), 0)
		FROM membership m JOIN space s ON s.id = m.space_id JOIN user u ON u.id = m.user_id
		WHERE m.space_id = ? AND m.state = 'active' AND m.paused = 0 AND m.consent_version >= ?
		  AND s.deleted_at IS NULL AND u.status = 'active' AND u.deleted_at IS NULL
		ORDER BY (m.role = 'owner') DESC, u.display_name COLLATE NOCASE, m.user_id`, spaceID, consent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Member
	for rows.Next() {
		var m Member
		var username string
		if err := rows.Scan(&m.UserID, &m.Name, &username, &m.Schema); err != nil {
			return nil, err
		}
		if m.Name == "" {
			m.Name = username
		}
		m.Mine = m.UserID == viewerID
		out = append(out, m)
	}
	return out, rows.Err()
}
