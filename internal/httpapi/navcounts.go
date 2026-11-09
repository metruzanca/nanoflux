package httpapi

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/metruzanca/nanoflux/internal/auth"
)

// navCountsTTL is how long a user's nav totals are reused. The badges may lag a
// read toggle by up to this long, which is acceptable for a fixed per-page cost
// of two count queries.
const navCountsTTL = 5 * time.Second

// navCountsCache memoizes nav totals per user for navCountsTTL. It is TTL-only
// on purpose: no invalidation hooks to keep in sync with every read marker.
type navCountsCache struct {
	mu     sync.Mutex
	byUser map[int64]navCountsEntry
	ttl    time.Duration
}

type navCountsEntry struct {
	counts navCounts
	at     time.Time
}

func newNavCountsCache() *navCountsCache {
	return &navCountsCache{byUser: map[int64]navCountsEntry{}, ttl: navCountsTTL}
}

func (c *navCountsCache) get(userID int64) (navCounts, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.byUser[userID]
	if !ok {
		return navCounts{}, false
	}
	if time.Since(e.at) >= c.ttl {
		delete(c.byUser, userID)
		return navCounts{}, false
	}
	return e.counts, true
}

func (c *navCountsCache) set(userID int64, n navCounts) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.byUser) >= 4096 {
		now := time.Now()
		for id, e := range c.byUser {
			if now.Sub(e.at) >= c.ttl {
				delete(c.byUser, id)
			}
		}
	}
	c.byUser[userID] = navCountsEntry{counts: n, at: time.Now()}
}

// navCounts are the unread totals shown in the top navigation: the total unread
// item count and how many authors have at least one unread item. They ride the
// request context so the shared topbar can render them without every page
// handler threading them through basePage.
type navCounts struct {
	Unread            int
	AuthorsWithUnread int
}

type navCountsCtxKey struct{}

// withNavCounts stores the counts in a request context.
func withNavCounts(ctx context.Context, c navCounts) context.Context {
	return context.WithValue(ctx, navCountsCtxKey{}, c)
}

// navCountsFrom reads the counts the middleware stored; the zero value (no
// badges) when they were never computed.
func navCountsFrom(ctx context.Context) navCounts {
	if c, ok := ctx.Value(navCountsCtxKey{}).(navCounts); ok {
		return c
	}
	return navCounts{}
}

// computeNavCounts queries the two nav totals for a user. UnreadCountsByAuthor
// returns one entry per author with unread items, so its length is the author
// badge without fetching the author rows themselves.
func (s *Server) computeNavCounts(userID int64) navCounts {
	var c navCounts
	if n, err := s.store.Items.CountUnread(userID, 0); err == nil {
		c.Unread = n
	}
	if counts, err := s.store.Items.UnreadCountsByAuthor(userID); err == nil {
		c.AuthorsWithUnread = len(counts)
	}
	return c
}

func (s *Server) cachedNavCounts(userID int64) navCounts {
	if c, ok := s.navCounts.get(userID); ok {
		return c
	}
	c := s.computeNavCounts(userID)
	s.navCounts.set(userID, c)
	return c
}

// navCountsMiddleware resolves the session for GET page requests and attaches
// the nav counts to the context before the page handler renders. Requests that
// don't render the topbar (JSON API, static assets, public share pages) are
// skipped to avoid the extra queries.
func (s *Server) navCountsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !navCountsPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		u, err := s.auth.User(r)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		r = auth.WithUser(r, u)
		ctx := withNavCounts(r.Context(), s.cachedNavCounts(u.ID))
		ctx = withProvisioners(ctx, s.provisionEntries())
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// navCountsPath reports whether a GET path renders the authenticated topbar and
// is therefore worth resolving a session and counts for. Fragments, JSON, image
// and asset routes are skipped so they don't pay the extra count queries.
func navCountsPath(p string) bool {
	if strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/static/") {
		return false
	}
	if strings.HasPrefix(p, "/shared/") || strings.HasPrefix(p, "/l/") || strings.HasPrefix(p, "/f/") {
		return false
	}
	if strings.HasPrefix(p, "/fragments/") || strings.HasPrefix(p, "/icons/") {
		return false
	}
	// htmx fragments ("load more", item view, item lists) and row fragments
	// scoped to an entity render no topbar.
	if strings.HasPrefix(p, "/items") || strings.HasSuffix(p, "/items") {
		return false
	}
	if strings.HasSuffix(p, "/avatar") || strings.HasSuffix(p, "/avatar-refresh") {
		return false
	}
	switch p {
	case "/manifest.webmanifest", "/sw.js", "/logo.svg", "/favicon.svg", "/img", "/avatar":
		return false
	}
	return true
}

// apiNavCounts returns the nav totals as JSON so the client can refresh the
// badges after an htmx swap (an item toggled read/unread) without a full reload.
func (s *Server) apiNavCounts(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	c := s.cachedNavCounts(u.ID)
	writeJSON(w, http.StatusOK, map[string]int{"unread": c.Unread, "authors": c.AuthorsWithUnread})
}
