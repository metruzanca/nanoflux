package httpapi

import (
	"net/http"

	"github.com/metruzanca/nanoflux/internal/auth"
)

// errorPages replaces the standard library's plain-text 404/405 body with the
// styled in-app error page for full-page browser navigations.
//
// It is installed inside navCountsMiddleware, which resolves the session for
// exactly the paths that render the topbar (navCountsPath), so the error page
// carries the real navbar for a signed-in visitor and stays brand-only for an
// anonymous one.
//
// Only GET navigations that would render the topbar are rewritten. JSON API
// calls, htmx fragments and non-GET form posts keep their current bodies,
// because their consumers (the extension, app.js) expect the raw response.
//
// The mux has no hook for its default NotFound/MethodNotAllowed handlers, and a
// catch-all "/" pattern would swallow the 405 (a wildcard matches every method),
// so the status is captured with a ResponseWriter wrapper instead.
func (s *Server) errorPages(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("HX-Request") != "" || !navCountsPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		ec := &errorCapture{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(ec, r)
		if ec.status != http.StatusNotFound && ec.status != http.StatusMethodNotAllowed {
			return
		}
		u, _ := auth.UserFrom(r)
		h := w.Header()
		// The captured handler may have set the plain-text headers; replace them
		// with an HTML response. The Allow header (405) is kept for display.
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Del("Content-Length")
		h.Del("X-Content-Type-Options")
		w.WriteHeader(ec.status)
		_ = basePage(errorTitle(ec.status), u, errorPage(ec.status, r.URL.Path, h.Get("Allow"))).
			Render(r.Context(), w)
	})
}

// errorTitle is the <title> text for a captured error status.
func errorTitle(status int) string {
	if status == http.StatusMethodNotAllowed {
		return "method not allowed"
	}
	return "page not found"
}

// errorCapture records the response status and, once it is a 404 or 405, drops
// the body so errorPages can render its own page. Other responses are forwarded
// to the underlying writer unchanged.
type errorCapture struct {
	http.ResponseWriter
	status int
	drop   bool
}

func (c *errorCapture) WriteHeader(code int) {
	c.status = code
	if code == http.StatusNotFound || code == http.StatusMethodNotAllowed {
		c.drop = true
		return
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *errorCapture) Write(p []byte) (int, error) {
	if c.drop {
		return len(p), nil
	}
	return c.ResponseWriter.Write(p)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (c *errorCapture) Unwrap() http.ResponseWriter { return c.ResponseWriter }
