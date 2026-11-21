// Package web embeds the built Svelte UI (web/dist) into the Go binary.
package web

import "embed"

// Dist holds the files produced by `npm run build`. The dist folder is
// committed with only a .gitkeep, so the Go build works before the UI is built.
//
//go:embed all:dist
var Dist embed.FS
