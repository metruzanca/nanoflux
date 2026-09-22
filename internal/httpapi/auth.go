package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/charmbracelet/log"

	"github.com/metruzanca/nanoflux/internal/auth"
	"github.com/metruzanca/nanoflux/internal/store"
	"github.com/metruzanca/nanoflux/internal/web"
)

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	next := safeNext(r.URL.Query().Get("next"))
	if _, err := s.auth.User(r); err == nil {
		http.Redirect(w, r, redirectTarget(next), http.StatusFound)
		return
	}
	web.Render(w, r, basePage("log in", store.User{}, loginPage("", s.allowSignup(), next)))
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	next := safeNext(r.FormValue("next"))
	ip := clientIP(r)
	if s.loginLimiter.blocked(ip) {
		w.WriteHeader(http.StatusTooManyRequests)
		web.Render(w, r, basePage("log in", store.User{}, loginPage("too many attempts — try again later", s.allowSignup(), next)))
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")

	u, err := s.store.Users.ByUsername(username)
	if err != nil || !auth.CheckPassword(u.PasswordHash, password) {
		s.loginLimiter.fail(ip)
		w.WriteHeader(http.StatusUnauthorized)
		web.Render(w, r, basePage("log in", store.User{}, loginPage("invalid username or password", s.allowSignup(), next)))
		return
	}
	s.loginLimiter.success(ip)

	token, err := s.auth.CreateSession(u.ID)
	if err != nil {
		log.Error("create session", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.auth.SetCookie(w, r, token)
	http.Redirect(w, r, redirectTarget(next), http.StatusFound)
}

// safeNext accepts only same-site relative paths so a crafted ?next= can't
// redirect off-site. It returns "" for anything unsafe (including "//host").
func safeNext(next string) string {
	if strings.HasPrefix(next, "/") && !strings.HasPrefix(next, "//") && !strings.Contains(next, "\\") {
		return next
	}
	return ""
}

// redirectTarget returns next when set, else the unread home.
func redirectTarget(next string) string {
	if next != "" {
		return next
	}
	return "/"
}

// allowSignup reports the global signup setting, defaulting to open when the
// setting cannot be read so an error never locks everyone out.
func (s *Server) allowSignup() bool {
	allow, err := s.store.Settings.AllowSignup()
	if err != nil {
		log.Error("read allow_signup", "err", err)
		return true
	}
	return allow
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if token := auth.Token(r); token != "" {
		if err := s.store.Sessions.Delete(token); err != nil {
			log.Error("delete session", "err", err)
		}
	}
	s.auth.ClearCookie(w, r)
	http.Redirect(w, r, "/login", http.StatusFound)
}

func (s *Server) signupPage(w http.ResponseWriter, r *http.Request) {
	if _, err := s.auth.User(r); err == nil {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if !s.allowSignup() {
		web.Render(w, r, basePage("create account", store.User{}, signupDisabledPage()))
		return
	}
	web.Render(w, r, basePage("create account", store.User{}, signupPage("")))
}

func (s *Server) signup(w http.ResponseWriter, r *http.Request) {
	if !s.allowSignup() {
		w.WriteHeader(http.StatusForbidden)
		web.Render(w, r, basePage("create account", store.User{}, signupDisabledPage()))
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")

	renderErr := func(msg string) {
		w.WriteHeader(http.StatusBadRequest)
		web.Render(w, r, basePage("create account", store.User{}, signupPage(msg)))
	}

	switch {
	case username == "":
		renderErr("username is required")
		return
	case len(password) < 8:
		renderErr("password must be at least 8 characters")
		return
	}
	if _, err := s.store.Users.ByUsername(username); err == nil {
		renderErr("that username is taken")
		return
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		log.Error("hash password", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	u, err := s.store.Users.Create(username, hash)
	if err != nil {
		log.Error("create user", "err", err)
		renderErr("that username is taken")
		return
	}

	// Seed the new account's timezone from the browser (an IANA name submitted
	// as a hidden field). An unknown name is ignored rather than rejecting the
	// signup: the server default is a safe fallback and the user can fix it on
	// /settings.
	if tz := strings.TrimSpace(r.FormValue("timezone")); tz != "" {
		if _, err := time.LoadLocation(tz); err == nil {
			if err := s.store.Users.SetTimezone(u.ID, tz); err != nil {
				log.Error("set signup timezone", "err", err)
			} else {
				u.Timezone = tz
			}
		}
	}

	token, err := s.auth.CreateSession(u.ID)
	if err != nil {
		log.Error("create session", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.auth.SetCookie(w, r, token)
	http.Redirect(w, r, "/", http.StatusFound)
}
