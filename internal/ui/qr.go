package ui

import (
	"fmt"
	"html"
	"html/template"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

// QRCodeSVG is a scannable join-code SVG. Empty content yields an empty box.
func QRCodeSVG(content string) (template.HTML, error) {
	if strings.TrimSpace(content) == "" {
		return `<svg class="ui-qr-svg" viewBox="0 0 1 1" aria-hidden="true"></svg>`, nil
	}
	code, err := qrcode.New(content, qrcode.Medium)
	if err != nil {
		return "", fmt.Errorf("ui: qr code: %w", err)
	}
	bits := code.Bitmap()
	n := len(bits)
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="ui-qr-svg" viewBox="0 0 %d %d" role="img" aria-label="Join QR">`, n, n)
	fmt.Fprintf(&b, `<title>%s</title>`, html.EscapeString(content))
	b.WriteString(`<rect width="100%" height="100%" fill="#fff"/>`)
	for y, row := range bits {
		for x, on := range row {
			if !on {
				continue
			}
			fmt.Fprintf(&b, `<rect x="%d" y="%d" width="1" height="1" fill="#111"/>`, x, y)
		}
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String()), nil
}
