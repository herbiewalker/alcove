// Package edit is how Alcove changes a person's entries (D-19, D-42). An edit never touches an
// upload: it is a revision of kind "edit" with change records, and the row cache is the upload plus
// every live edit replayed in order.
package edit

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Errors carry no content (constraint 8): they name the field's rule, never the value.
var (
	ErrStale    = errors.New("edit: this entry changed since you opened it")
	ErrNoChange = errors.New("edit: nothing was changed")
	ErrNoEntry  = errors.New("edit: no such entry")
	ErrNoHead   = errors.New("edit: there is no current backup to edit")
)

// BadField is a value that breaks its field's rule.
type BadField struct{ Field, Rule string }

func (e *BadField) Error() string { return "edit: " + e.Field + " " + e.Rule }

// Field is one editable column (D-19). Kind is one of time, int, enum, tokens, text, places, or
// "set:<catalog table>" for a set chosen from the person's own catalog. Hidden fields are written
// only by Alcove itself (a partner's anonymous flag follows their name).
type Field struct {
	Name   string
	Label  string
	Kind   string
	Max    int // text: the longest accepted, in bytes
	Hidden bool
}

// Fields are the encounter columns a person may change, in display order. Everything else in a
// row, and every other table, passes through untouched.
var Fields = []Field{
	{Name: "startAt", Label: "When", Kind: "time"},
	{Name: "durationMin", Label: "Length (minutes)", Kind: "int"},
	{Name: "satisfactionRating", Label: "Rating", Kind: "int"},
	{Name: "mood", Label: "Mood", Kind: "enum"},
	{Name: "initiator", Label: "Who started it", Kind: "enum"},
	{Name: "protectionUsed", Label: "Protection", Kind: "tokens"},
	{Name: "orgasmCountSelf", Label: "Orgasms (yours)", Kind: "int"},
	{Name: "practicesPerformed", Label: "Acts done", Kind: "set:acts"},
	{Name: "practicesReceived", Label: "Acts received", Kind: "set:acts"},
	{Name: "positions", Label: "Positions", Kind: "set:positions"},
	{Name: "kinks", Label: "Kinks", Kind: "set:kinks"},
	{Name: "contexts", Label: "Settings", Kind: "places"},
	{Name: "toys", Label: "Toys", Kind: "set:toys"},
	{Name: "occasions", Label: "Occasions", Kind: "set:occasions"},
	{Name: "note", Label: "Note", Kind: "text", Max: maxNote},
}

// PartnerFields are the partner columns a person may change: a name and a note. Naming a partner
// also clears the anonymous flag, and clearing the name sets it, as Tryst does.
var PartnerFields = []Field{
	{Name: "displayName", Label: "Name", Kind: "text", Max: maxName},
	{Name: "note", Label: "Note", Kind: "text", Max: maxNote},
	{Name: "isAnonymous", Label: "Anonymous", Kind: "bool", Hidden: true},
}

// Moods, Initiators, Protections and Places are Tryst's own values. Tryst rejects any other
// spelling, so Alcove offers nothing else. Copied from Tryst's Enums.kt; a new Tryst value is added here.
var (
	Moods = []string{"AMAZING", "EUPHORIC", "PASSIONATE", "HORNY", "SENSUAL", "WILD", "ADVENTUROUS", "KINKY", "NAUGHTY", "PLAYFUL",
		"ROMANTIC", "INTIMATE", "TENDER", "CONNECTED", "LOVED", "DESIRED", "CONFIDENT", "SAFE", "VULNERABLE", "EMOTIONAL", "CURIOUS",
		"SPONTANEOUS", "TIPSY", "SATISFIED", "RELAXED", "SLEEPY", "GOOD", "NEUTRAL", "BORED", "AWKWARD", "MEH", "FRUSTRATED",
		"DISAPPOINTED", "BAD"}
	Initiators  = []string{"ME", "PARTNER", "MUTUAL"}
	Protections = []string{"NONE", "CONDOM", "INTERNAL_CONDOM", "DENTAL_DAM", "PILL", "IUD", "IMPLANT", "PATCH", "VAGINAL_RING",
		"INJECTION", "DIAPHRAGM", "CERVICAL_CAP", "SPERMICIDE", "SPONGE", "FERTILITY_AWARENESS", "WITHDRAWAL", "VASECTOMY",
		"TUBAL_LIGATION", "PREP", "PEP", "DOXY_PEP", "EMERGENCY_CONTRACEPTION", "OTHER"}
	Places = []string{"HOME", "BEDROOM", "LIVING_ROOM", "KITCHEN", "BATHROOM", "SHOWER", "BATH", "BALCONY", "BACKYARD", "ROOFTOP", "CAR",
		"HOTEL", "AIRBNB", "FRIENDS_FAMILY", "VACATION", "OUTDOORS", "NATURE", "BEACH", "CAMPING", "BOAT", "POOL", "HOT_TUB", "GYM",
		"OFFICE", "CINEMA", "CHANGING_ROOM", "PUBLIC_RESTROOM", "PARKING_LOT", "ELEVATOR", "BAR_CLUB"}
)

