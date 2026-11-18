package server

import (
	"html/template"

	"github.com/herbiewalker/alcove/internal/qr"
)

// qrFor draws text as an inline QR code (F31, D-48). The picture is made here, from numbers only, so
// no outbound call and no script is involved. If the text is too long it returns nothing and the
// page simply shows the plain address as it always did.
func qrFor(text, label string) template.HTML {
	c, err := qr.New(text)
	if err != nil {
		return ""
	}
	return template.HTML(c.SVG(label)) // #nosec G203 -- the SVG is built from integers and a label with markup characters removed
}
