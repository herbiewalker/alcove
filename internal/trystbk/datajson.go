package trystbk

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrDataJSONMalformed means data.json is not the expected shape.
var ErrDataJSONMalformed = errors.New("trystbk: data.json malformed or unexpected shape")

// parseDataJSON reads { "schemaVersion": N, "tables": { "<t>": [ {col: value}, ... ] } }
// into b, preserving table order, column order and every value's raw JSON. It streams
// with Decoder.Token so memory is bounded by the data, not by decoder overhead.
func parseDataJSON(r io.Reader, b *Backup) error {
	dec := json.NewDecoder(io.LimitReader(r, lim.dataJSON+1))
	dec.UseNumber()

	if err := expectDelim(dec, '{'); err != nil {
		return err
	}
	sawSchema, sawTables := false, false
	totalRows := 0
	for dec.More() {
		key, err := readObjectKey(dec)
		if err != nil {
			return err
		}
		switch key {
		case "schemaVersion":
			if sawSchema {
				return fmt.Errorf("duplicate schemaVersion")
			}
			t, err := dec.Token()
			if err != nil {
				return err
			}
			num, ok := t.(json.Number)
			if !ok {
				return fmt.Errorf("schemaVersion is not a number: %T", t)
			}
			v, err := num.Int64()
			if err != nil {
				return fmt.Errorf("schemaVersion %q: %v", num, err)
			}
			b.SchemaVersion = int(v)
			sawSchema = true

		case "tables":
			if sawTables {
				return fmt.Errorf("duplicate tables object")
			}
			if err := parseTables(dec, b, &totalRows); err != nil {
				return err
			}
			sawTables = true

		default:
			// A future Tryst may add top-level keys. Keep them raw so a write can
			// put them back.
			var raw json.RawMessage
			if err := dec.Decode(&raw); err != nil {
				return err
			}
			b.Extra = append(b.Extra, ExtraKey{Key: key, Value: raw})
		}
	}
	if _, err := dec.Token(); err != nil { // closing '}'
		return err
	}
	if !sawSchema || !sawTables {
		return fmt.Errorf("missing schemaVersion or tables")
	}
	return nil
}

func parseTables(dec *json.Decoder, b *Backup, totalRows *int) error {
	if err := expectDelim(dec, '{'); err != nil {
		return err
	}
	seen := map[string]bool{}
	for dec.More() {
		name, err := readObjectKey(dec)
		if err != nil {
			return err
		}
		// Names starting with "__" are reserved for Alcove's own bookkeeping.
		if strings.HasPrefix(name, "__") {
			return fmt.Errorf("unexpected reserved-shape table name %q", name)
		}
		if seen[name] {
			return fmt.Errorf("duplicate table %q", name)
		}
		seen[name] = true

		rows, err := parseRows(dec, totalRows)
		if err != nil {
			return fmt.Errorf("table %q: %w", name, err)
		}
		b.Tables = append(b.Tables, Table{Name: name, Rows: rows})
	}
	_, err := dec.Token() // closing '}'
	return err
}

func parseRows(dec *json.Decoder, totalRows *int) ([]Row, error) {
	if err := expectDelim(dec, '['); err != nil {
		return nil, err
	}
	var rows []Row
	for dec.More() {
		*totalRows++
		if *totalRows > lim.rows {
			return nil, fmt.Errorf("more than %d rows", lim.rows)
		}
		if err := expectDelim(dec, '{'); err != nil {
			return nil, fmt.Errorf("row is not an object: %w", err)
		}
		row := Row{Values: map[string]json.RawMessage{}}
		for dec.More() {
			col, err := readObjectKey(dec)
			if err != nil {
				return nil, err
			}
			if _, dup := row.Values[col]; dup {
				return nil, fmt.Errorf("duplicate column %q in a row", col)
			}
			var raw json.RawMessage
			if err := dec.Decode(&raw); err != nil {
				return nil, err
			}
			row.Keys = append(row.Keys, col)
			row.Values[col] = raw
		}
		if _, err := dec.Token(); err != nil { // closing '}'
			return nil, err
		}
		rows = append(rows, row)
	}
	if _, err := dec.Token(); err != nil { // closing ']'
		return nil, err
	}
	return rows, nil
}

func readObjectKey(dec *json.Decoder) (string, error) {
	t, err := dec.Token()
	if err != nil {
		return "", err
	}
	s, ok := t.(string)
	if !ok {
		return "", fmt.Errorf("expected string key, got %T", t)
	}
	return s, nil
}

func expectDelim(dec *json.Decoder, want rune) error {
	t, err := dec.Token()
	if err != nil {
		return err
	}
	d, ok := t.(json.Delim)
	if !ok || rune(d) != want {
		return fmt.Errorf("expected %q, got %v", want, t)
	}
	return nil
}
