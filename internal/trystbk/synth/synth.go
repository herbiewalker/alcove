// Package synth builds SYNTHETIC Tryst backups for tests.
//
// Hard constraints 4 and 10: no real data ever enters this repo. Everything here is
// generated from a seed, uses neutral placeholder text, and mirrors the table and column
// layout of Tryst's Room schema v19 (app/schemas/.../19.json) so that a generated backup
// is shaped like a real one and can be restored in Tryst on an emulator.
//
// The output is deterministic for a given Options value except for the container's AEAD
// nonces, so tests are reproducible.
package synth

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math/rand/v2"
	"time"

	"github.com/herbiewalker/alcove/internal/trystbk"
)

// Options controls what is generated. Zero values pick small, sensible defaults.
type Options struct {
	Seed       uint64
	Schema     int // Tryst schemaVersion to stamp; default trystbk.LatestKnownSchema
	Encounters int // default 12
	Partners   int // default 2
	Photos     int // default 0; each adds a media row, a media/<id> entry and a tiny JPEG
	Password   string
	Iterations int // PBKDF2 count; default trystbk.MinIterations to keep tests fast
	Version    int // container version byte; default trystbk.FormatVersion2
	// IncludeSettings adds a settings.json entry (container v2).
	IncludeSettings bool
}

// Result is a generated backup and what was put in it.
type Result struct {
	File     []byte // the TRYSTBK1 container
	ZIP      []byte // the plaintext ZIP inside it (tests may corrupt and re-seal it)
	DataJSON []byte // the generated data.json
	Password string
	Counts   map[string]int
	MediaIDs []string
}

func (o *Options) defaults() {
	if o.Schema == 0 {
		o.Schema = trystbk.LatestKnownSchema
	}
	if o.Encounters == 0 {
		o.Encounters = 12
	}
	if o.Partners == 0 {
		o.Partners = 2
	}
	if o.Password == "" {
		o.Password = "synthetic-test-password"
	}
	if o.Iterations == 0 {
		o.Iterations = trystbk.MinIterations
	}
	if o.Version == 0 {
		o.Version = trystbk.FormatVersion2
	}
}

// Build generates a backup.
func Build(o Options) (*Result, error) {
	o.defaults()
	g := &gen{rng: rand.New(rand.NewPCG(o.Seed, o.Seed^0x9e3779b97f4a7c15)), o: o, counts: map[string]int{}}
	data, media, err := g.dataJSON()
	if err != nil {
		return nil, err
	}
	zipBytes, err := buildZIP(data, media, o.IncludeSettings && o.Version >= trystbk.FormatVersion2)
	if err != nil {
		return nil, err
	}
	var salt [trystbk.SaltSize]byte
	for i := range salt {
		salt[i] = byte(g.rng.UintN(256))
	}
	file, err := trystbk.Seal(o.Password, zipBytes, trystbk.SealOptions{
		Version: o.Version, Iterations: o.Iterations, Salt: &salt,
	})
	if err != nil {
		return nil, err
	}
	return &Result{File: file, ZIP: zipBytes, DataJSON: data, Password: o.Password, Counts: g.counts, MediaIDs: g.mediaIDs}, nil
}

// BuildZIP exposes the plaintext ZIP assembly for tests that need to craft hostile or
// unusual archives around a generated data.json.
func BuildZIP(dataJSON []byte, media map[string][]byte, settings bool) ([]byte, error) {
	return buildZIP(dataJSON, mediaList(media), settings)
}

type mediaBlob struct {
	id   string
	data []byte
}

func mediaList(m map[string][]byte) []mediaBlob {
	var out []mediaBlob
	for id, d := range m {
		out = append(out, mediaBlob{id: id, data: d})
	}
	return out
}

