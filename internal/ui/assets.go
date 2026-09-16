// Package ui owns shared host templates and static browser assets.
package ui

import (
	"embed"
	"io/fs"
	"net/http"
)

// AssetVersion is the ?v= query on /static/ links. Bump it when embedded CSS or JS changes.
const AssetVersion = "neon-23"

//go:embed static/*
var staticFiles embed.FS

// StaticPath is the browser URL for an embedded file, with a cache-busting query.
func StaticPath(name string) string {
	return "/static/" + name + "?v=" + AssetVersion
}

// StaticHandler serves the embedded host browser assets.
func StaticHandler() http.Handler {
	files, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic("ui: embedded static directory is missing")
	}
	return http.FileServerFS(files)
}
