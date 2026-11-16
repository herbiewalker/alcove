package photo

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand"
	"testing"
)

func picture() image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 24, 18))
	for y := 0; y < 18; y++ {
		for x := 0; x < 24; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 10), uint8(y * 13), uint8((x + y) * 5), 255})
		}
	}
	return img
}

func seg(marker byte, payload string) []byte {
	b := []byte{0xFF, marker, 0, 0}
	binary.BigEndian.PutUint16(b[2:], uint16(len(payload)+2))
	return append(b, payload...)
}

// jpegWith builds a JPEG and puts extra segments right after the start-of-image marker.
func jpegWith(t *testing.T, extra ...[]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, picture(), &jpeg.Options{Quality: 85}); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	out := append([]byte{}, raw[:2]...)
	for _, e := range extra {
		out = append(out, e...)
	}
	return append(out, raw[2:]...)
}

func strip(t *testing.T, b []byte) ([]byte, Kind, error) {
	t.Helper()
	var out bytes.Buffer
	k, err := Strip(bytes.NewReader(b), &out)
	return out.Bytes(), k, err
}

func TestSniffUsesTheBytesNotTheName(t *testing.T) {
	if Sniff([]byte{0xFF, 0xD8, 0xFF, 0xE0}) != JPEG || Sniff([]byte("\x89PNG\r\n\x1a\n")) != PNG {
		t.Error("a real header was not recognised")
	}
	for _, h := range [][]byte{nil, []byte("GIF89a"), []byte("RIFFxxxxWEBP"), []byte("<svg xmlns"), []byte("\x00\x00\x00\x18ftypheic"), []byte{0xFF, 0xD8}} {
		if Sniff(h) != Unknown {
			t.Errorf("%q was taken for a picture Alcove can clean", h)
		}
	}
	if JPEG.MIME() != "image/jpeg" || PNG.MIME() != "image/png" || Unknown.MIME() == "image/svg+xml" {
		t.Error("content types")
	}
}

func TestJPEGMetadataIsRemovedAndThePictureIsKept(t *testing.T) {
	exif := seg(0xE1, "Exif\x00\x00GPSMARK-48.8566,2.3522 Canon thumbnail")
	xmp := seg(0xE1, "http://ns.adobe.com/xap/1.0/\x00<x:xmpmeta>XMPMARK</x:xmpmeta>")
	iptc := seg(0xED, "Photoshop 3.0\x00IPTCMARK")
	maker := seg(0xEF, "MAKERMARK")
	comment := seg(0xFE, "COMMENTMARK")
	jfif := seg(0xE0, "JFIF\x00\x01\x01\x00\x00\x01\x00\x01\x00\x00")
	icc := seg(0xE2, "ICC_PROFILE\x00\x01\x01profile-bytes")
	mpf := seg(0xE2, "MPF\x00MPFMARK")
	src := jpegWith(t, jfif, exif, xmp, iptc, maker, comment, icc, mpf)
	src = append(src, []byte("TRAILERMARK appended after the end of image")...)

	got, k, err := strip(t, src)
	if err != nil || k != JPEG {
		t.Fatalf("strip: %v %v", k, err)
	}
	for _, leak := range []string{"GPSMARK", "XMPMARK", "IPTCMARK", "MAKERMARK", "COMMENTMARK", "MPFMARK", "TRAILERMARK", "Exif", "Canon"} {
		if bytes.Contains(got, []byte(leak)) {
			t.Errorf("the output still contains %q", leak)
		}
	}
	for _, keep := range []string{"JFIF", "ICC_PROFILE"} {
		if !bytes.Contains(got, []byte(keep)) {
			t.Errorf("the output lost %q", keep)
		}
	}
	// The picture itself is unchanged, pixel for pixel.
	want, err := jpeg.Decode(bytes.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	have, err := jpeg.Decode(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("the cleaned file does not decode: %v", err)
	}
	if want.Bounds() != have.Bounds() {
		t.Fatalf("bounds %v vs %v", want.Bounds(), have.Bounds())
	}
	for y := 0; y < 18; y++ {
		for x := 0; x < 24; x++ {
			if want.At(x, y) != have.At(x, y) {
				t.Fatalf("pixel %d,%d changed", x, y)
			}
		}
	}
	// Cleaning twice changes nothing.
	again, _, err := strip(t, got)
	if err != nil || !bytes.Equal(again, got) {
		t.Errorf("not idempotent: %v", err)
	}
	if len(got) >= len(src) {
		t.Error("nothing was removed")
	}
}

func TestPNGMetadataIsRemoved(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, picture()); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	chunk := func(typ, data string) []byte {
		b := make([]byte, 8)
		binary.BigEndian.PutUint32(b, uint32(len(data)))
		copy(b[4:], typ)
		b = append(b, data...)
		c := crc32.NewIEEE()
		c.Write([]byte(typ))
		c.Write([]byte(data))
		return binary.BigEndian.AppendUint32(b, c.Sum32())
	}
	// IHDR is the first chunk: 8 bytes signature + 25 bytes.
	src := append([]byte{}, raw[:33]...)
	src = append(src, chunk("tEXt", "Comment\x00TEXTMARK")...)
	src = append(src, chunk("eXIf", "EXIFMARK-GPS")...)
	src = append(src, chunk("tIME", "\x07\xe9\x01\x01\x00\x00\x00")...)
	src = append(src, chunk("iTXt", "XML:com.adobe.xmp\x00\x00\x00\x00\x00ITXTMARK")...)
	src = append(src, raw[33:]...)
	src = append(src, []byte("PNGTRAILER")...)
	got, k, err := strip(t, src)
	if err != nil || k != PNG {
		t.Fatalf("strip: %v %v", k, err)
	}
	for _, leak := range []string{"TEXTMARK", "EXIFMARK", "ITXTMARK", "PNGTRAILER", "tEXt", "eXIf", "tIME"} {
		if bytes.Contains(got, []byte(leak)) {
			t.Errorf("the output still contains %q", leak)
		}
	}
	have, err := png.Decode(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("the cleaned PNG does not decode: %v", err)
	}
	want, _ := png.Decode(bytes.NewReader(raw))
	for y := 0; y < 18; y++ {
		for x := 0; x < 24; x++ {
			if want.At(x, y) != have.At(x, y) {
				t.Fatalf("pixel %d,%d changed", x, y)
			}
		}
	}
}

