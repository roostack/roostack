package web

import (
	"fmt"
	"html/template"
	"strings"

	"rsc.io/qr"
)

// qrSVG draws text as a QR code in SVG. It is always black on white with
// the standard four-module quiet zone, even in dark mode: some scanners
// can't read light-on-dark codes.
func qrSVG(text string) (template.HTML, error) {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return "", err
	}
	const quiet = 4
	size := code.Size + 2*quiet
	var path strings.Builder
	for y := 0; y < code.Size; y++ {
		for x := 0; x < code.Size; x++ {
			if code.Black(x, y) {
				fmt.Fprintf(&path, "M%d %dh1v1h-1z", x+quiet, y+quiet)
			}
		}
	}
	// Only numbers and a fixed path alphabet go into the markup.
	return template.HTML(fmt.Sprintf(`<svg class="qr" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" `+
		`role="img" aria-label="QR code for your authenticator app" shape-rendering="crispEdges">`+
		`<rect width="%d" height="%d" fill="#fff"/><path fill="#000" d="%s"/></svg>`,
		size, size, size, size, path.String())), nil
}
