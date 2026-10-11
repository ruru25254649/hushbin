// Package web embeds the built single-page app. `npm run build` (Vite)
// writes web/dist/, which must exist when the Go binary is compiled.
// Only cmd/hushbin imports this package; the server packages take the SPA
// as an fs.FS so they can be tested without a frontend build.
package web

import "embed"

// Dist holds the frontend build; use fs.Sub(Dist, "dist") for its root.
//
//go:embed all:dist
var Dist embed.FS