// ZIP rules learned from restoring in Tryst (S1): Tryst reads the archive with a streaming
// java.util.zip.ZipInputStream, which cannot read a STORED entry whose size and CRC come after
// the data (the usual output of a streaming writer). A backup that used STORED media restored
// every table but silently kept only the first photo. So every entry is DEFLATED, data.json comes
// first, as Tryst writes them.
func buildZIP(dataJSON []byte, media []mediaBlob, settings bool) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name string, body []byte, method uint16) error {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: method})
		if err != nil {
			return err
		}
		_, err = w.Write(body)
		return err
	}
	if err := add("data.json", dataJSON, zip.Deflate); err != nil {
		return nil, err
	}
	if settings {
		s := `{"tryst_appearance":{"theme":{"t":"string","v":"dark"}}}`
		if err := add("settings.json", []byte(s), zip.Deflate); err != nil {
			return nil, err
		}
	}
	for _, m := range media {
		if err := add("media/"+m.id, m.data, zip.Deflate); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// --- generation ---------------------------------------------------------------------

type gen struct {
	rng      *rand.Rand
	o        Options
	counts   map[string]int
	mediaIDs []string
}

type kv struct {
	k string
	v any
}

func (g *gen) id() string {
	var b [16]byte
	for i := range b {
		b[i] = byte(g.rng.UintN(256))
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func (g *gen) pick(opts ...string) string { return opts[g.rng.IntN(len(opts))] }

// nullable returns nil about one time in four.
func (g *gen) nullable(v any) any {
	if g.rng.IntN(4) == 0 {
		return nil
	}
	return v
}

func (g *gen) dataJSON() ([]byte, []mediaBlob, error) {
	tables := map[string][][]kv{}
	b2i := func(b bool) int {
		if b {
			return 1
		}
		return 0
	}
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC).UnixMilli()
	start := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()

	// Partners.
	var partnerIDs []string
	for i := 0; i < g.o.Partners; i++ {
		id := g.id()
		partnerIDs = append(partnerIDs, id)
		anon := g.rng.IntN(5) == 0
		var name any = fmt.Sprintf("Synthetic Partner %d", i+1)
		if anon {
			name = nil
		}
		tables["partners"] = append(tables["partners"], []kv{
			{"id", id}, {"displayName", name}, {"isAnonymous", b2i(anon)}, {"color", nil}, {"note", nil},
			{"sex", nil}, {"gender", nil}, {"relationshipType", g.pick("PARTNER", "SPOUSE", "FRIEND")},
			{"photoMediaId", nil}, {"birthDate", nil}, {"ethnicity", nil}, {"height", nil}, {"bodyType", nil},
			{"location", nil}, {"orientation", nil}, {"archivedAt", nil}, {"createdAt", now}, {"updatedAt", now},
		})
	}
	tables["profile"] = [][]kv{{
		{"id", "self"}, {"displayName", "Synthetic Self"}, {"photoMediaId", nil}, {"sex", nil}, {"gender", nil},
		{"birthDate", nil}, {"ethnicity", nil}, {"height", nil}, {"bodyType", nil}, {"location", nil},
		{"orientation", nil}, {"note", nil}, {"updatedAt", now},
	}}

	// Catalog tables: user-defined labels, ids are plain uuids and referenced as custom:<id>.
	catalog := func(table string, n int, builtIn bool) []string {
		var ids []string
		for i := 0; i < n; i++ {
			id := g.id()
			ids = append(ids, id)
			row := []kv{{"id", id}, {"label", fmt.Sprintf("Synthetic %s %d", table, i+1)}}
			switch table {
			case "locations":
				row = append(row, kv{"createdAt", now})
			case "tags", "orientations":
			default:
				row = append(row, kv{"isBuiltIn", b2i(builtIn)})
			}
			tables[table] = append(tables[table], row)
		}
		return ids
	}
	catalog("locations", 2, false)
	catalog("tags", 2, false)
	positions := catalog("positions", 3, false)
	acts := catalog("acts", 3, false)
	kinks := catalog("kinks", 2, false)
	toys := catalog("toys", 2, false)
	occasions := catalog("occasions", 2, false)
	catalog("ejaculation_locations", 2, false)
	catalog("orientations", 2, false)

	custom := func(ids []string) any {
		n := g.rng.IntN(3)
		if n == 0 {
			return nil
		}
		s := ""
		for i := 0; i < n; i++ {
			if i > 0 {
				s += ","
			}
			s += "custom:" + ids[g.rng.IntN(len(ids))]
		}
		return s
	}

	// Encounters and their links.
	var encIDs []string
	for i := 0; i < g.o.Encounters; i++ {
		id := g.id()
		encIDs = append(encIDs, id)
		at := start + g.rng.Int64N(now-start)
		pid := partnerIDs[g.rng.IntN(len(partnerIDs))]
		tables["encounters"] = append(tables["encounters"], []kv{
			{"id", id}, {"startAt", at}, {"durationMin", g.nullable(5 + g.rng.IntN(120))},
			{"note", g.nullable(fmt.Sprintf("Synthetic entry %d", i+1))},
			{"satisfactionRating", g.nullable(1 + g.rng.IntN(5))}, {"orgasm", nil},
			{"mood", g.nullable(g.pick("HAPPY", "RELAXED", "PLAYFUL"))},
			{"initiator", g.nullable(g.pick("ME", "PARTNER", "MUTUAL"))},
			{"protectionUsed", g.pick("", "CONDOM")},
			{"orgasmCountSelf", g.nullable(g.rng.IntN(3))}, {"orgasmCountPartner", nil},
			{"ejaculationLocations", nil},
			{"practicesPerformed", custom(acts)}, {"practicesReceived", custom(acts)},
			{"positions", custom(positions)}, {"kinks", custom(kinks)}, {"contexts", g.nullable("HOME")},
			{"toys", custom(toys)}, {"occasions", custom(occasions)},
			{"partnerOrgasms", g.nullable(pid + "=1")}, {"locationId", nil},
			{"createdAt", at}, {"updatedAt", at},
		})
		tables["encounter_partner"] = append(tables["encounter_partner"], []kv{{"encounterId", id}, {"partnerId", pid}})
	}
	tables["encounter_position"] = nil
	tables["encounter_tag"] = nil

	// Photos.
	var blobs []mediaBlob
	for i := 0; i < g.o.Photos && len(encIDs) > 0; i++ {
		mid := g.id()
		g.mediaIDs = append(g.mediaIDs, mid)
		blobs = append(blobs, mediaBlob{id: mid, data: tinyJPEG(g.rng.UintN(256))})
		var lat, lon any
		if g.rng.IntN(2) == 0 {
			lat, lon = 10.0+float64(g.rng.IntN(100))/100, 20.0+float64(g.rng.IntN(100))/100 // fake coordinates
		}
		tables["media"] = append(tables["media"], []kv{
			{"id", mid}, {"encounterId", encIDs[g.rng.IntN(len(encIDs))]},
			{"encFilePath", "/synthetic/" + mid}, {"mimeType", "image/jpeg"}, {"createdAt", now},
			{"favorite", b2i(g.rng.IntN(4) == 0)}, {"caption", g.nullable("Synthetic caption")},
			{"capturedAt", now}, {"capturedOffsetMin", 0}, {"latitude", lat}, {"longitude", lon},
			{"altitudeM", nil}, {"camera", nil}, {"lens", nil}, {"aperture", nil}, {"shutterSeconds", nil},
			{"iso", nil}, {"focalLengthMm", nil}, {"originalName", fmt.Sprintf("synthetic-%d.jpg", i+1)},
			{"metaScanned", 1},
		})
	}
	tables["person_photo"] = nil

	// Assemble data.json in Tryst's restore order, preserving column order.
	var buf bytes.Buffer
	fmt.Fprintf(&buf, `{"schemaVersion":%d,"tables":{`, g.o.Schema)
	for ti, name := range trystbk.RestoreTables {
		if ti > 0 {
			buf.WriteByte(',')
		}
		enc, _ := json.Marshal(name)
		buf.Write(enc)
		buf.WriteString(":[")
		rows := tables[name]
		g.counts[name] = len(rows)
		for ri, row := range rows {
			if ri > 0 {
				buf.WriteByte(',')
			}
			buf.WriteByte('{')
			for ci, c := range row {
				if ci > 0 {
					buf.WriteByte(',')
				}
				k, _ := json.Marshal(c.k)
				v, err := json.Marshal(c.v)
				if err != nil {
					return nil, nil, fmt.Errorf("synth: %s.%s: %w", name, c.k, err)
				}
				buf.Write(k)
				buf.WriteByte(':')
				buf.Write(v)
			}
			buf.WriteByte('}')
		}
		buf.WriteByte(']')
	}
	buf.WriteString("}}")
	g.counts["__media__"] = len(blobs)
	return buf.Bytes(), blobs, nil
}

// tinyJPEG returns a small valid JPEG so a restored synthetic photo can actually be decoded.
func tinyJPEG(shade uint) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			img.Set(x, y, color.RGBA{R: uint8(shade), G: uint8(x * 16), B: uint8(y * 16), A: 255})
		}
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 60})
	return buf.Bytes()
}
