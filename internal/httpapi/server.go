package httpapi

import (
	"net/http"
	"time"

	"github.com/metruzanca/nanoflux/internal/auth"
	"github.com/metruzanca/nanoflux/internal/config"
	"github.com/metruzanca/nanoflux/internal/discover"
	"github.com/metruzanca/nanoflux/internal/filestore"
	"github.com/metruzanca/nanoflux/internal/oembed"
	"github.com/metruzanca/nanoflux/internal/poller"
	"github.com/metruzanca/nanoflux/internal/store"
	"github.com/metruzanca/nanoflux/internal/web"
)

// Server wires the HTTP layer over the store. JSON API routes for the
// extension live here too (see api.go).
type Server struct {
	store      *store.Store
	auth       *auth.Authenticator
	cfg        config.Config
	poller     *poller.Poller
	discoverer *discover.Discoverer
	client     *http.Client
	files      filestore.Store
	oembed     *oembed.Resolver
	reddit     *redditCache
}

func New(st *store.Store, a *auth.Authenticator, cfg config.Config, fs filestore.Store) *Server {
	client := &http.Client{Timeout: 20 * time.Second}
	return &Server{
		store:      st,
		auth:       a,
		cfg:        cfg,
		discoverer: discover.New(nil),
		client:     client,
		files:      fs,
		oembed:     oembed.New(client, 5*time.Minute, 30*time.Second, 2000),
		reddit:     newRedditCache(),
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
	mux.HandleFunc("GET /signup", s.signupPage)
	mux.HandleFunc("POST /signup", s.signup)
	mux.Handle("POST /logout", s.auth.Require(http.HandlerFunc(s.logout)))

	// Items.
	mux.Handle("GET /{$}", s.auth.Require(http.HandlerFunc(s.home)))
	mux.Handle("GET /items", s.auth.Require(http.HandlerFunc(s.itemsFragment)))
	mux.Handle("GET /search", s.auth.Require(http.HandlerFunc(s.searchPage)))
	mux.Handle("GET /read", s.auth.Require(http.HandlerFunc(s.readPage)))
	mux.Handle("GET /favorites", s.auth.Require(http.HandlerFunc(s.favoritesPage)))
	mux.Handle("POST /items/read-all", s.auth.Require(http.HandlerFunc(s.itemsReadAll)))
	mux.Handle("POST /items/unread-all", s.auth.Require(http.HandlerFunc(s.itemsMarkAllUnread)))
	mux.Handle("POST /items/{id}/read", s.auth.Require(http.HandlerFunc(s.itemRead)))
	mux.Handle("POST /items/{id}/favorite", s.auth.Require(http.HandlerFunc(s.itemFavorite)))
	mux.Handle("GET /items/{id}/view", s.auth.Require(http.HandlerFunc(s.itemView)))
	mux.Handle("POST /items/{id}/share", s.auth.Require(http.HandlerFunc(s.itemShare)))
	mux.Handle("POST /items/{id}/revoke", s.auth.Require(http.HandlerFunc(s.itemRevokeShare)))
	mux.HandleFunc("GET /shared/{token}", s.sharedPage)

	// Feeds.
	mux.Handle("GET /feeds", s.auth.Require(http.HandlerFunc(s.feeds)))
	mux.Handle("POST /feeds", s.auth.Require(http.HandlerFunc(s.feedCreate)))
	mux.Handle("GET /feeds/{id}", s.auth.Require(http.HandlerFunc(s.feedPage)))
	mux.Handle("GET /feeds/{id}/items", s.auth.Require(http.HandlerFunc(s.feedItems)))
	mux.Handle("GET /feeds/{id}/edit", s.auth.Require(http.HandlerFunc(s.feedEdit)))
	mux.Handle("POST /feeds/{id}/edit", s.auth.Require(http.HandlerFunc(s.feedUpdate)))
	mux.Handle("POST /feeds/{id}/delete", s.auth.Require(http.HandlerFunc(s.feedDelete)))
	mux.Handle("POST /feeds/{id}/refresh", s.auth.Require(http.HandlerFunc(s.feedRefresh)))
	mux.Handle("POST /feeds/{id}/toggle", s.auth.Require(http.HandlerFunc(s.feedToggle)))
	mux.Handle("POST /feeds/{id}/filters", s.auth.Require(http.HandlerFunc(s.feedRuleCreate)))
	mux.Handle("POST /filters/{id}/delete", s.auth.Require(http.HandlerFunc(s.filterDelete)))

	// Authors.
	mux.Handle("GET /authors", s.auth.Require(http.HandlerFunc(s.authors)))
	mux.Handle("POST /authors", s.auth.Require(http.HandlerFunc(s.authorCreate)))
	mux.Handle("GET /authors/{id}", s.auth.Require(http.HandlerFunc(s.authorPage)))
	mux.Handle("GET /authors/{id}/items", s.auth.Require(http.HandlerFunc(s.authorItems)))
	mux.Handle("GET /authors/{id}/edit", s.auth.Require(http.HandlerFunc(s.authorEdit)))
	mux.Handle("POST /authors/{id}/edit", s.auth.Require(http.HandlerFunc(s.authorUpdate)))
	mux.Handle("POST /authors/{id}/delete", s.auth.Require(http.HandlerFunc(s.authorDelete)))

	// Collections.
	mux.Handle("GET /collections", s.auth.Require(http.HandlerFunc(s.collections)))
	mux.Handle("POST /collections", s.auth.Require(http.HandlerFunc(s.collectionCreate)))
	mux.Handle("GET /collections/{id}", s.auth.Require(http.HandlerFunc(s.collectionPage)))
	mux.Handle("GET /collections/{id}/items", s.auth.Require(http.HandlerFunc(s.collectionItems)))
	mux.Handle("POST /collections/{id}/delete", s.auth.Require(http.HandlerFunc(s.collectionDelete)))
	mux.Handle("POST /collections/{id}/add-feed", s.auth.Require(http.HandlerFunc(s.collectionAddFeed)))
	mux.Handle("POST /collections/{id}/remove-feed/{feed_id}", s.auth.Require(http.HandlerFunc(s.collectionRemoveFeed)))

	// htmx fragments.
	mux.Handle("GET /fragments/author-form", s.auth.Require(http.HandlerFunc(s.authorFormFragment)))
	mux.Handle("POST /fragments/feed-preview", s.auth.Require(http.HandlerFunc(s.feedPreview)))
	mux.Handle("POST /fragments/author-preview", s.auth.Require(http.HandlerFunc(s.authorPreview)))

	// Image proxy for avatars.
	mux.Handle("GET /img", s.auth.Require(http.HandlerFunc(s.imgProxy)))

	// Settings.
	mux.Handle("GET /settings", s.auth.Require(http.HandlerFunc(s.settingsPage)))
	mux.Handle("POST /settings/avatar", s.auth.Require(http.HandlerFunc(s.settingsAvatar)))
	mux.Handle("POST /settings/timezone", s.auth.Require(http.HandlerFunc(s.settingsTimezone)))
	mux.Handle("POST /settings/theme", s.auth.Require(http.HandlerFunc(s.settingsTheme)))
	mux.Handle("POST /settings/accent", s.auth.Require(http.HandlerFunc(s.settingsAccent)))
	mux.Handle("GET /settings/export.opml", s.auth.Require(http.HandlerFunc(s.opmlExport)))
	mux.Handle("POST /settings/opml", s.auth.Require(http.HandlerFunc(s.opmlImport)))
	mux.Handle("GET /avatar", s.auth.Require(http.HandlerFunc(s.avatarImage)))
	mux.Handle("POST /settings/icons", s.auth.Require(http.HandlerFunc(s.settingsIconAdd)))
	mux.Handle("POST /settings/icons/{id}/refresh", s.auth.Require(http.HandlerFunc(s.settingsIconRefresh)))
	mux.Handle("POST /settings/icons/{id}/delete", s.auth.Require(http.HandlerFunc(s.settingsIconDelete)))
	mux.Handle("GET /icons/{domain}", s.auth.Require(http.HandlerFunc(s.serveSourceIcon)))

	// JSON API (for the browser extension).
	mux.HandleFunc("POST /api/login", s.apiLogin)
	mux.Handle("GET /api/unread-count", s.auth.Require(http.HandlerFunc(s.apiUnreadCount)))
	mux.Handle("GET /api/items", s.auth.Require(http.HandlerFunc(s.apiItems)))
	mux.Handle("GET /api/search", s.auth.Require(http.HandlerFunc(s.apiSearch)))
	mux.Handle("POST /api/items/{id}/read", s.auth.Require(http.HandlerFunc(s.apiItemRead)))
	mux.Handle("POST /api/discover", s.auth.Require(http.HandlerFunc(s.apiDiscover)))
	mux.Handle("POST /api/save", s.auth.Require(http.HandlerFunc(s.apiSave)))

	return logRequests(privacyHeaders(cors(mux)))
}

// privacyHeaders prevents referrer leakage on every response.
func privacyHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
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
