package photo

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
)

// A phone stores a picture the way the sensor saw it and records how to turn it in one EXIF tag
// (Orientation, 1 to 8). Cleaning a photo removes the whole EXIF block (D-39), which would leave
// every portrait photo lying on its side for the people it is shared with. So the one number is
// kept, in an EXIF block of its own that holds nothing else (D-44).

// exifHeader starts the payload of an EXIF application segment.
var exifHeader = []byte("Exif\x00\x00")

// orientationOf reads the Orientation tag from the payload of an EXIF segment. It returns 1
// ("as stored") when the tag is missing, malformed or out of range.
func orientationOf(payload []byte) int {
	if !bytes.HasPrefix(payload, exifHeader) {
		return 1
	}
	t := payload[len(exifHeader):]
	if len(t) < 8 {
		return 1
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	if bo.Uint16(t[2:4]) != 42 {
		return 1
	}
	off := int(bo.Uint32(t[4:8]))
	if off < 8 || off > len(t)-2 {
		return 1
	}
	n := int(bo.Uint16(t[off : off+2]))
	for i := 0; i < n; i++ {
		e := off + 2 + i*12
		if e+12 > len(t) {
			return 1
		}
		if bo.Uint16(t[e:e+2]) == 0x0112 && bo.Uint16(t[e+2:e+4]) == 3 && bo.Uint32(t[e+4:e+8]) == 1 {
			if v := int(bo.Uint16(t[e+8 : e+10])); v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}

// orientationSegment is a complete APP1 segment holding only the Orientation tag.
func orientationSegment(o int) []byte {
	var b bytes.Buffer
	b.Write(exifHeader)
	b.WriteString("MM")
	b.Write([]byte{0x00, 0x2A, 0x00, 0x00, 0x00, 0x08}) // magic, IFD0 at offset 8
	b.Write([]byte{0x00, 0x01})                         // one entry
	b.Write([]byte{0x01, 0x12, 0x00, 0x03, 0x00, 0x00, 0x00, 0x01, byte(o >> 8), byte(o), 0x00, 0x00})
	b.Write([]byte{0x00, 0x00, 0x00, 0x00}) // no next IFD
	out := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(out[2:], uint16(2+b.Len()))
	return append(out, b.Bytes()...)
}

// Orientation reads how a JPEG should be turned (1 to 8, 1 meaning as stored) by looking at its
// segments up to the first scan. Anything unreadable gives 1; PNG gives 1.
func Orientation(r io.Reader) int {
	br := bufio.NewReaderSize(r, 64<<10)
	head, _ := br.Peek(3)
	if Sniff(head) != JPEG {
		return 1
	}
	_, _ = br.Discard(2)
	for i := 0; i < 64; i++ { // a few application segments at most before the scan
		b, err := br.ReadByte()
		if err != nil || b != 0xFF {
			return 1
		}
		m, err := br.ReadByte()
		for err == nil && m == 0xFF {
			m, err = br.ReadByte()
		}
		if err != nil || m == 0xDA || m == 0xD9 {
			return 1
		}
		if m == 0x01 || (m >= 0xD0 && m <= 0xD8) {
			continue
		}
		lb := make([]byte, 2)
		if _, err := io.ReadFull(br, lb); err != nil {
			return 1
		}
		l := int(binary.BigEndian.Uint16(lb))
		if l < 2 {
			return 1
		}
		if m != 0xE1 || l > 65535 {
			if _, err := br.Discard(l - 2); err != nil {
				return 1
			}
			continue
		}
		payload := make([]byte, l-2)
		if _, err := io.ReadFull(br, payload); err != nil {
			return 1
		}
		if o := orientationOf(payload); o != 1 || bytes.HasPrefix(payload, exifHeader) {
			return o
		}
	}
	return 1
}
