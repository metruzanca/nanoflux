// Package imageutil decides whether fetched bytes may be stored and served
// back as an image. Only raster formats are accepted: an SVG is XML that can
// carry script and would execute on the app's origin if a browser navigated
// directly to the served file.
package imageutil

import (
	"net/http"
	"strings"
)

// rasterTypes are the browser-renderable bitmaps the app stores and serves.
var rasterTypes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
	"image/avif": true,
	"image/bmp":  true,
}

// Sniff returns the content type to store and serve when declared (an upstream
// Content-Type) or the leading bytes identify a raster image. It reports false
// for SVG, HTML, and anything else.
func Sniff(declared string, head []byte) (string, bool) {
	ct := strings.ToLower(strings.TrimSpace(strings.SplitN(declared, ";", 2)[0]))
	if rasterTypes[ct] {
		return ct, true
	}
	if d := http.DetectContentType(head); rasterTypes[d] {
		return d, true
	}
	return "", false
}
