// Package web holds the dashboard: plain HTML, CSS and JavaScript with no
// build step, embedded in the Sophron binary and served by the API.
package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var files embed.FS

// FS returns the dashboard files, rooted at static/.
func FS() fs.FS {
	sub, err := fs.Sub(files, "static")
	if err != nil {
		panic(err) // the embed above guarantees the directory exists
	}
	return sub
}
