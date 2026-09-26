package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/metruzanca/nanoflux/internal/auth"
)

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

// computeNavCounts queries the two nav totals for a user. The author count uses
// ListWithFeedCount, so "authors with unread" means authors with UnreadCount > 0.
func (s *Server) computeNavCounts(userID int64) navCounts {
	var c navCounts
	if n, err := s.store.Items.CountUnread(userID, 0); err == nil {
		c.Unread = n
	}
	if rows, err := s.store.Authors.ListWithFeedCount(userID); err == nil {
		for _, r := range rows {
			if r.UnreadCount > 0 {
				c.AuthorsWithUnread++
			}
		}
	}
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
		ctx := withNavCounts(r.Context(), s.computeNavCounts(u.ID))
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
	c := s.computeNavCounts(u.ID)
	writeJSON(w, http.StatusOK, map[string]int{"unread": c.Unread, "authors": c.AuthorsWithUnread})
}
