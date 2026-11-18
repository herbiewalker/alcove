// Package qr draws QR codes locally (F31, D-48). It exists so a phone can open Alcove or an invite
// page by pointing its camera at the screen, with no library to license and no outbound call.
//
// It writes only what Alcove needs: byte mode, error correction level M, versions 1 to 9 (up to 180
// bytes), which is far more than a web address. The output is a small inline SVG made of numbers
// alone, so nothing in it can carry markup from the text.
package qr

import (
	"errors"
	"strconv"
	"strings"
)

// ErrTooLong means the text does not fit in a version 9 code.
var ErrTooLong = errors.New("qr: text too long for a QR code here")

// version describes level M for one version: error-correction words per block and the block layout.
type version struct {
	ecc            int
	n1, d1, n2, d2 int // n1 blocks of d1 data words, then n2 blocks of d2 data words
	align          []int
}

var versions = [...]version{
	1: {10, 1, 16, 0, 0, nil},
	2: {16, 1, 28, 0, 0, []int{6, 18}},
	3: {26, 1, 44, 0, 0, []int{6, 22}},
	4: {18, 2, 32, 0, 0, []int{6, 26}},
	5: {24, 2, 43, 0, 0, []int{6, 30}},
	6: {16, 4, 27, 0, 0, []int{6, 34}},
	7: {18, 4, 31, 0, 0, []int{6, 22, 38}},
	8: {22, 2, 38, 2, 39, []int{6, 24, 42}},
	9: {22, 3, 36, 2, 37, []int{6, 26, 46}},
}

const maxVersion = 9

func (v version) dataWords() int { return v.n1*v.d1 + v.n2*v.d2 }

// ---- Reed-Solomon over GF(256), polynomial 0x11d ----

var expT [512]byte
var logT [256]int

func init() {
	x := 1
	for i := 0; i < 255; i++ {
		expT[i] = byte(x)
		logT[x] = i
		x <<= 1
		if x&0x100 != 0 {
			x ^= 0x11d
		}
	}
	for i := 255; i < 512; i++ {
		expT[i] = expT[i-255]
	}
}

func gmul(a, b byte) byte {
	if a == 0 || b == 0 {
		return 0
	}
	return expT[logT[a]+logT[b]]
}

// rsRemainder returns the n error-correction words for data.
func rsRemainder(data []byte, n int) []byte {
	gen := make([]byte, n) // generator polynomial without its leading 1, highest power first
	gen[n-1] = 1
	root := byte(1)
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			gen[j] = gmul(gen[j], root)
			if j+1 < n {
				gen[j] ^= gen[j+1]
			}
		}
		root = gmul(root, 2)
	}
	rem := make([]byte, n)
	for _, b := range data {
		f := b ^ rem[0]
		copy(rem, rem[1:])
		rem[n-1] = 0
		for j := range rem {
			rem[j] ^= gmul(gen[j], f)
		}
	}
	return rem
}

// ---- encoding ----

// Code is a finished QR code: Size by Size modules, true for dark.
type Code struct {
	Size int
	m    [][]bool
}

// Dark reports whether the module at column x, row y is dark.
func (c *Code) Dark(x, y int) bool { return c.m[y][x] }

// New encodes text as a QR code.
func New(text string) (*Code, error) {
	data := []byte(text)
	ver := 0
	for v := 1; v <= maxVersion; v++ {
		if len(data)+2 <= versions[v].dataWords() { // 4 mode bits + 8 count bits + 4 terminator bits fit in 2 words
			ver = v
			break
		}
	}
	if ver == 0 {
		return nil, ErrTooLong
	}
	v := versions[ver]

	var bits []bool
	put := func(val, n int) {
		for i := n - 1; i >= 0; i-- {
			bits = append(bits, val>>i&1 == 1)
		}
	}
	put(0b0100, 4)
	put(len(data), 8)
	for _, b := range data {
		put(int(b), 8)
	}
	capBits := v.dataWords() * 8
	for i := 0; i < 4 && len(bits) < capBits; i++ {
		bits = append(bits, false)
	}
	for len(bits)%8 != 0 {
		bits = append(bits, false)
	}
	words := make([]byte, 0, v.dataWords())
	for i := 0; i < len(bits); i += 8 {
		var w byte
		for j := 0; j < 8; j++ {
			w <<= 1
			if bits[i+j] {
				w |= 1
			}
		}
		words = append(words, w)
	}
	for pad := byte(0xEC); len(words) < v.dataWords(); pad ^= 0xEC ^ 0x11 {
		words = append(words, pad)
	}
	return build(ver, interleave(v, words)), nil
}

