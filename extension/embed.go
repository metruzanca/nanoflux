// Package extension embeds the browser-extension source so a single nanoflux
// binary can serve it as a downloadable zip from /settings/extension.zip. The
// same files can also be loaded unpacked directly from this directory.
package extension

import "embed"

// FS holds the extension's files at their root (manifest.json, popup.*,
// options.*, api.js, htmx.min.js, icons/…). It deliberately excludes *.go so
// the generated zip contains only the extension itself.
//
//go:embed *.html *.js *.css *.json icons
var FS embed.FS
