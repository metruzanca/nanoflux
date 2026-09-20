package httpapi

import (
	"github.com/charmbracelet/log"
	"net/http"
	"strings"

	"github.com/metruzanca/nanoflux/internal/auth"
	"github.com/metruzanca/nanoflux/internal/web"
)

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	if _, err := s.auth.User(r); err == nil {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	web.Render(w, "login", web.Page{Title: "log in"})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	username := r.FormValue("username")
	password := r.FormValue("password")

	u, err := s.store.Users.ByUsername(username)
	if err != nil || !auth.CheckPassword(u.PasswordHash, password) {
		w.WriteHeader(http.StatusUnauthorized)
		web.Render(w, "login", web.Page{Title: "log in", Data: "invalid username or password"})
		return
	}

	token, err := s.auth.CreateSession(u.ID)
	if err != nil {
		log.Error("create session", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.auth.SetCookie(w, token)
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if token := auth.Token(r); token != "" {
		if err := s.store.Sessions.Delete(token); err != nil {
			log.Error("delete session", "err", err)
		}
	}
	s.auth.ClearCookie(w)
	http.Redirect(w, r, "/login", http.StatusFound)
}

func (s *Server) signupPage(w http.ResponseWriter, r *http.Request) {
	if _, err := s.auth.User(r); err == nil {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	web.Render(w, "signup", web.Page{Title: "create account"})
}

func (s *Server) signup(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")

	renderErr := func(msg string) {
		w.WriteHeader(http.StatusBadRequest)
		web.Render(w, "signup", web.Page{Title: "create account", Data: msg})
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

	token, err := s.auth.CreateSession(u.ID)
	if err != nil {
		log.Error("create session", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.auth.SetCookie(w, token)
	http.Redirect(w, r, "/", http.StatusFound)
}
