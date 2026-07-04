// Package ui embeds the built React SPA (ui/dist) into the Go binary.
//
// The dist directory is produced by `npm run build`. A placeholder index.html
// is committed so the embed compiles before the first UI build.
package ui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// FS returns the embedded dist directory rooted so that "index.html" and asset
// paths resolve directly.
func FS() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
