package httpapi

import (
	"net/http"

	"github.com/metruzanca/nanoflux/internal/auth"
	"github.com/metruzanca/nanoflux/internal/web"
)

// landingData is the template input for the public landing page.
type landingData struct {
	AllowSignup bool // offer account creation when signup is open
}

// root serves the exact "/" route. A signed-in user gets their normal home; an
// anonymous visitor gets the public landing page. There is no redirect to
// /login, so the app's root doubles as its marketing page.
func (s *Server) root(w http.ResponseWriter, r *http.Request) {
	// The nav-counts middleware runs on GET / and already resolves the session
	// for signed-in users; only fall back to a store lookup for anonymous ones.
	if _, ok := auth.UserFrom(r); ok {
		s.home(w, r)
		return
	}
	if u, err := s.auth.User(r); err == nil {
		s.home(w, auth.WithUser(r, u))
		return
	}
	s.landing(w, r)
}

// landing renders the public landing page. Its primary call to action points at
// login and, when signups are open, account creation.
func (s *Server) landing(w http.ResponseWriter, r *http.Request) {
	// An already-valid session shouldn't sit on the marketing page.
	if _, ok := auth.UserFrom(r); ok {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if _, err := s.auth.User(r); err == nil {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	web.Render(w, r, landingPage(landingData{AllowSignup: s.allowSignup()}))
}
