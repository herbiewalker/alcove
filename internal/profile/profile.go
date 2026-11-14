// Package profile holds a member's profile: a few optional text fields, each with its own
// share flag that defaults to private (D-33, M4 piece B). Nothing here decides who may see a
// field; that is share.Cards. This package stores, validates and prefills.
//
// Hard constraints: 5 (private by default, birth date shared only as an age); 11 (adults only:
// a birth date under 18 is refused); 8 (no profile values in errors or logs).
package profile

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Field is one profile field. Key is the stored name; Tryst is the column in a Tryst profile row.
type Field struct {
	Key, Label, Tryst string
	Max               int
}

// Fields is the whole list, in the order pages show them.
var Fields = []Field{
	{"about", "About", "note", 300},
	{"sex", "Sex", "sex", 120},
	{"gender", "Gender", "gender", 120},
	{"birth", "Birth date", "birthDate", 10},
	{"ethnicity", "Ethnicity", "ethnicity", 120},
	{"height", "Height", "height", 120},
	{"body_type", "Body type", "bodyType", 120},
	{"orientation", "Orientation", "orientation", 120},
	{"location", "Location", "location", 120},
}

// Birth is the key of the birth date, which is shared only as an age.
const Birth = "birth"

// Errors a page can show. None carries a value.
var (
	ErrUnknownField = errors.New("profile: unknown field")
	ErrTooLong      = errors.New("profile: that is too long")
	ErrBadBirth     = errors.New("profile: the birth date must be a real date like 1990-04-23")
	ErrUnderage     = errors.New("profile: Alcove is for adults, so the birth date must be 18 years ago or more")
	ErrNoProfile    = errors.New("profile: there is no profile in your latest backup")
)

// Value is one stored field.
type Value struct {
	Value  string
	Shared bool
}

func find(key string) (Field, bool) {
	for _, f := range Fields {
		if f.Key == key {
			return f, true
		}
	}
	return Field{}, false
}

// Get returns a person's stored fields by key.
func Get(tx *sql.Tx, owner string) (map[string]Value, error) {
	rows, err := tx.Query(`SELECT field, value, shared FROM profile WHERE owner_id = ?`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Value{}
	for rows.Next() {
		var k string
		var v Value
		var sh int
		if err := rows.Scan(&k, &v.Value, &sh); err != nil {
			return nil, err
		}
		v.Shared = sh == 1
		out[k] = v
	}
	return out, rows.Err()
}

// Clean validates and normalises one value. An empty result means "no value".
func Clean(key, v string, now time.Time) (string, error) {
	f, ok := find(key)
	if !ok {
		return "", ErrUnknownField
	}
	v = strings.TrimSpace(v)
	if key == Birth {
		if v == "" {
			return "", nil
		}
		b, err := time.Parse("2006-01-02", v)
		if err != nil {
			return "", ErrBadBirth
		}
		if Age(b, now) < 18 {
			if b.After(now) {
				return "", ErrBadBirth
			}
			return "", ErrUnderage
		}
		if Age(b, now) > 120 {
			return "", ErrBadBirth
		}
		return v, nil
	}
	if utf8.RuneCountInString(v) > f.Max {
		return "", ErrTooLong
	}
	for _, r := range v {
		if r < 0x20 && r != '\n' || r == 0x7f {
			return "", ErrTooLong
		}
	}
	return v, nil
}

// Age is the whole years between a birth date and now.
func Age(birth, now time.Time) int {
	a := now.Year() - birth.Year()
	if now.Month() < birth.Month() || now.Month() == birth.Month() && now.Day() < birth.Day() {
		a--
	}
	return a
}

// Display is how a stored value is shown to OTHER people: a birth date becomes "Age N".
func Display(key, value string, now time.Time) string {
	if key == Birth {
		b, err := time.Parse("2006-01-02", value)
		if err != nil {
			return ""
		}
		return "Age " + strconv.Itoa(Age(b, now))
	}
	return value
}

// Save replaces the person's whole profile with values and shared flags. Every value is validated
// first; one bad field changes nothing. A field with an empty value is removed, and an empty field
// cannot be shared.
func Save(tx *sql.Tx, now time.Time, owner string, values map[string]string, shared map[string]bool) error {
	clean := map[string]string{}
	for _, f := range Fields {
		v, err := Clean(f.Key, values[f.Key], now)
		if err != nil {
			return err
		}
		clean[f.Key] = v
	}
	for _, f := range Fields {
		v := clean[f.Key]
		if v == "" {
			if _, err := tx.Exec(`DELETE FROM profile WHERE owner_id = ? AND field = ?`, owner, f.Key); err != nil {
				return err
			}
			continue
		}
		sh := 0
		if shared[f.Key] {
			sh = 1
		}
		if _, err := tx.Exec(`INSERT INTO profile(owner_id, field, value, shared, updated_at) VALUES (?,?,?,?,?)
			ON CONFLICT(owner_id, field) DO UPDATE SET value = excluded.value, shared = excluded.shared, updated_at = excluded.updated_at`,
			owner, f.Key, v, sh, now.Unix()); err != nil {
			return err
		}
	}
	return nil
}

// Offer is one thing the latest backup's profile row has, offered for import.
type Offer struct {
	Key, Label, Value string
	Name              bool // the name shown to others
}

// NameKey is the pseudo field for the display name in an offer.
const NameKey = "name"

// Offers reads the profile row in the owner's cache (the head revision) and lists what could be
// imported. It never imports anything.
func Offers(tx *sql.Tx, owner string, now time.Time) ([]Offer, error) {
	var data string
	err := tx.QueryRow(`SELECT data FROM cache_row WHERE owner_id = ? AND tbl = 'profile' ORDER BY pos LIMIT 1`, owner).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoProfile
	}
	if err != nil {
		return nil, err
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal([]byte(data), &raw) != nil {
		return nil, ErrNoProfile
	}
	text := func(col string) string {
		var s string
		if json.Unmarshal(raw[col], &s) == nil {
			return strings.TrimSpace(s)
		}
		var n json.Number
		if json.Unmarshal(raw[col], &n) == nil {
			return n.String()
		}
		return ""
	}
	var out []Offer
	if n := text("displayName"); n != "" {
		out = append(out, Offer{Key: NameKey, Label: "Name shown to others", Value: n, Name: true})
	}
	for _, f := range Fields {
		v := text(f.Tryst)
		if f.Key == Birth && v != "" {
			// Tryst keeps a date; accept epoch milliseconds or an ISO date.
			if ms, err := strconv.ParseInt(v, 10, 64); err == nil {
				v = time.UnixMilli(ms).UTC().Format("2006-01-02")
			} else if len(v) > 10 {
				v = v[:10]
			}
		}
		if v == "" {
			continue
		}
		if c, err := Clean(f.Key, v, now); err != nil || c == "" {
			continue // a value that would not pass the rules is simply not offered
		}
		out = append(out, Offer{Key: f.Key, Label: f.Label, Value: v})
	}
	if len(out) == 0 {
		return nil, ErrNoProfile
	}
	return out, nil
}
