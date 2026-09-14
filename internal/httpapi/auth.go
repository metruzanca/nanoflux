package httpapi

import (
	"log"
	"net/http"

	"github.com/metruzanca/rss/internal/auth"
	"github.com/metruzanca/rss/internal/web"
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
		log.Printf("create session: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.auth.SetCookie(w, token)
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if token := auth.Token(r); token != "" {
		if err := s.store.Sessions.Delete(token); err != nil {
			log.Printf("delete session: %v", err)
		}
	}
	s.auth.ClearCookie(w)
	http.Redirect(w, r, "/login", http.StatusFound)
}