const (
	maxNote   = 20000
	maxName   = 80
	minStart  = int64(0)             // 1970
	maxStart  = int64(4102444800000) // 2100-01-01
	maxLength = 7 * 24 * 60          // minutes
	maxCount  = 99
	maxTokens = 60
)

func lookup(fields []Field, name string) (Field, bool) {
	for _, f := range fields {
		if f.Name == name {
			return f, true
		}
	}
	return Field{}, false
}

func in(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// CatalogTable names the catalog a "set:" field draws from ("" for any other field).
func CatalogTable(f Field) string {
	if strings.HasPrefix(f.Kind, "set:") {
		return strings.TrimPrefix(f.Kind, "set:")
	}
	return ""
}

// Clean turns what a person typed into the raw JSON Tryst stores for an entry column. Empty means
// "not set" (null; an empty string for protection, as Tryst writes it), except the start time.
// A catalog set is only checked for shape here; Apply checks it against the person's own catalog.
func Clean(name, input string) (json.RawMessage, error) {
	return clean(Fields, name, input)
}

func clean(fields []Field, name, input string) (json.RawMessage, error) {
	f, ok := lookup(fields, name)
	if !ok || f.Hidden {
		return nil, &BadField{name, "cannot be edited"}
	}
	s := strings.TrimSpace(input)
	null := json.RawMessage("null")
	switch f.Kind {
	case "time":
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n < minStart || n >= maxStart {
			return nil, &BadField{name, "must be a real date and time"}
		}
		return json.RawMessage(strconv.FormatInt(n, 10)), nil
	case "int":
		if s == "" {
			return null, nil
		}
		n, err := strconv.Atoi(s)
		lo, hi, rule := 0, maxLength, "must be a whole number of minutes, 0 to 10080"
		switch f.Name {
		case "satisfactionRating":
			lo, hi, rule = 1, 5, "must be 1 to 5"
		case "orgasmCountSelf":
			lo, hi, rule = 0, maxCount, "must be a whole number, 0 to 99"
		}
		if err != nil || n < lo || n > hi {
			return nil, &BadField{name, rule}
		}
		return json.RawMessage(strconv.Itoa(n)), nil
	case "enum":
		if s == "" {
			return null, nil
		}
		list := Moods
		if f.Name == "initiator" {
			list = Initiators
		}
		if !in(list, s) {
			return nil, &BadField{name, "is not one of the choices"}
		}
		return quote(s), nil
	case "tokens", "places":
		list, empty := Protections, true
		if f.Kind == "places" {
			list, empty = Places, false
		}
		keep, err := tokens(name, s, func(t string) bool { return in(list, t) })
		if err != nil {
			return nil, err
		}
		if len(keep) == 0 && !empty {
			return null, nil
		}
		return quote(strings.Join(keep, ",")), nil
	case "bool":
		return nil, &BadField{name, "cannot be edited"}
	default: // text
		if s == "" {
			return null, nil
		}
		if !utf8.ValidString(input) || len(input) > f.Max || hasControl(strings.TrimRight(input, " \t\r\n")) {
			return nil, &BadField{name, "is too long or not text"}
		}
		if f.Max == maxName { // a name has no leading or trailing blanks
			return quote(s), nil
		}
		return quote(strings.TrimRight(input, " \t\r\n")), nil
	}
}

// hasControl reports a control character other than a line break or a tab.
func hasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) && r != '\n' && r != '\t' && r != '\r' {
			return true
		}
	}
	return false
}

// tokens splits a comma list, drops blanks and repeats, and checks each token.
func tokens(name, s string, ok func(string) bool) ([]string, error) {
	var keep []string
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t == "" {
			continue
		}
		if !ok(t) {
			return nil, &BadField{name, "is not one of the choices"}
		}
		if !in(keep, t) {
			keep = append(keep, t)
		}
	}
	if len(keep) > maxTokens {
		return nil, &BadField{name, "has too many choices"}
	}
	return keep, nil
}

func quote(s string) json.RawMessage {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return bytes.TrimRight(b.Bytes(), "\n")
}

// same compares two raw JSON values by meaning, so 5 and 5.0 or a reordered spelling do not count
// as a change.
func same(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return string(a) == string(b)
	}
	ab, _ := json.Marshal(x)
	bb, _ := json.Marshal(y)
	return string(ab) == string(bb)
}
