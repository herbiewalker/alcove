// Command logocrop makes the square header mark and favicons from the square Alcove icon (the A, heart
// and server stack). It uses only the standard library.
//
//	go run ./tools/logocrop docs/img/alcove-icon-512.png internal/server/static
//
// The mark keeps the logo's own deep-purple background, so it reads as a small rounded tile in
// both the light and the dark theme (the CSS rounds the corners).
package main

import (
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
)

// The master icon is already a square tile, so the crop is the whole image.
var crop = image.Rectangle{}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: logocrop <alcove-logo.png> <out dir>")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, "logocrop:", err)
		os.Exit(1)
	}
}

func run(src, outDir string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return err
	}
	crop := crop
	if crop.Empty() {
		crop = img.Bounds()
	}
	if crop.Dx() != crop.Dy() {
		return fmt.Errorf("the source must be square, got %v", crop)
	}
	if !crop.In(img.Bounds()) {
		return fmt.Errorf("the crop %v does not fit the image %v", crop, img.Bounds())
	}
	sq := image.NewRGBA(image.Rect(0, 0, crop.Dx(), crop.Dy()))
	draw.Draw(sq, sq.Bounds(), img, crop.Min, draw.Src)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	for _, size := range []int{32, 96, 192} {
		out := filepath.Join(outDir, fmt.Sprintf("mark-%d.png", size))
		if err := write(out, boxResize(sq, size)); err != nil {
			return err
		}
		fmt.Println("wrote", out)
	}
	return nil
}

func write(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// boxResize shrinks a square image by averaging the source pixels under each target pixel.
func boxResize(src *image.RGBA, size int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	sw := src.Bounds().Dx()
	for y := 0; y < size; y++ {
		y0, y1 := y*sw/size, (y+1)*sw/size
		for x := 0; x < size; x++ {
			x0, x1 := x*sw/size, (x+1)*sw/size
			var r, g, b, a, n uint32
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					c := src.RGBAAt(sx, sy)
					r, g, b, a, n = r+uint32(c.R), g+uint32(c.G), b+uint32(c.B), a+uint32(c.A), n+1
				}
			}
			if n == 0 {
				continue
			}
			i := dst.PixOffset(x, y)
			dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2], dst.Pix[i+3] = uint8(r/n), uint8(g/n), uint8(b/n), uint8(a/n)
		}
	}
	return dst
}
