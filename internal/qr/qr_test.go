package qr

import (
	"bytes"
	"strings"
	"testing"
)

// The published worked example for version 1, level M ("HELLO WORLD"): 16 data words, 10 check words.
func TestReedSolomonMatchesThePublishedExample(t *testing.T) {
	data := []byte{32, 91, 11, 120, 209, 114, 220, 77, 67, 64, 236, 17, 236, 17, 236, 17}
	want := []byte{196, 35, 39, 119, 235, 215, 231, 226, 93, 23}
	if got := rsRemainder(data, 10); !bytes.Equal(got, want) {
		t.Fatalf("check words %v, want %v", got, want)
	}
}

func TestBlockTablesAddUp(t *testing.T) {
	total := []int{0, 26, 44, 70, 100, 134, 172, 196, 242, 292}
	for v := 1; v <= maxVersion; v++ {
		x := versions[v]
		if got := x.dataWords() + (x.n1+x.n2)*x.ecc; got != total[v] {
			t.Errorf("version %d: %d words, want %d", v, got, total[v])
		}
	}
}

// decode reads a code back the way a scanner would: format word, mask, zigzag, check words, bytes.
func decode(t *testing.T, c *Code) string {
	t.Helper()
	ver := (c.Size - 17) / 4
	g := newGrid(c.Size)
	g.drawFunction(ver)
	// Read the format word (first copy) and find the mask it names.
	var fmtBits int
	pos := [][2]int{{8, 0}, {8, 1}, {8, 2}, {8, 3}, {8, 4}, {8, 5}, {8, 7}, {8, 8}, {7, 8}, {5, 8}, {4, 8}, {3, 8}, {2, 8}, {1, 8}, {0, 8}}
	for i, p := range pos {
		if c.m[p[1]][p[0]] {
			fmtBits |= 1 << i
		}
	}
	mask := -1
	for m := 0; m < 8; m++ {
		if formatBits(m) == fmtBits {
			mask = m
		}
	}
	if mask < 0 {
		t.Fatalf("format word %015b is not valid", fmtBits)
	}
	// The second copy must say the same thing.
	var second int
	for i := 0; i < 8; i++ {
		if c.m[8][c.Size-1-i] {
			second |= 1 << i
		}
	}
	for i := 8; i < 15; i++ {
		if c.m[c.Size-15+i][8] {
			second |= 1 << i
		}
	}
	if second != fmtBits {
		t.Fatalf("the two format copies differ: %015b vs %015b", fmtBits, second)
	}
	// Unmask and read the data modules in the standard order.
	var raw []byte
	var cur, n int
	for right := c.Size - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5
		}
		for vert := 0; vert < c.Size; vert++ {
			for j := 0; j < 2; j++ {
				x := right - j
				y := vert
				if (right+1)&2 == 0 {
					y = c.Size - 1 - vert
				}
				if g.fn[y][x] {
					continue
				}
				bit := c.m[y][x] != maskBit(mask, x, y)
				cur <<= 1
				if bit {
					cur |= 1
				}
				if n++; n == 8 {
					raw = append(raw, byte(cur))
					cur, n = 0, 0
				}
			}
		}
	}
	v := versions[ver]
	nb := v.n1 + v.n2
	blocks := make([][]byte, nb)
	eccs := make([][]byte, nb)
	idx := 0
	maxD := v.d1
	if v.n2 > 0 {
		maxD = v.d2
	}
	for i := 0; i < maxD; i++ {
		for b := 0; b < nb; b++ {
			d := v.d1
			if b >= v.n1 {
				d = v.d2
			}
			if i < d {
				blocks[b] = append(blocks[b], raw[idx])
				idx++
			}
		}
	}
	for i := 0; i < v.ecc; i++ {
		for b := 0; b < nb; b++ {
			eccs[b] = append(eccs[b], raw[idx])
			idx++
		}
	}
	var data []byte
	for b := 0; b < nb; b++ {
		if !bytes.Equal(rsRemainder(blocks[b], v.ecc), eccs[b]) {
			t.Fatalf("block %d fails its check words", b)
		}
		data = append(data, blocks[b]...)
	}
	// Byte mode: 0100, 8-bit count, bytes.
	if data[0]>>4 != 0b0100 {
		t.Fatalf("mode %04b is not byte mode", data[0]>>4)
	}
	count := int(data[0]&0x0F)<<4 | int(data[1]>>4)
	out := make([]byte, count)
	for i := range out {
		out[i] = data[1+i]<<4 | data[2+i]>>4
	}
	return string(out)
}

func TestEveryVersionRoundTripsThroughTheDecoder(t *testing.T) {
	for _, n := range []int{1, 5, 14, 17, 25, 32, 40, 53, 78, 106, 134, 154, 178, 180} {
		text := strings.Repeat("http://192.168.1.20:8080/join?", 8)[:0]
		for len(text) < n {
			text += "abcXYZ019/:.-_~?=&%"[len(text)%19 : len(text)%19+1]
		}
		c, err := New(text)
		if err != nil {
			t.Fatalf("%d bytes: %v", n, err)
		}
		if got := decode(t, c); got != text {
			t.Errorf("%d bytes decoded as %q", n, got)
		}
	}
	if _, err := New(strings.Repeat("x", 181)); err != ErrTooLong {
		t.Errorf("181 bytes: %v", err)
	}
}

func TestAddressDecodesAndSVGIsSafe(t *testing.T) {
	const url = "http://192.168.1.20:8080/join"
	c, err := New(url)
	if err != nil {
		t.Fatal(err)
	}
	if got := decode(t, c); got != url {
		t.Fatalf("decoded %q", got)
	}
	// Finder patterns sit where a scanner looks for them.
	for _, o := range [][2]int{{0, 0}, {c.Size - 7, 0}, {0, c.Size - 7}} {
		if !c.Dark(o[0], o[1]) || !c.Dark(o[0]+6, o[1]+6) || c.Dark(o[0]+1, o[1]+1) || !c.Dark(o[0]+3, o[1]+3) {
			t.Errorf("finder at %v is wrong", o)
		}
	}
	svg := c.SVG(`a "label" <b>`)
	if !strings.HasPrefix(svg, "<svg") || !strings.HasSuffix(svg, "</svg>") || strings.Contains(svg, "<b>") || strings.Contains(svg, `"label"`) {
		t.Errorf("svg: %.200s", svg)
	}
	if strings.Contains(svg, "script") {
		t.Error("the svg carries text it should not")
	}
}
