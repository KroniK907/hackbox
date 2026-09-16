package ui

import (
	"embed"
	"html/template"
	"io/fs"
)

//go:embed templates/*.html
var chromeTemplates embed.FS

// Funcs are template helpers for avatars, QR codes, and seed colors.
func Funcs() template.FuncMap {
	return template.FuncMap{
		"avatar": AvatarSVG,
		"color":  SeedColor,
		"static": StaticPath,
		"qr": func(content string) (template.HTML, error) {
			return QRCodeSVG(content)
		},
	}
}

// MustParse loads shared chrome defines, then page templates from pages.
func MustParse(pages fs.FS, glob string) *template.Template {
	root := template.New("hackbox").Funcs(Funcs())
	root = template.Must(root.ParseFS(chromeTemplates, "templates/chrome.html"))
	return template.Must(root.ParseFS(pages, glob))
}
