package photo

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	_ "image/png" // registers the PNG decoder
)

// MaxPixels bounds the picture a thumbnail is made from. Decoding holds roughly four bytes per
// pixel, so this keeps one decode well inside the container's memory limit (D-44).
const MaxPixels = 50_000_000

// ErrTooBig means the picture has more pixels than Alcove will decode for a thumbnail.
var ErrTooBig = errors.New("photo: too many pixels for a thumbnail")

// Thumbnail makes a small JPEG of a stored picture (JPEG or PNG), turned the way the picture's
// orientation says, with its longest side at most edge pixels. It is drawn from the decoded
// pixels only, so nothing else from the file (position, camera, comments) can be in it. Anything
// that does not decode, or is too large, is an error and the caller falls back to the picture.
func Thumbnail(data []byte, edge int) ([]byte, error) {
	k := Sniff(data)
	if k == Unknown {
		return nil, ErrBadImage
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width < 1 || cfg.Height < 1 {
		return nil, ErrBadImage
	}
	if int64(cfg.Width)*int64(cfg.Height) > MaxPixels {
		return nil, ErrTooBig
	}
	orient := 1
	if k == JPEG {
		orient = Orientation(bytes.NewReader(data))
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, ErrBadImage
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, scale(src, orient, edge), &jpeg.Options{Quality: 78}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// scale draws src, turned by orient, into an image whose longest side is at most edge. Each new
// pixel is the average of a small grid of samples from the block of the original it covers, which
// is cheap and looks fine at thumbnail size. Transparent pixels are drawn over white.
func scale(src image.Image, orient, edge int) *image.RGBA {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	w, h := sw, sh // the size once turned
	if orient >= 5 {
		w, h = sh, sw
	}
	dw, dh := w, h
	if w > edge || h > edge {
		if w >= h {
			dw, dh = edge, max(1, h*edge/w)
		} else {
			dw, dh = max(1, w*edge/h), edge
		}
	}
	k := 1
	if f := max(w/dw, h/dh); f > 1 {
		k = min(4, f)
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	fx, fy := float64(w)/float64(dw), float64(h)/float64(dh)
	for y := 0; y < dh; y++ {
		for x := 0; x < dw; x++ {
			var r, g, bl, n uint32
			for j := 0; j < k; j++ {
				oy := min(h-1, int((float64(y)+(float64(j)+0.5)/float64(k))*fy))
				for i := 0; i < k; i++ {
					ox := min(w-1, int((float64(x)+(float64(i)+0.5)/float64(k))*fx))
					sx, sy := turn(ox, oy, sw, sh, orient)
					cr, cg, cb, ca := src.At(b.Min.X+sx, b.Min.Y+sy).RGBA()
					white := 0xFFFF - ca // composite over white
					r += cr + white
					g += cg + white
					bl += cb + white
					n++
				}
			}
			o := dst.PixOffset(x, y)
			dst.Pix[o], dst.Pix[o+1], dst.Pix[o+2], dst.Pix[o+3] = byte(r/n>>8), byte(g/n>>8), byte(bl/n>>8), 0xFF
		}
	}
	return dst
}

// turn maps a position in the turned picture back to the stored one (EXIF orientation 1 to 8).
func turn(x, y, w, h, orient int) (int, int) {
	switch orient {
	case 2:
		return w - 1 - x, y
	case 3:
		return w - 1 - x, h - 1 - y
	case 4:
		return x, h - 1 - y
	case 5:
		return y, x
	case 6:
		return y, h - 1 - x
	case 7:
		return w - 1 - y, h - 1 - x
	case 8:
		return w - 1 - y, x
	}
	return x, y
}
