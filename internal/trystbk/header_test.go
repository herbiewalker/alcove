package trystbk

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func headerBytes(version byte, iters uint32) []byte {
	b := append([]byte(Magic), version)
	b = append(b, bytes.Repeat([]byte{7}, SaltSize)...)
	return binary.BigEndian.AppendUint32(b, iters)
}

func TestHeaderSizeMatchesParts(t *testing.T) {
	parts := MagicSize + VersionSize + SaltSize + IterationsSize
	size, want := HeaderSize, 29
	if size != parts {
		t.Fatalf("HeaderSize = %d, parts sum to %d", size, parts)
	}
	if size != want {
		t.Fatalf("HeaderSize = %d, want %d", size, want)
	}
}

func TestParseHeader_Valid(t *testing.T) {
	for _, v := range []byte{FormatVersion1, FormatVersion2} {
		h, err := ParseHeader(bytes.NewReader(headerBytes(v, DefaultIterations)))
		if err != nil {
			t.Fatalf("v%d: %v", v, err)
		}
		if h.Version != int(v) || h.Iterations != DefaultIterations || h.Salt[0] != 7 {
			t.Errorf("v%d: %+v", v, h)
		}
	}
}

func TestParseHeader_Rejects(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want error
	}{
		{"bad magic", append([]byte("NOTTRYST"), headerBytes(1, DefaultIterations)[8:]...), ErrBadMagic},
		{"version 0", headerBytes(0, DefaultIterations), ErrUnsupportedVer},
		{"version 3", headerBytes(3, DefaultIterations), ErrUnsupportedVer},
		{"truncated", headerBytes(1, DefaultIterations)[:10], ErrHeaderTruncated},
		{"empty", nil, ErrHeaderTruncated},
		{"iterations just below", headerBytes(1, MinIterations-1), ErrIterationsRange},
		{"iterations just above", headerBytes(1, MaxIterations+1), ErrIterationsRange},
		{"iterations zero", headerBytes(1, 0), ErrIterationsRange},
		{"iterations int32 max", headerBytes(1, 0x7FFFFFFF), ErrIterationsRange},
		{"iterations sign bit set", headerBytes(1, 0xFFFFFFFF), ErrIterationsRange},
	}
	for _, c := range cases {
		if _, err := ParseHeader(bytes.NewReader(c.in)); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
}

func TestParseHeader_AcceptsBounds(t *testing.T) {
	for _, n := range []uint32{MinIterations, MaxIterations} {
		if _, err := ParseHeader(bytes.NewReader(headerBytes(2, n))); err != nil {
			t.Errorf("iterations %d: %v", n, err)
		}
	}
}

func TestHeaderMarshalRoundTrip(t *testing.T) {
	h := Header{Version: FormatVersion2, Iterations: 123_456}
	h.Salt[3] = 9
	b, err := h.Marshal()
	if err != nil || len(b) != HeaderSize {
		t.Fatalf("Marshal: %v len=%d", err, len(b))
	}
	got, err := ParseHeader(bytes.NewReader(b))
	if err != nil || got != h {
		t.Fatalf("round trip: %+v %v", got, err)
	}
}

func TestHeaderMarshalRefusesWhatTrystRefuses(t *testing.T) {
	if _, err := (Header{Version: 9, Iterations: DefaultIterations}).Marshal(); !errors.Is(err, ErrUnsupportedVer) {
		t.Errorf("bad version: %v", err)
	}
	for _, n := range []int{0, MinIterations - 1, MaxIterations + 1} {
		if _, err := (Header{Version: 1, Iterations: n}).Marshal(); !errors.Is(err, ErrIterationsRange) {
			t.Errorf("iterations %d: %v", n, err)
		}
	}
}

func TestValidateEntryName(t *testing.T) {
	good := []string{"data.json", "settings.json", "media/abc", "media/3f2a-9", "a/b/c.txt", "dir/"}
	for _, n := range good {
		if err := validateEntryName(n); err != nil {
			t.Errorf("%q rejected: %v", n, err)
		}
	}
	bad := []string{"", "/etc/passwd", "../x", "a/../../x", "..", "media/../../x", `a\b`, `..\x`, "/", "a/../../../etc"}
	for _, n := range bad {
		if err := validateEntryName(n); !errors.Is(err, ErrZipSlip) {
			t.Errorf("%q: err = %v, want ErrZipSlip", n, err)
		}
	}
}
