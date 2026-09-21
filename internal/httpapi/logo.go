package httpapi

import (
	"net/http"
	"strings"

	"github.com/metruzanca/nanoflux/internal/web"
)

// logoAccent returns a valid "#rrggbb" accent for the brand logo, falling back
// to the built-in default when the stored value is missing or malformed. The
// result is safe to embed in a URL query and in the generated SVG.
func logoAccent(c string) string {
	if c != "" && !strings.HasPrefix(c, "#") {
		c = "#" + c
	}
	if accentRe.MatchString(c) {
		return c
	}
	return defaultAccent
}

// logoColorParam returns the accent as a bare "#rrggbb"-free hex string for use
// in a URL query. The leading "#" is dropped because in a query string "#"
// begins a URL fragment and would never reach the server.
func logoColorParam(c string) string {
	return strings.TrimPrefix(logoAccent(c), "#")
}

// logoSVG renders the "η" brand mark filled with an accent color.
func logoSVG(color string) string {
	return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24"><text x="12" y="17" text-anchor="middle" font-family="sans-serif" font-weight="700" font-size="18" fill="` + color + `">η</text></svg>`
}

// serveLogo renders the "η" brand mark as an SVG filled with the requested
// accent color. The color rides in the URL (?c=#rrggbb), so the response is
// content-addressed: the browser can cache each accent immutably and a change
// of accent yields a fresh URL that re-fetches automatically.
func (s *Server) serveLogo(w http.ResponseWriter, r *http.Request) {
	color := logoAccent(strings.ToLower(strings.TrimSpace(r.URL.Query().Get("c"))))

	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	_, _ = w.Write([]byte(logoSVG(color)))
}

// serveFavicon serves the brand mark filled with the requested accent color
// (same color-addressed caching as the logo). The tab favicon follows the
// user's accent so it stays consistent with the topbar mark.
func (s *Server) serveFavicon(w http.ResponseWriter, r *http.Request) {
	color := logoAccent(strings.ToLower(strings.TrimSpace(r.URL.Query().Get("c"))))

	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	_, _ = w.Write([]byte(logoSVG(color)))
}

// serveEmbedded serves a static asset embedded under internal/web/static at a
// non-/static/ route (the PWA manifest and service worker), with the given
// content type and cache policy.
func serveEmbedded(w http.ResponseWriter, r *http.Request, name, contentType, cache string) {
	data, err := web.ReadStatic(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", cache)
	_, _ = w.Write(data)
}
