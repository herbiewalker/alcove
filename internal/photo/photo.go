// Package photo handles photo bytes on their way to other people (D-39, M7).
//
// A photo can carry its own metadata: an EXIF block with the GPS position, the camera, the time,
// an embedded thumbnail, comments. A member who shows photos to a space but keeps the place off
// (the default) must not leak it through the file itself, so everything that is not needed to
// draw the picture is removed before the bytes reach anyone but the owner. Only JPEG and PNG are
// cleaned; anything else is refused for sharing rather than passed on unchecked (fail closed).
package photo

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
)

// Kind is a picture format Alcove can clean.
type Kind int

const (
	Unknown Kind = iota
	JPEG
	PNG
)

// ErrBadImage means the bytes are not a well formed picture of the format they claim.
var ErrBadImage = errors.New("photo: not a well formed picture")

// Sniff decides the format from the first bytes, never from a file name or a label.
func Sniff(head []byte) Kind {
	switch {
	case len(head) >= 3 && head[0] == 0xFF && head[1] == 0xD8 && head[2] == 0xFF:
		return JPEG
	case len(head) >= 8 && bytes.Equal(head[:8], []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}):
		return PNG
	}
	return Unknown
}

// MIME is the content type to serve for a cleaned picture.
func (k Kind) MIME() string {
	switch k {
	case JPEG:
		return "image/jpeg"
	case PNG:
		return "image/png"
	}
	return "application/octet-stream"
}

// Strip copies a picture to w without its metadata. The whole output is built in memory by the
// caller's writer; on any error nothing should be sent.
func Strip(r io.Reader, w io.Writer) (Kind, error) {
	br := bufio.NewReaderSize(r, 64<<10)
	head, err := br.Peek(8)
	if err != nil && len(head) < 3 {
		return Unknown, ErrBadImage
	}
	k := Sniff(head)
	bw := bufio.NewWriterSize(w, 64<<10)
	switch k {
	case JPEG:
		err = stripJPEG(br, bw)
	case PNG:
		err = stripPNG(br, bw)
	default:
		return Unknown, ErrBadImage
	}
	if err != nil {
		return k, err
	}
	return k, bw.Flush()
}

func readN(br *bufio.Reader, n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(br, b); err != nil {
		return nil, ErrBadImage
	}
	return b, nil
}

// keepJPEGSegment says whether an application or comment segment is needed to draw the picture.
func keepJPEGSegment(marker byte, payload []byte) bool {
	switch {
	case marker == 0xE0: // JFIF
		return true
	case marker == 0xE2 && bytes.HasPrefix(payload, []byte("ICC_PROFILE\x00")): // the colour profile
		return true
	case marker == 0xEE && bytes.HasPrefix(payload, []byte("Adobe")): // colour transform flag some CMYK files need
		return true
	case marker >= 0xE1 && marker <= 0xEF: // EXIF, XMP, IPTC, MPF, maker notes
		return false
	case marker == 0xFE: // comment
		return false
	}
	return true
}

func stripJPEG(br *bufio.Reader, bw *bufio.Writer) error {
	if _, err := readN(br, 2); err != nil { // SOI
		return err
	}
	bw.Write([]byte{0xFF, 0xD8})
	var pending byte // a marker already read at the end of a scan
	for {
		m := pending
		pending = 0
		if m == 0 {
			// A marker: 0xFF, any fill bytes, then the marker code.
			b, err := br.ReadByte()
			if err != nil || b != 0xFF {
				return ErrBadImage
			}
			if m, err = br.ReadByte(); err != nil {
				return ErrBadImage
			}
			for m == 0xFF {
				if m, err = br.ReadByte(); err != nil {
					return ErrBadImage
				}
			}
		}
		switch {
		case m == 0xD9: // EOI: the end; anything appended after it (extra images, thumbnails) is dropped
			bw.Write([]byte{0xFF, 0xD9})
			return nil
		case m == 0x00:
			return ErrBadImage
		case m == 0x01 || (m >= 0xD0 && m <= 0xD8): // standalone markers
			bw.Write([]byte{0xFF, m})
			continue
		}
		lb, err := readN(br, 2)
		if err != nil {
			return err
		}
		l := int(binary.BigEndian.Uint16(lb))
		if l < 2 {
			return ErrBadImage
		}
		payload, err := readN(br, l-2)
		if err != nil {
			return err
		}
		if keepJPEGSegment(m, payload) {
			bw.Write([]byte{0xFF, m})
			bw.Write(lb)
			bw.Write(payload)
		}
		if m != 0xDA { // not a scan header: the next thing is another marker
			continue
		}
		// Entropy-coded data follows the scan header: copy it until the next real marker.
	scan:
		for {
			c, err := br.ReadByte()
			if err != nil {
				return ErrBadImage
			}
			if c != 0xFF {
				bw.WriteByte(c)
				continue
			}
			n, err := br.ReadByte()
			if err != nil {
				return ErrBadImage
			}
			for n == 0xFF { // fill bytes before a marker
				if n, err = br.ReadByte(); err != nil {
					return ErrBadImage
				}
			}
			if n == 0x00 || (n >= 0xD0 && n <= 0xD7) { // a stuffed byte or a restart marker is still data
				bw.Write([]byte{0xFF, n})
				continue
			}
			pending = n // a real marker ends the scan
			break scan
		}
	}
}

var keepPNG = map[string]bool{
	"IHDR": true, "PLTE": true, "IDAT": true, "IEND": true, "tRNS": true, "gAMA": true, "cHRM": true, "sRGB": true,
	"iCCP": true, "sBIT": true, "bKGD": true, "pHYs": true, "hIST": true, "sPLT": true, "acTL": true, "fcTL": true, "fdAT": true,
}

func stripPNG(br *bufio.Reader, bw *bufio.Writer) error {
	sig, err := readN(br, 8)
	if err != nil {
		return err
	}
	bw.Write(sig)
	first := true
	for {
		hdr, err := readN(br, 8)
		if err != nil {
			return err
		}
		n := binary.BigEndian.Uint32(hdr[:4])
		typ := string(hdr[4:8])
		if n > 1<<31-1 {
			return ErrBadImage
		}
		if first && typ != "IHDR" {
			return ErrBadImage
		}
		first = false
		keep := keepPNG[typ]
		if keep {
			bw.Write(hdr)
			if _, err := io.CopyN(bw, br, int64(n)+4); err != nil { // data and its CRC
				return ErrBadImage
			}
		} else if _, err := io.CopyN(io.Discard, br, int64(n)+4); err != nil {
			return ErrBadImage
		}
		if typ == "IEND" {
			return nil // anything after IEND is dropped
		}
	}
}
