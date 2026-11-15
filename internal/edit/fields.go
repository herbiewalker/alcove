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

// Field is one editable column of an entry (D-19).
type Field struct {
	Name  string
	Label string
	Kind  string // time, int, enum, tokens, text
}

// Fields are the encounter columns a person may change, in display order. Everything else in a
// row, and every other table, passes through untouched.
var Fields = []Field{
	{"startAt", "When", "time"},
	{"durationMin", "Length (minutes)", "int"},
	{"satisfactionRating", "Rating", "int"},
	{"mood", "Mood", "enum"},
	{"initiator", "Who started it", "enum"},
	{"protectionUsed", "Protection", "tokens"},
	{"orgasmCountSelf", "Orgasms (yours)", "int"},
	{"note", "Note", "text"},
}

// Moods, Initiators and Protections are Tryst's own values. Tryst rejects any other spelling, so
// Alcove offers nothing else. Copied from Tryst's Enums.kt; a new Tryst value is added here.
var (
	Moods = []string{"AMAZING", "EUPHORIC", "PASSIONATE", "HORNY", "SENSUAL", "WILD", "ADVENTUROUS", "KINKY", "NAUGHTY", "PLAYFUL",
		"ROMANTIC", "INTIMATE", "TENDER", "CONNECTED", "LOVED", "DESIRED", "CONFIDENT", "SAFE", "VULNERABLE", "EMOTIONAL", "CURIOUS",
		"SPONTANEOUS", "TIPSY", "SATISFIED", "RELAXED", "SLEEPY", "GOOD", "NEUTRAL", "BORED", "AWKWARD", "MEH", "FRUSTRATED",
		"DISAPPOINTED", "BAD"}
	Initiators  = []string{"ME", "PARTNER", "MUTUAL"}
	Protections = []string{"NONE", "CONDOM", "INTERNAL_CONDOM", "DENTAL_DAM", "PILL", "IUD", "IMPLANT", "PATCH", "VAGINAL_RING",
		"INJECTION", "DIAPHRAGM", "CERVICAL_CAP", "SPERMICIDE", "SPONGE", "FERTILITY_AWARENESS", "WITHDRAWAL", "VASECTOMY",
		"TUBAL_LIGATION", "PREP", "PEP", "DOXY_PEP", "EMERGENCY_CONTRACEPTION", "OTHER"}
)

const (
	maxNote   = 20000
	minStart  = int64(0)             // 1970
	maxStart  = int64(4102444800000) // 2100-01-01
	maxLength = 7 * 24 * 60          // minutes
	maxCount  = 99
)

func lookup(name string) (Field, bool) {
	for _, f := range Fields {
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

// Clean turns what a person typed into the raw JSON Tryst stores for that column. Empty means
// "not set" (null; an empty string for protection, as Tryst writes it), except the start time.
func Clean(name, input string) (json.RawMessage, error) {
	f, ok := lookup(name)
	if !ok {
		return nil, &BadField{name, "cannot be edited"}
	}
	s := strings.TrimSpace(input)
	null := json.RawMessage("null")
	switch f.Name {
	case "startAt":
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n < minStart || n >= maxStart {
			return nil, &BadField{name, "must be a real date and time"}
		}
		return json.RawMessage(strconv.FormatInt(n, 10)), nil
	case "durationMin", "satisfactionRating", "orgasmCountSelf":
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
	case "mood", "initiator":
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
	case "protectionUsed":
		var keep []string
		for _, t := range strings.Split(s, ",") {
			if t = strings.TrimSpace(t); t == "" {
				continue
			}
			if !in(Protections, t) {
				return nil, &BadField{name, "is not one of the choices"}
			}
			if !in(keep, t) {
				keep = append(keep, t)
			}
		}
		return quote(strings.Join(keep, ",")), nil
	default: // note
		if s == "" {
			return null, nil
		}
		if !utf8.ValidString(input) || len(input) > maxNote {
			return nil, &BadField{name, "is too long or not text"}
		}
		return quote(strings.TrimRight(input, " \t\r\n")), nil
	}
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