// interleave splits the data into blocks, adds the error-correction words and weaves the blocks.
func interleave(v version, words []byte) []byte {
	var blocks, eccs [][]byte
	pos := 0
	for i := 0; i < v.n1+v.n2; i++ {
		n := v.d1
		if i >= v.n1 {
			n = v.d2
		}
		blk := words[pos : pos+n]
		pos += n
		blocks = append(blocks, blk)
		eccs = append(eccs, rsRemainder(blk, v.ecc))
	}
	var out []byte
	maxD := v.d1
	if v.n2 > 0 {
		maxD = v.d2
	}
	for i := 0; i < maxD; i++ {
		for _, b := range blocks {
			if i < len(b) {
				out = append(out, b[i])
			}
		}
	}
	for i := 0; i < v.ecc; i++ {
		for _, e := range eccs {
			out = append(out, e[i])
		}
	}
	return out
}

// ---- drawing ----

type grid struct {
	size int
	m    [][]bool
	fn   [][]bool // function modules: finders, timing, alignment, format and version areas
}

func newGrid(size int) *grid {
	g := &grid{size: size}
	g.m = make([][]bool, size)
	g.fn = make([][]bool, size)
	for i := range g.m {
		g.m[i] = make([]bool, size)
		g.fn[i] = make([]bool, size)
	}
	return g
}

func (g *grid) setFn(x, y int, dark bool) {
	g.m[y][x] = dark
	g.fn[y][x] = true
}

func abs(a int) int {
	if a < 0 {
		return -a
	}
	return a
}

func (g *grid) drawFunction(ver int) {
	size := g.size
	for i := 0; i < size; i++ {
		g.setFn(6, i, i%2 == 0)
		g.setFn(i, 6, i%2 == 0)
	}
	for _, c := range [][2]int{{3, 3}, {size - 4, 3}, {3, size - 4}} {
		for dy := -4; dy <= 4; dy++ {
			for dx := -4; dx <= 4; dx++ {
				d := max(abs(dx), abs(dy))
				x, y := c[0]+dx, c[1]+dy
				if x >= 0 && x < size && y >= 0 && y < size {
					g.setFn(x, y, d != 2 && d != 4)
				}
			}
		}
	}
	al := versions[ver].align
	for i, ay := range al {
		for j, ax := range al {
			if (i == 0 && j == 0) || (i == 0 && j == len(al)-1) || (i == len(al)-1 && j == 0) {
				continue
			}
			for dy := -2; dy <= 2; dy++ {
				for dx := -2; dx <= 2; dx++ {
					g.setFn(ax+dx, ay+dy, max(abs(dx), abs(dy)) != 1)
				}
			}
		}
	}
	g.drawFormat(0) // reserves the format area; drawn again with the real mask
	if ver >= 7 {
		rem := ver
		for i := 0; i < 12; i++ {
			rem = rem<<1 ^ (rem>>11)*0x1F25
		}
		bits := ver<<12 | rem
		for i := 0; i < 18; i++ {
			b := bits>>i&1 == 1
			a, c := size-11+i%3, i/3
			g.setFn(a, c, b)
			g.setFn(c, a, b)
		}
	}
}

// formatBits is the 15-bit format word for level M and the given mask.
func formatBits(mask int) int {
	data := 0<<3 | mask // level M is 00
	rem := data
	for i := 0; i < 10; i++ {
		rem = rem<<1 ^ (rem>>9)*0x537
	}
	return (data<<10 | rem) ^ 0x5412
}

func (g *grid) drawFormat(mask int) {
	bits := formatBits(mask)
	bit := func(i int) bool { return bits>>i&1 == 1 }
	size := g.size
	for i := 0; i <= 5; i++ {
		g.setFn(8, i, bit(i))
	}
	g.setFn(8, 7, bit(6))
	g.setFn(8, 8, bit(7))
	g.setFn(7, 8, bit(8))
	for i := 9; i < 15; i++ {
		g.setFn(14-i, 8, bit(i))
	}
	for i := 0; i < 8; i++ {
		g.setFn(size-1-i, 8, bit(i))
	}
	for i := 8; i < 15; i++ {
		g.setFn(8, size-15+i, bit(i))
	}
	g.setFn(8, size-8, true) // the always-dark module
}

func (g *grid) place(data []byte) {
	size, i := g.size, 0
	for right := size - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5
		}
		for vert := 0; vert < size; vert++ {
			for j := 0; j < 2; j++ {
				x := right - j
				y := vert
				if (right+1)&2 == 0 {
					y = size - 1 - vert
				}
				if !g.fn[y][x] && i < len(data)*8 {
					g.m[y][x] = data[i>>3]>>(7-uint(i&7))&1 == 1
					i++
				}
			}
		}
	}
}

