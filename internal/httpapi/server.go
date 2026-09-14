package httpapi

import (
	"net/http"

	"github.com/metruzanca/rss/internal/auth"
	"github.com/metruzanca/rss/internal/config"
	"github.com/metruzanca/rss/internal/discover"
	"github.com/metruzanca/rss/internal/poller"
	"github.com/metruzanca/rss/internal/store"
	"github.com/metruzanca/rss/internal/web"
)

// Server wires the HTTP layer over the store. JSON API routes for the
// extension live here too (see api.go).
type Server struct {
	store      *store.Store
	auth       *auth.Authenticator
	cfg        config.Config
	poller     *poller.Poller
	discoverer *discover.Discoverer
}

func New(st *store.Store, a *auth.Authenticator, cfg config.Config) *Server {
	return &Server{
		store:      st,
		auth:       a,
		cfg:        cfg,
		discoverer: discover.New(nil),
	}
}

// SetPoller attaches the feed poller (needed for manual refresh).
func (s *Server) SetPoller(p *poller.Poller) { s.poller = p }

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	mux.Handle("GET /static/", web.Static())

	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("POST /login", s.login)
	mux.Handle("POST /logout", s.auth.Require(http.HandlerFunc(s.logout)))

	// Items.
	mux.Handle("GET /{$}", s.auth.Require(http.HandlerFunc(s.home)))
	mux.Handle("POST /items/read-all", s.auth.Require(http.HandlerFunc(s.itemsReadAll)))
	mux.Handle("POST /items/{id}/read", s.auth.Require(http.HandlerFunc(s.itemRead)))

	// Feeds.
	mux.Handle("GET /feeds", s.auth.Require(http.HandlerFunc(s.feeds)))
	mux.Handle("POST /feeds", s.auth.Require(http.HandlerFunc(s.feedCreate)))
	mux.Handle("GET /feeds/{id}/edit", s.auth.Require(http.HandlerFunc(s.feedEdit)))
	mux.Handle("POST /feeds/{id}/edit", s.auth.Require(http.HandlerFunc(s.feedUpdate)))
	mux.Handle("POST /feeds/{id}/delete", s.auth.Require(http.HandlerFunc(s.feedDelete)))
	mux.Handle("POST /feeds/{id}/refresh", s.auth.Require(http.HandlerFunc(s.feedRefresh)))

	// Authors.
	mux.Handle("GET /authors", s.auth.Require(http.HandlerFunc(s.authors)))
	mux.Handle("POST /authors", s.auth.Require(http.HandlerFunc(s.authorCreate)))
	mux.Handle("GET /authors/{id}", s.auth.Require(http.HandlerFunc(s.authorPage)))
	mux.Handle("GET /authors/{id}/edit", s.auth.Require(http.HandlerFunc(s.authorEdit)))
	mux.Handle("POST /authors/{id}/edit", s.auth.Require(http.HandlerFunc(s.authorUpdate)))
	mux.Handle("POST /authors/{id}/delete", s.auth.Require(http.HandlerFunc(s.authorDelete)))

	// Collections.
	mux.Handle("GET /collections", s.auth.Require(http.HandlerFunc(s.collections)))
	mux.Handle("POST /collections", s.auth.Require(http.HandlerFunc(s.collectionCreate)))
	mux.Handle("GET /collections/{id}", s.auth.Require(http.HandlerFunc(s.collectionPage)))
	mux.Handle("POST /collections/{id}/delete", s.auth.Require(http.HandlerFunc(s.collectionDelete)))
	mux.Handle("POST /collections/{id}/add-feed", s.auth.Require(http.HandlerFunc(s.collectionAddFeed)))
	mux.Handle("POST /collections/{id}/remove-feed/{feed_id}", s.auth.Require(http.HandlerFunc(s.collectionRemoveFeed)))

	// htmx fragments.
	mux.Handle("GET /fragments/author-form", s.auth.Require(http.HandlerFunc(s.authorFormFragment)))

	// JSON API (for the browser extension).
	mux.HandleFunc("POST /api/login", s.apiLogin)
	mux.Handle("GET /api/unread-count", s.auth.Require(http.HandlerFunc(s.apiUnreadCount)))
	mux.Handle("GET /api/items", s.auth.Require(http.HandlerFunc(s.apiItems)))
	mux.Handle("POST /api/items/{id}/read", s.auth.Require(http.HandlerFunc(s.apiItemRead)))
	mux.Handle("POST /api/discover", s.auth.Require(http.HandlerFunc(s.apiDiscover)))
	mux.Handle("POST /api/save", s.auth.Require(http.HandlerFunc(s.apiSave)))

	return cors(mux)
}

// cors answers the browser extension's cross-origin preflights. Auth relies
// on a Bearer token (never cookies cross-site), so allowing any origin is safe.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
