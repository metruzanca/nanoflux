package httpapi

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/metruzanca/nanoflux/internal/auth"
	"github.com/metruzanca/nanoflux/internal/backup"
	"github.com/metruzanca/nanoflux/internal/config"
	"github.com/metruzanca/nanoflux/internal/demo"
	"github.com/metruzanca/nanoflux/internal/discover"
	"github.com/metruzanca/nanoflux/internal/filestore"
	"github.com/metruzanca/nanoflux/internal/oembed"
	"github.com/metruzanca/nanoflux/internal/plugin"
	"github.com/metruzanca/nanoflux/internal/poller"
	"github.com/metruzanca/nanoflux/internal/safedial"
	"github.com/metruzanca/nanoflux/internal/store"
	"github.com/metruzanca/nanoflux/internal/web"
)

// Server wires the HTTP layer over the store. JSON API routes for the
// extension live here too (see api.go).
type Server struct {
	store        *store.Store
	auth         *auth.Authenticator
	cfg          config.Config
	poller       *poller.Poller
	backups      *backup.Runner
	plugins      *plugin.Registry
	pluginHosts  *plugin.Hosts
	discoverer   *discover.Discoverer
	client       *http.Client
	files        filestore.Store
	oembed       *oembed.Resolver
	loginLimiter *loginLimiter
	demo         *demo.Manager
	demoThrottle *demoThrottle
}

func New(st *store.Store, a *auth.Authenticator, cfg config.Config, fs filestore.Store) *Server {
	client := safedial.Client(20 * time.Second)
	return &Server{
		store:        st,
		auth:         a,
		cfg:          cfg,
		discoverer:   discover.New(client),
		client:       client,
		files:        fs,
		oembed:       oembed.New(client, 5*time.Minute, 30*time.Second, 2000),
		loginLimiter: newLoginLimiter(),
		demoThrottle: newDemoThrottle(),
	}
}

// SetPoller attaches the feed poller (needed for manual refresh).
func (s *Server) SetPoller(p *poller.Poller) { s.poller = p }

// SetBackupRunner attaches the automatic-backup runner (nil when disabled), so
// the admin page can show status and trigger a run on demand.
func (s *Server) SetBackupRunner(r *backup.Runner) { s.backups = r }