func maskBit(mask, x, y int) bool {
	switch mask {
	case 0:
		return (x+y)%2 == 0
	case 1:
		return y%2 == 0
	case 2:
		return x%3 == 0
	case 3:
		return (x+y)%3 == 0
	case 4:
		return (x/3+y/2)%2 == 0
	case 5:
		return x*y%2+x*y%3 == 0
	case 6:
		return (x*y%2+x*y%3)%2 == 0
	}
	return ((x+y)%2+x*y%3)%2 == 0
}

func (g *grid) applyMask(mask int) {
	for y := 0; y < g.size; y++ {
		for x := 0; x < g.size; x++ {
			if !g.fn[y][x] && maskBit(mask, x, y) {
				g.m[y][x] = !g.m[y][x]
			}
		}
	}
}

func build(ver int, data []byte) *Code {
	g := newGrid(17 + 4*ver)
	g.drawFunction(ver)
	g.place(data)
	best, bestScore := 0, int(^uint(0)>>1)
	for mask := 0; mask < 8; mask++ {
		g.applyMask(mask)
		g.drawFormat(mask)
		if s := g.penalty(); s < bestScore {
			best, bestScore = mask, s
		}
		g.applyMask(mask) // undo
	}
	g.applyMask(best)
	g.drawFormat(best)
	return &Code{Size: g.size, m: g.m}
}

// penalty scores a masked grid by the four rules in the QR standard; lower is easier to scan.
func (g *grid) penalty() int {
	n, total := g.size, 0
	line := func(get func(i int) bool) {
		run, prev := 1, get(0)
		for i := 1; i < n; i++ {
			if get(i) == prev {
				run++
				continue
			}
			if run >= 5 {
				total += 3 + run - 5
			}
			run, prev = 1, get(i)
		}
		if run >= 5 {
			total += 3 + run - 5
		}
		// finder-like 1011101 with four light modules on a side
		for i := 0; i+7 <= n; i++ {
			if get(i) && !get(i+1) && get(i+2) && get(i+3) && get(i+4) && !get(i+5) && get(i+6) {
				light := func(a, b int) bool {
					for k := a; k < b; k++ {
						if k >= 0 && k < n && get(k) {
							return false
						}
					}
					return true
				}
				if light(i-4, i) || light(i+7, i+11) {
					total += 40
				}
			}
		}
	}
	for y := 0; y < n; y++ {
		line(func(i int) bool { return g.m[y][i] })
	}
	for x := 0; x < n; x++ {
		line(func(i int) bool { return g.m[i][x] })
	}
	dark := 0
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			if g.m[y][x] {
				dark++
			}
			if x+1 < n && y+1 < n && g.m[y][x] == g.m[y][x+1] && g.m[y][x] == g.m[y+1][x] && g.m[y][x] == g.m[y+1][x+1] {
				total += 3
			}
		}
	}
	all := n * n
	k := (abs(dark*20-all*10)+all-1)/all - 1
	return total + k*10
}

// SVG draws the code with a four-module quiet zone, black on white whatever the page theme, because
// phone scanners expect dark on light. The label is for screen readers.
func (c *Code) SVG(label string) string {
	const quiet = 4
	dim := strconv.Itoa(c.Size + 2*quiet)
	var b strings.Builder
	b.WriteString(`<svg class="qr" viewBox="0 0 ` + dim + ` ` + dim + `" role="img" aria-label="`)
	for _, r := range label { // the label is ours, but never trust a caller to be tidy
		if r == '"' || r == '<' || r == '>' || r == '&' || r < 0x20 {
			continue
		}
		b.WriteRune(r)
	}
	b.WriteString(`" shape-rendering="crispEdges"><rect width="` + dim + `" height="` + dim + `" fill="#fff"/><path fill="#000" d="`)
	for y := 0; y < c.Size; y++ {
		for x := 0; x < c.Size; {
			if !c.m[y][x] {
				x++
				continue
			}
			x0 := x
			for x < c.Size && c.m[y][x] {
				x++
			}
			b.WriteString("M" + strconv.Itoa(x0+quiet) + " " + strconv.Itoa(y+quiet) + "h" + strconv.Itoa(x-x0) + "v1h-" + strconv.Itoa(x-x0) + "z")
		}
	}
	b.WriteString(`"/></svg>`)
	return b.String()
}
