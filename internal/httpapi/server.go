package httpapi

import (
	"net/http"

	"github.com/metruzanca/rss/internal/auth"
	"github.com/metruzanca/rss/internal/config"
	"github.com/metruzanca/rss/internal/store"
	"github.com/metruzanca/rss/internal/web"
)

// Server wires the HTTP layer over the store. JSON API routes for the
// extension live here too (see api.go).
type Server struct {
	store *store.Store
	auth  *auth.Authenticator
	cfg   config.Config
}

func New(st *store.Store, a *auth.Authenticator, cfg config.Config) *Server {
	return &Server{store: st, auth: a, cfg: cfg}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	mux.Handle("GET /static/", web.Static())

	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("POST /login", s.login)
	mux.Handle("POST /logout", s.auth.Require(http.HandlerFunc(s.logout)))

	mux.Handle("GET /{$}", s.auth.Require(http.HandlerFunc(s.home)))

	return mux
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	unread, _ := s.store.Items.CountUnread(u.ID)
	web.Render(w, "home", web.Page{Title: "home", User: u, Any: unread})
}
