package photo

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

// quadrants draws a w by h picture: red top left, green top right, blue bottom left, white bottom right.
func quadrants(w, h int) *image.RGBA {
	im := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{255, 255, 255, 255}
			switch {
			case y < h/2 && x < w/2:
				c = color.RGBA{255, 0, 0, 255}
			case y < h/2:
				c = color.RGBA{0, 255, 0, 255}
			case x < w/2:
				c = color.RGBA{0, 0, 255, 255}
			}
			im.Set(x, y, c)
		}
	}
	return im
}

func encodeJPEG(t *testing.T, im image.Image, extra ...[]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, im, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	out := append([]byte{}, raw[:2]...)
	for _, e := range extra {
		out = append(out, e...)
	}
	return append(out, raw[2:]...)
}

// near reports whether c is close to the 8-bit colour r, g, b (JPEG is lossy).
func near(c color.Color, r, g, b uint32) bool {
	cr, cg, cb, _ := c.RGBA()
	d := func(a, b uint32) bool {
		if a > b {
			a, b = b, a
		}
		return b-a < 0x3000
	}
	return d(cr, r<<8|r) && d(cg, g<<8|g) && d(cb, b<<8|b)
}

func decodeThumb(t *testing.T, b []byte) image.Image {
	t.Helper()
	im, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("the thumbnail does not decode: %v", err)
	}
	return im
}

func mustThumb(t *testing.T, b []byte, edge int) []byte {
	t.Helper()
	th, err := Thumbnail(b, edge)
	if err != nil {
		t.Fatal(err)
	}
	return th
}

func TestThumbnailShrinksAndKeepsTheProportions(t *testing.T) {
	im := decodeThumb(t, mustThumb(t, encodeJPEG(t, quadrants(800, 400)), 100))
	if im.Bounds().Dx() != 100 || im.Bounds().Dy() != 50 {
		t.Fatalf("size %v, want 100x50", im.Bounds())
	}
	if !near(im.At(10, 10), 255, 0, 0) || !near(im.At(90, 10), 0, 255, 0) || !near(im.At(10, 40), 0, 0, 255) || !near(im.At(90, 40), 255, 255, 255) {
		t.Error("the quadrants are in the wrong places")
	}
	// A small picture is not enlarged.
	if decodeThumb(t, mustThumb(t, encodeJPEG(t, quadrants(40, 30)), 100)).Bounds().Dx() != 40 {
		t.Error("a small picture was resized")
	}
}

func TestThumbnailAppliesTheOrientation(t *testing.T) {
	// An 80x40 stored picture marked "rotate 90 clockwise" shows as 40x80: the stored bottom left
	// (blue) is at the top left and the stored top left (red) at the top right.
	im := decodeThumb(t, mustThumb(t, encodeJPEG(t, quadrants(80, 40), exifWith(6, false)), 200))
	if im.Bounds().Dx() != 40 || im.Bounds().Dy() != 80 {
		t.Fatalf("size %v, want 40x80", im.Bounds())
	}
	if !near(im.At(5, 5), 0, 0, 255) || !near(im.At(35, 5), 255, 0, 0) || !near(im.At(5, 75), 255, 255, 255) || !near(im.At(35, 75), 0, 255, 0) {
		t.Error("orientation 6 drew the wrong corners")
	}
	// 3 is a half turn: red goes to the bottom right.
	im = decodeThumb(t, mustThumb(t, encodeJPEG(t, quadrants(80, 40), exifWith(3, true)), 200))
	if !near(im.At(75, 35), 255, 0, 0) || !near(im.At(5, 5), 255, 255, 255) {
		t.Error("orientation 3 drew the wrong corners")
	}
}

func TestThumbnailCarriesNoMetadata(t *testing.T) {
	src := encodeJPEG(t, quadrants(200, 100), exifWith(1, false), seg(0xFE, "COMMENTMARK"), seg(0xE1, "http://ns.adobe.com/xap/1.0/\x00XMPMARK"))
	th := mustThumb(t, src, 50)
	for _, leak := range []string{"GPSMARK", "COMMENTMARK", "XMPMARK", "Exif"} {
		if bytes.Contains(th, []byte(leak)) {
			t.Errorf("the thumbnail contains %q", leak)
		}
	}
}

func TestThumbnailFromPNGDrawsTransparencyOnWhite(t *testing.T) {
	im := image.NewNRGBA(image.Rect(0, 0, 60, 60))
	for y := 0; y < 60; y++ {
		for x := 0; x < 30; x++ { // the right half stays fully transparent
			im.Set(x, y, color.NRGBA{255, 0, 0, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, im); err != nil {
		t.Fatal(err)
	}
	out := decodeThumb(t, mustThumb(t, buf.Bytes(), 30))
	if !near(out.At(3, 15), 255, 0, 0) || !near(out.At(27, 15), 255, 255, 255) {
		t.Error("PNG colours or transparency are wrong")
	}
}

func TestThumbnailRefusesWhatItCannotTrust(t *testing.T) {
	if _, err := Thumbnail([]byte("hello world"), 100); err == nil {
		t.Error("text was accepted")
	}
	good := encodeJPEG(t, quadrants(40, 40))
	if _, err := Thumbnail(good[:len(good)/2], 100); err == nil {
		t.Error("half a picture was accepted")
	}
	// A PNG header that claims a huge size is refused before anything is allocated.
	var buf bytes.Buffer
	_ = png.Encode(&buf, image.NewGray(image.Rect(0, 0, 4, 4)))
	big := append([]byte{}, buf.Bytes()...)
	copy(big[16:20], []byte{0x00, 0x01, 0x00, 0x00}) // width 65536
	copy(big[20:24], []byte{0x00, 0x01, 0x00, 0x00}) // height 65536
	if _, err := Thumbnail(big, 100); err == nil {
		t.Error("a picture of four billion pixels was accepted")
	}
}
