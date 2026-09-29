// Package uiassets embeds the built UI (run `npm run build` in ui/ first).
package uiassets

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS is the UI's file tree (index.html, app.js, styles.css, fonts/…).
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