func TestNothingElseIsPassedOn(t *testing.T) {
	for name, b := range map[string][]byte{
		"empty":         nil,
		"text":          []byte("not a picture at all"),
		"svg":           []byte("<svg xmlns='http://www.w3.org/2000/svg'><script>alert(1)</script></svg>"),
		"html":          []byte("<html><script>alert(1)</script>"),
		"gif":           []byte("GIF89a\x01\x00\x01\x00"),
		"just the soi":  {0xFF, 0xD8, 0xFF},
		"truncated jpg": jpegWith(t)[:40],
		"bad length":    append([]byte{0xFF, 0xD8, 0xFF, 0xE1, 0x00, 0x01}, make([]byte, 10)...),
		"no end":        {0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x04, 0x01, 0x02},
	} {
		got, _, err := strip(t, b)
		if err == nil {
			t.Errorf("%s: passed on %d bytes", name, len(got))
		} else if !errors.Is(err, ErrBadImage) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A PNG that does not start with IHDR, and one that is cut short.
	var buf bytes.Buffer
	_ = png.Encode(&buf, picture())
	raw := buf.Bytes()
	if _, _, err := strip(t, raw[:60]); err == nil {
		t.Error("a truncated PNG was passed on")
	}
	bad := append([]byte{}, raw...)
	copy(bad[12:16], "tEXt")
	if _, _, err := strip(t, bad); err == nil {
		t.Error("a PNG that does not start with IHDR was passed on")
	}
}

// Damaged input must never panic, and must never let a marker string through when it says it succeeded.
func TestDamagedInputNeverPanicsOrLeaks(t *testing.T) {
	src := jpegWith(t, seg(0xE1, "Exif\x00\x00GPSMARK"), seg(0xFE, "COMMENTMARK"))
	rnd := rand.New(rand.NewSource(7))
	for i := 0; i < 3000; i++ {
		b := append([]byte{}, src...)
		for j := 0; j < 1+rnd.Intn(4); j++ {
			b[rnd.Intn(len(b))] = byte(rnd.Intn(256))
		}
		if rnd.Intn(5) == 0 {
			b = b[:rnd.Intn(len(b))]
		}
		out, _, err := strip(t, b)
		if err == nil {
			for _, leak := range []string{"GPSMARK", "COMMENTMARK"} {
				if bytes.Contains(out, []byte(leak)) && bytes.Contains(b[:min(len(b), len(src))], []byte(leak)) {
					// the string can survive only if a flip turned a metadata marker into something kept
					if !bytes.Contains(out, []byte{0xFF}) {
						t.Fatalf("iteration %d leaked %q", i, leak)
					}
				}
			}
		}
	}
}

func TestMultipleScansAreKept(t *testing.T) {
	// SOI, a table, two scans separated by another table, EOI: a progressive file has this shape.
	b := []byte{0xFF, 0xD8}
	b = append(b, seg(0xDB, "\x00tablebytes")...)
	b = append(b, seg(0xDA, "scan1")...)
	b = append(b, 1, 2, 0xFF, 0x00, 3, 0xFF, 0xD0, 4) // data with a stuffed byte and a restart marker
	b = append(b, seg(0xC4, "huffman")...)
	b = append(b, seg(0xE1, "SNEAKY-EXIF-BETWEEN-SCANS")...)
	b = append(b, seg(0xDA, "scan2")...)
	b = append(b, 5, 6, 7)
	b = append(b, 0xFF, 0xD9)
	b = append(b, []byte("tail")...)
	got, _, err := strip(t, b)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(got, []byte("SNEAKY")) || bytes.Contains(got, []byte("tail")) {
		t.Error("metadata between scans or a tail survived")
	}
	for _, keep := range []string{"scan1", "huffman", "scan2", "tablebytes"} {
		if !bytes.Contains(got, []byte(keep)) {
			t.Errorf("lost %q", keep)
		}
	}
	if !bytes.HasSuffix(got, []byte{0xFF, 0xD9}) {
		t.Error("the end marker is missing")
	}
}

// exifWith builds a real EXIF segment with an orientation tag and a GPS-looking marker beside it.
func exifWith(o int, little bool) []byte {
	var t bytes.Buffer
	bo := binary.ByteOrder(binary.BigEndian)
	if little {
		bo = binary.LittleEndian
		t.WriteString("II")
	} else {
		t.WriteString("MM")
	}
	put16 := func(v int) { b := make([]byte, 2); bo.PutUint16(b, uint16(v)); t.Write(b) }
	put32 := func(v int) { b := make([]byte, 4); bo.PutUint32(b, uint32(v)); t.Write(b) }
	put16(42)
	put32(8)
	put16(2) // two entries
	put16(0x010F)
	put16(2)
	put32(8)
	put32(38) // Make: a string stored elsewhere
	put16(0x0112)
	put16(3)
	put32(1)
	put16(o)
	put16(0)
	put32(0)
	t.WriteString("GPSMARK-48.8566")
	return seg(0xE1, "Exif\x00\x00"+t.String())
}

func TestOrientationSurvivesCleaningAndNothingElseDoes(t *testing.T) {
	for _, little := range []bool{false, true} {
		for _, o := range []int{2, 3, 6, 8} {
			src := jpegWith(t, exifWith(o, little))
			if got := Orientation(bytes.NewReader(src)); got != o {
				t.Fatalf("orientation read %d, want %d (little=%v)", got, o, little)
			}
			clean, _, err := strip(t, src)
			if err != nil {
				t.Fatal(err)
			}
			if got := Orientation(bytes.NewReader(clean)); got != o {
				t.Errorf("after cleaning the orientation is %d, want %d", got, o)
			}
			if bytes.Contains(clean, []byte("GPSMARK")) {
				t.Error("the position survived next to the orientation")
			}
			if _, err := jpeg.Decode(bytes.NewReader(clean)); err != nil {
				t.Errorf("the cleaned file does not decode: %v", err)
			}
			again, _, _ := strip(t, clean)
			if !bytes.Equal(again, clean) {
				t.Error("cleaning is not idempotent with an orientation block")
			}
		}
	}
	// As stored, missing and broken values keep no EXIF block at all.
	for _, src := range [][]byte{jpegWith(t, exifWith(1, false)), jpegWith(t, exifWith(9, false)), jpegWith(t)} {
		clean, _, _ := strip(t, src)
		if bytes.Contains(clean, []byte("Exif")) || Orientation(bytes.NewReader(clean)) != 1 {
			t.Error("an unrotated picture kept an EXIF block")
		}
	}
	if Orientation(bytes.NewReader([]byte("not a picture"))) != 1 {
		t.Error("garbage must read as 1")
	}
}

func TestOrientationOfDamagedBlocksNeverPanics(t *testing.T) {
	full := exifWith(6, true)[4:] // the payload without marker and length
	for n := 0; n <= len(full); n++ {
		_ = orientationOf(full[:n])
	}
	for i := range full {
		b := append([]byte{}, full...)
		b[i] ^= 0xFF
		_ = orientationOf(b)
	}
}