// SetPlugins attaches the plugin registry and host factory, enabling plugin
// discovery in the add-feed flow. Either may be nil (no plugins).
func (s *Server) SetPlugins(reg *plugin.Registry, hosts *plugin.Hosts) {
	s.plugins = reg
	s.pluginHosts = hosts
	// Let a plugin opt its image hosts out of the /img proxy (a site that
	// hotlinks freely but blocks the proxy's server-side request).
	if reg != nil {
		web.SetProxyBypass(reg.BypassesProxy)
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	mux.Handle("GET /static/", web.Static())

	// PWA: the manifest and service worker are served at the root so the
	// worker's scope is "/" and the manifest URL is stable. The service worker
	// is deliberately not HTTP-cached so the browser revalidates it.
	mux.HandleFunc("GET /manifest.webmanifest", func(w http.ResponseWriter, r *http.Request) {
		serveEmbedded(w, r, "manifest.webmanifest", "application/manifest+json; charset=utf-8", "public, max-age=86400")
	})
	mux.HandleFunc("GET /sw.js", func(w http.ResponseWriter, r *http.Request) {
		serveEmbedded(w, r, "sw.js", "application/javascript; charset=utf-8", "no-cache")
	})

	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc("GET /signup", s.signupPage)
	mux.HandleFunc("POST /signup", s.signup)
	mux.Handle("POST /logout", s.auth.Require(http.HandlerFunc(s.logout)))

	// Public landing page for anonymous visitors; demo mode adds the CTA that
	// provisions an ephemeral account.
	mux.HandleFunc("POST /demo", s.demoStart)

	// Items.
	mux.HandleFunc("GET /{$}", s.root)
	mux.Handle("GET /unread", s.auth.Require(http.HandlerFunc(s.unread)))
	mux.Handle("GET /items", s.auth.Require(http.HandlerFunc(s.itemsFragment)))
	mux.Handle("GET /search", s.auth.Require(http.HandlerFunc(s.searchPage)))
	mux.Handle("GET /read", s.auth.Require(http.HandlerFunc(s.readPage)))
	mux.Handle("GET /favorites", s.auth.Require(http.HandlerFunc(s.favoritesPage)))
	mux.Handle("GET /favorites/algorithm", s.auth.Require(http.HandlerFunc(s.favoritesAlgorithmPage)))
	mux.Handle("GET /bookmarks", s.auth.Require(http.HandlerFunc(s.bookmarksPage)))
	mux.Handle("POST /items/read-all", s.auth.Require(http.HandlerFunc(s.itemsReadAll)))
	mux.Handle("POST /items/unread-all", s.auth.Require(http.HandlerFunc(s.itemsMarkAllUnread)))
	mux.Handle("POST /prefs/display", s.auth.Require(http.HandlerFunc(s.displayPrefSet)))
	mux.Handle("POST /items/{id}/read", s.auth.Require(http.HandlerFunc(s.itemRead)))
	mux.Handle("POST /items/{id}/unread", s.auth.Require(http.HandlerFunc(s.itemUnread)))
	mux.Handle("POST /items/{id}/read-before", s.auth.Require(http.HandlerFunc(s.itemReadBefore)))
	mux.Handle("POST /items/{id}/read-after", s.auth.Require(http.HandlerFunc(s.itemReadAfter)))
	mux.Handle("POST /items/{id}/favorite", s.auth.Require(http.HandlerFunc(s.itemFavorite)))
	mux.Handle("POST /items/{id}/bookmark", s.auth.Require(http.HandlerFunc(s.itemBookmark)))
	mux.Handle("POST /items/{id}/delete", s.auth.Require(http.HandlerFunc(s.itemDeleteSaved)))
	mux.Handle("GET /items/{id}/view", s.auth.Require(http.HandlerFunc(s.itemView)))
	mux.Handle("POST /items/{id}/share", s.auth.Require(http.HandlerFunc(s.itemShare)))
	mux.Handle("POST /items/{id}/revoke", s.auth.Require(http.HandlerFunc(s.itemRevokeShare)))
	mux.HandleFunc("GET /shared/{token}", s.sharedPage)

	// PWA share target: the OS share sheet opens GET /add?url=…
	mux.Handle("GET /add", s.auth.Require(http.HandlerFunc(s.shareAdd)))

	// Feeds (always viewed from an author).
	mux.Handle("POST /feeds", s.auth.Require(http.HandlerFunc(s.feedCreate)))
	mux.Handle("GET /feeds/{id}", s.auth.Require(http.HandlerFunc(s.feedPage)))
	mux.Handle("GET /feeds/{id}/items", s.auth.Require(http.HandlerFunc(s.feedItems)))
	mux.Handle("GET /feeds/{id}/edit", s.auth.Require(http.HandlerFunc(s.feedEdit)))
	mux.Handle("POST /feeds/{id}/edit", s.auth.Require(http.HandlerFunc(s.feedUpdate)))
	mux.Handle("POST /feeds/{id}/delete", s.auth.Require(http.HandlerFunc(s.feedDelete)))
	mux.Handle("POST /feeds/{id}/refresh", s.auth.Require(http.HandlerFunc(s.feedRefresh)))
	mux.Handle("POST /feeds/{id}/read-all", s.auth.Require(http.HandlerFunc(s.feedReadAll)))
	mux.Handle("POST /feeds/{id}/older", s.auth.Require(http.HandlerFunc(s.feedOlder)))
	mux.Handle("POST /feeds/{id}/toggle", s.auth.Require(http.HandlerFunc(s.feedToggle)))
	mux.Handle("POST /feeds/{id}/rank", s.auth.Require(http.HandlerFunc(s.feedRank)))

	// Authors.
	mux.Handle("GET /authors", s.auth.Require(http.HandlerFunc(s.authors)))
	mux.Handle("POST /authors", s.auth.Require(http.HandlerFunc(s.authorCreate)))
	mux.Handle("POST /authors/{id}/feeds", s.auth.Require(http.HandlerFunc(s.authorFeedCreate)))
	mux.Handle("GET /authors/{id}", s.auth.Require(http.HandlerFunc(s.authorPage)))
	mux.Handle("GET /authors/{id}/items", s.auth.Require(http.HandlerFunc(s.authorItems)))
	mux.Handle("POST /authors/{id}/read-all", s.auth.Require(http.HandlerFunc(s.authorReadAll)))
	mux.Handle("GET /authors/{id}/edit", s.auth.Require(http.HandlerFunc(s.authorEdit)))
	mux.Handle("POST /authors/{id}/edit", s.auth.Require(http.HandlerFunc(s.authorUpdate)))
	mux.Handle("POST /authors/{id}/delete", s.auth.Require(http.HandlerFunc(s.authorDelete)))
	mux.Handle("GET /authors/{id}/avatar", s.auth.Require(http.HandlerFunc(s.authorAvatar)))
	mux.Handle("POST /authors/{id}/avatar-refresh", s.auth.Require(http.HandlerFunc(s.authorAvatarRefresh)))

	// Collections.
	mux.Handle("GET /collections", s.auth.Require(http.HandlerFunc(s.collections)))
	mux.Handle("POST /collections", s.auth.Require(http.HandlerFunc(s.collectionCreate)))
	mux.Handle("GET /collections/{id}", s.auth.Require(http.HandlerFunc(s.collectionPage)))
	mux.Handle("GET /collections/{id}/edit", s.auth.Require(http.HandlerFunc(s.collectionEdit)))
	mux.Handle("POST /collections/{id}/edit", s.auth.Require(http.HandlerFunc(s.collectionUpdate)))
	mux.Handle("GET /collections/{id}/items", s.auth.Require(http.HandlerFunc(s.collectionItems)))
	mux.Handle("POST /collections/{id}/delete", s.auth.Require(http.HandlerFunc(s.collectionDelete)))
	mux.Handle("POST /collections/{id}/add-feed", s.auth.Require(http.HandlerFunc(s.collectionAddFeed)))
	mux.Handle("POST /collections/{id}/remove-feed/{feed_id}", s.auth.Require(http.HandlerFunc(s.collectionRemoveFeed)))

	// Lists (user-defined sets of items; favorites is the special list).
	mux.Handle("GET /lists", s.auth.Require(http.HandlerFunc(s.listsPage)))
	mux.Handle("POST /lists", s.auth.Require(http.HandlerFunc(s.listCreate)))
	mux.Handle("GET /lists/{id}", s.auth.Require(http.HandlerFunc(s.listPage)))
	mux.Handle("GET /lists/{id}/edit", s.auth.Require(http.HandlerFunc(s.listEdit)))
	mux.Handle("POST /lists/{id}/edit", s.auth.Require(http.HandlerFunc(s.listUpdate)))
	mux.Handle("GET /lists/{id}/items", s.auth.Require(http.HandlerFunc(s.listItems)))
	mux.Handle("POST /lists/{id}/delete", s.auth.Require(http.HandlerFunc(s.listDelete)))
	mux.Handle("POST /lists/{id}/share", s.auth.Require(http.HandlerFunc(s.listShare)))
	mux.Handle("POST /lists/{id}/revoke", s.auth.Require(http.HandlerFunc(s.listRevoke)))
	mux.Handle("GET /items/{id}/lists", s.auth.Require(http.HandlerFunc(s.itemLists)))
	mux.Handle("POST /items/{id}/lists", s.auth.Require(http.HandlerFunc(s.itemListsUpdate)))
	mux.Handle("POST /favorites/share", s.auth.Require(http.HandlerFunc(s.favoritesShare)))
	mux.Handle("POST /favorites/revoke", s.auth.Require(http.HandlerFunc(s.favoritesRevoke)))
	mux.Handle("POST /bookmarks/share", s.auth.Require(http.HandlerFunc(s.bookmarksShare)))
	mux.Handle("POST /bookmarks/revoke", s.auth.Require(http.HandlerFunc(s.bookmarksRevoke)))
	mux.HandleFunc("GET /l/{token}", s.sharedListPage)
	mux.HandleFunc("GET /f/{token}", s.sharedFavoritesPage)
	mux.HandleFunc("GET /b/{token}", s.sharedBookmarksPage)

	// htmx fragments.
	mux.Handle("GET /fragments/author-form", s.auth.Require(http.HandlerFunc(s.authorFormFragment)))
	mux.Handle("POST /fragments/feed-preview", s.auth.Require(http.HandlerFunc(s.feedPreview)))
	mux.Handle("POST /fragments/manual-feed", s.auth.Require(http.HandlerFunc(s.manualFeedForm)))
	mux.Handle("GET /fragments/save-page", s.auth.Require(http.HandlerFunc(s.savePageFormFragment)))
	// Provisioning: create a feed on a remote service (a newsletter inbox) from
	// the nav add menu, without leaving nanoflux.
	mux.Handle("GET /fragments/provision-form", s.auth.Require(http.HandlerFunc(s.provisionFormFragment)))
	mux.Handle("POST /feeds/provision", s.auth.Require(http.HandlerFunc(s.feedProvision)))
	// Remote management (settings, delete) for a plugin-owned feed.
	mux.Handle("POST /feeds/{id}/plugin-admin", s.auth.Require(http.HandlerFunc(s.pluginAdminAction)))
	// Plugin docs are available to every signed-in user: the feed edit page
	// offers them next to the filter rules, not just the admin plugin card.
	mux.Handle("GET /fragments/plugin-docs", s.auth.Require(http.HandlerFunc(s.pluginDocsFragment)))

	// Saved pages (in-app "save url for later").
	mux.Handle("POST /save-page", s.auth.Require(http.HandlerFunc(s.savePageWeb)))

	// Image proxy for avatars.
	mux.Handle("GET /img", s.auth.Require(http.HandlerFunc(s.imgProxy)))
	mux.Handle("GET /cache/{key...}", s.auth.Require(http.HandlerFunc(s.cacheImage)))

	// Brand logo, color-addressed so it caches immutably per accent.
	mux.Handle("GET /logo.svg", http.HandlerFunc(s.serveLogo))

	// Favicon: the same brand mark in the built-in accent.
	mux.Handle("GET /favicon.svg", http.HandlerFunc(s.serveFavicon))

	// Admin.
	mux.Handle("GET /admin", s.auth.Require(s.adminOnly(http.HandlerFunc(s.adminPage))))
	mux.Handle("POST /admin/users/{id}/reset-password", s.auth.Require(s.adminOnly(http.HandlerFunc(s.adminResetPassword))))
	mux.Handle("POST /admin/users/{id}/set-admin", s.auth.Require(s.adminOnly(http.HandlerFunc(s.adminSetAdmin))))
	mux.Handle("POST /admin/users/{id}/delete", s.auth.Require(s.adminOnly(http.HandlerFunc(s.adminDeleteUser))))
	mux.Handle("POST /admin/settings/signup", s.auth.Require(s.adminOnly(http.HandlerFunc(s.adminSetSignup))))
	mux.Handle("POST /admin/settings/signup-banner-dismiss", s.auth.Require(s.adminOnly(http.HandlerFunc(s.adminDismissSignupBanner))))
	mux.Handle("POST /admin/backup", s.auth.Require(s.adminOnly(http.HandlerFunc(s.adminBackupNow))))
	mux.Handle("POST /admin/plugins/reset", s.auth.Require(s.adminOnly(http.HandlerFunc(s.adminResetPluginDomain))))
	mux.Handle("POST /admin/plugins/{name}/settings", s.auth.Require(s.adminOnly(http.HandlerFunc(s.adminSavePluginSettings))))

	// Settings.
	mux.Handle("GET /settings", s.auth.Require(http.HandlerFunc(s.settingsPage)))
	mux.Handle("POST /settings/avatar", s.auth.Require(http.HandlerFunc(s.settingsAvatar)))
	mux.Handle("POST /settings/timezone", s.auth.Require(http.HandlerFunc(s.settingsTimezone)))
	mux.Handle("POST /settings/home", s.auth.Require(http.HandlerFunc(s.settingsHome)))
	mux.Handle("POST /settings/home/reset", s.auth.Require(http.HandlerFunc(s.settingsHomeReset)))
	mux.Handle("POST /settings/theme", s.auth.Require(http.HandlerFunc(s.settingsTheme)))
	mux.Handle("POST /settings/auto-read", s.auth.Require(http.HandlerFunc(s.settingsAutoRead)))
	mux.Handle("POST /settings/accent", s.auth.Require(http.HandlerFunc(s.settingsAccent)))
	mux.Handle("POST /settings/appearance/unread-counts", s.auth.Require(http.HandlerFunc(s.settingsAppearanceUnreadCounts)))
	mux.Handle("POST /settings/appearance/unread-nav", s.auth.Require(http.HandlerFunc(s.settingsAppearanceUnreadNav)))
	mux.Handle("POST /settings/appearance/grid-columns", s.auth.Require(http.HandlerFunc(s.settingsAppearanceGridColumns)))
	mux.Handle("POST /settings/password", s.auth.Require(http.HandlerFunc(s.settingsPassword)))
	mux.Handle("POST /settings/sessions/{token}/revoke", s.auth.Require(http.HandlerFunc(s.settingsSessionsRevoke)))
	mux.Handle("GET /settings/export.opml", s.auth.Require(http.HandlerFunc(s.opmlExport)))
	mux.Handle("GET /settings/extension.zip", s.auth.Require(http.HandlerFunc(s.settingsExtensionZip)))
	mux.Handle("POST /settings/opml", s.auth.Require(http.HandlerFunc(s.opmlImport)))
	mux.Handle("GET /avatar", s.auth.Require(http.HandlerFunc(s.avatarImage)))
	mux.Handle("GET /icons/{domain}", s.auth.Require(http.HandlerFunc(s.serveSourceIcon)))

	// JSON API (for the browser extension).
	mux.HandleFunc("POST /api/login", s.apiLogin)
	mux.Handle("GET /api/unread-count", s.auth.Require(http.HandlerFunc(s.apiUnreadCount)))
	mux.Handle("GET /api/nav-counts", s.auth.Require(http.HandlerFunc(s.apiNavCounts)))
	mux.Handle("GET /api/items", s.auth.Require(http.HandlerFunc(s.apiItems)))
	mux.Handle("GET /api/search", s.auth.Require(http.HandlerFunc(s.apiSearch)))
	mux.Handle("GET /api/entities", s.auth.Require(http.HandlerFunc(s.apiEntities)))
	mux.Handle("POST /api/items/{id}/read", s.auth.Require(http.HandlerFunc(s.apiItemRead)))
	mux.Handle("POST /api/discover", s.auth.Require(http.HandlerFunc(s.apiDiscover)))
	mux.Handle("POST /api/save", s.auth.Require(http.HandlerFunc(s.apiSave)))

	// Browser-extension HTML fragments (loaded/submitted with htmx).
	mux.Handle("POST /api/ext/feed-form", s.auth.Require(http.HandlerFunc(s.apiExtFeedForm)))
	mux.Handle("POST /api/ext/save", s.auth.Require(http.HandlerFunc(s.apiExtSave)))
	mux.Handle("POST /api/ext/page-form", s.auth.Require(http.HandlerFunc(s.apiExtPageForm)))
	mux.Handle("POST /api/ext/page-save", s.auth.Require(http.HandlerFunc(s.apiExtPageSave)))

	return logRequests(s.securityHeaders(s.csrfMiddleware(cors(s.analyticsMiddleware(s.navCountsMiddleware(s.errorPages(mux)))))))
}

// securityHeaders sets the response headers shared by every route. The CSP is
// deliberately eval- and inline-script-free; a few vendored scripts rely on
// inline <style> injection, so style-src keeps 'unsafe-inline'.
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", s.contentSecurityPolicy())
		// Only send HSTS when the request actually arrived over HTTPS, so a
		// plain-HTTP LAN install is not locked to a scheme it does not serve.
		if auth.SecureRequest(r) {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

// contentSecurityPolicy builds the app's CSP. Analytics origins are added only
// when analytics is enabled; everything else is static.
//
// frame-src is deliberately open to any http(s) origin, like img-src and
// media-src: the app renders third-party player iframes resolved at view time
// (oEmbed providers such as vimeo or imgur, and plugin Render EmbedSrc),
// and those hosts cannot be known ahead of time. Stored feed and plugin HTML
// cannot inject an iframe (sanitize.HTML strips it at the store boundary), so
// the only frames are the ones the core itself emits. 'self' is intentionally
// absent so an embed cannot frame the app's own authenticated pages.
func (s *Server) contentSecurityPolicy() string {
	script := []string{"'self'", "https://cdnjs.cloudflare.com"}
	connect := []string{"'self'", "https:", "http:"}
	style := []string{"'self'", "'unsafe-inline'", "https://cdnjs.cloudflare.com"}
	if s.cfg.Analytics.Enabled() {
		if o := urlOrigin(s.cfg.Analytics.ScriptURL); o != "" {
			script = append(script, o)
			connect = append(connect, o) // umami posts to the script origin by default
		}
		if o := urlOrigin(s.cfg.Analytics.HostURL); o != "" {
			connect = append(connect, o)
		}
	}
	directives := [][2]string{
		{"default-src", "'self'"},
		{"script-src", strings.Join(script, " ")},
		{"style-src", strings.Join(style, " ")},
		{"img-src", "'self' data: blob: https: http:"},
		{"media-src", "'self' blob: https: http:"},
		{"connect-src", strings.Join(connect, " ")},
		{"font-src", "'self' data:"},
		{"frame-src", "https: http:"},
		{"frame-ancestors", "'none'"},
		{"base-uri", "'self'"},
		{"form-action", "'self'"},
		{"object-src", "'none'"},
		{"worker-src", "'self' blob:"},
		{"manifest-src", "'self'"},
	}
	parts := make([]string, 0, len(directives))
	for _, d := range directives {
		parts = append(parts, d[0]+" "+d[1])
	}
	return strings.Join(parts, "; ")
}

// urlOrigin returns scheme://host for raw, or "" when it is not an absolute URL.
func urlOrigin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// cors answers the browser extension's cross-origin preflights, and only for
// the /api/ routes it uses. Auth there relies on a Bearer token (never a
// cross-site cookie), so allowing any origin is safe. The HTML app is
// same-origin and gets no CORS headers.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-CSRF-Token")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
