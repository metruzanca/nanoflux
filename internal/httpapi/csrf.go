package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/metruzanca/nanoflux/internal/auth"
)

// csrfToken derives a per-session CSRF token as a hash of the session token.
// The session token lives in an HttpOnly cookie the browser never exposes to
// scripts, so a cross-site page cannot produce this value. Nothing extra is
// stored server-side.
func csrfToken(sessionToken string) string {
	sum := sha256.Sum256([]byte("nanoflux-csrf:" + sessionToken))
	return hex.EncodeToString(sum[:])
}

// csrfExempt reports the unsafe-method paths that do not need a CSRF token:
// anonymous login/signup/demo (there is no session to protect) and the
// extension JSON API, which authenticates with a Bearer token, not a cookie.
func csrfExempt(path string) bool {
	switch path {
	case "/login", "/signup", "/demo":
		return true
	}
	return strings.HasPrefix(path, "/api/")
}

type csrfCtxKey struct{}

func withCSRFToken(r *http.Request, token string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), csrfCtxKey{}, token))
}

// csrfFrom returns the request's CSRF token, or "" for an anonymous request.
func csrfFrom(ctx context.Context) string {
	t, _ := ctx.Value(csrfCtxKey{}).(string)
	return t
}

// csrfMiddleware enforces the double-submit-style token on unsafe methods. The
// token rides a form field (plain forms) or the X-CSRF-Token header (htmx and
// fetch), and is checked against the one derived from the session cookie.
func (s *Server) csrfMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := auth.Token(r)
		if raw != "" {
			r = withCSRFToken(r, csrfToken(raw))
		}
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
			next.ServeHTTP(w, r)
			return
		}
		if csrfExempt(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		want := csrfFrom(r.Context())
		got := r.Header.Get("X-CSRF-Token")
		if got == "" {
			got = r.FormValue("csrf_token")
		}
		if want == "" || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			http.Error(w, "invalid csrf token", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
