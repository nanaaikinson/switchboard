// Package dashboard embeds the built web dashboard (dist/, from
// `npm run build`) into the sb binary. dist/ is committed so that `go build`
// needs no Node toolchain; CI checks it matches the sources.
package dashboard

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Assets returns the built dashboard, with index.html at the root.
func Assets() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // dist is embedded at build time; it always exists
	}
	return sub
}
