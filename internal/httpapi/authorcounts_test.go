package httpapi

import (
	"strings"
	"testing"
	"time"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/store"
)

func TestAuthorUnreadCacheExpires(t *testing.T) {
	c := newAuthorUnreadCache()
	c.ttl = 10 * time.Millisecond
	c.set(1, "UTC", map[int64]int{7: 2})
	if got, ok := c.get(1, "UTC"); !ok || got[7] != 2 {
		t.Fatalf("cached counts = %+v, ok=%v", got, ok)
	}
	// A saved view can carry a time filter, so a timezone change invalidates.
	if _, ok := c.get(1, "Europe/Berlin"); ok {
		t.Fatal("a timezone change should miss")
	}
	c.set(1, "UTC", map[int64]int{7: 2})
	time.Sleep(15 * time.Millisecond)
	if _, ok := c.get(1, "UTC"); ok {
		t.Fatal("expired entry should miss")
	}
}

// TestAuthorsIndexUnreadCountsAreCached pins the intended staleness: the author
// index serves its unread counts from a per-user TTL cache, so a read write is
// not reflected until the entry expires.
func TestAuthorsIndexUnreadCountsAreCached(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Cached", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://cache.dev/rss.xml", "", "", 900)
	if _, err := s.store.Items.Upsert(f.ID, store.Item{GUID: "g1", Title: "One", Link: "https://cache.dev/1", FetchedAt: db.Now()}); err != nil {
		t.Fatal(err)
	}

	body := doGet(h, "/authors", cookie).Body.String()
	if !strings.Contains(body, `data-unread="1"`) {
		t.Fatalf("authors index should start at one unread: %s", body)
	}

	// A new item arrives, but the index answers from its cache.
	if _, err := s.store.Items.Upsert(f.ID, store.Item{GUID: "g2", Title: "Two", Link: "https://cache.dev/2", FetchedAt: db.Now()}); err != nil {
		t.Fatal(err)
	}
	body = doGet(h, "/authors", cookie).Body.String()
	if !strings.Contains(body, `data-unread="1"`) || strings.Contains(body, `data-unread="2"`) {
		t.Fatalf("authors index should serve the cached count: %s", body)
	}

	// Once the entry expires it recomputes from the store (and from a fresh nav
	// map, which is what a real request would have).
	s.authorUnread.ttl = 0
	s.navCounts.ttl = 0
	body = doGet(h, "/authors", cookie).Body.String()
	if !strings.Contains(body, `data-unread="2"`) {
		t.Fatalf("authors index should refresh after expiry: %s", body)
	}
}

// TestAuthorUnreadViewScopeDoesNotLeakIntoNavCache guards the clone in
// computeAuthorUnread: merging a view's scoped count must not rewrite the nav
// cache's owner-feed map, which the nav badge counts with len().
func TestAuthorUnreadViewScopeDoesNotLeakIntoNavCache(t *testing.T) {
	s, h := newTestServer(t)
	sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	scoped, _ := s.store.Authors.Create(u.ID, "Scoped", "", "")
	ownFeed, _ := s.store.Feeds.Create(u.ID, scoped.ID, "Blog", "https://scope.dev/rss.xml", "", "", 900)
	if _, err := s.store.Items.Upsert(ownFeed.ID, store.Item{GUID: "own", Title: "Own", Link: "https://scope.dev/own", FetchedAt: db.Now()}); err != nil {
		t.Fatal(err)
	}
	// The view pulls in a post from another author's feed, which is what makes
	// the scoped count differ from the owner-feed count.
	other, _ := s.store.Authors.Create(u.ID, "Other", "", "")
	otherFeed, _ := s.store.Feeds.Create(u.ID, other.ID, "Elsewhere", "https://elsewhere.dev/rss.xml", "", "", 900)
	if _, err := s.store.Items.Upsert(otherFeed.ID, store.Item{
		GUID: "tagged", Title: "Tagged", Link: "https://elsewhere.dev/tagged",
		Categories: []string{"u/example-poster"}, FetchedAt: db.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.SavedViews.Create(u.ID, store.SavedView{
		AuthorID: scoped.ID,
		Title:    "Example",
		Filters:  []string{"tag:show:u/example-poster"},
	}); err != nil {
		t.Fatal(err)
	}

	// Populate the nav map first, the way the middleware does per request.
	if c := s.cachedNavCounts(u.ID); c.AuthorsWithUnread != 2 {
		t.Fatalf("nav authors with unread = %d, want 2", c.AuthorsWithUnread)
	}
	rows := s.authorRows(u.ID, "UTC")
	if len(rows) != 2 {
		t.Fatalf("author rows = %+v, want two authors", rows)
	}
	for _, r := range rows {
		if r.ID == scoped.ID && r.Unread != 2 {
			t.Fatalf("scoped author unread = %d, want 2 (own feed plus the view)", r.Unread)
		}
		if r.ID == other.ID && r.Unread != 1 {
			t.Fatalf("other author unread = %d, want its own count of 1", r.Unread)
		}
	}

	navMap, ok := s.navCounts.byAuthor(u.ID)
	if !ok || navMap[scoped.ID] != 1 || navMap[other.ID] != 1 {
		t.Fatalf("nav cache map = %+v (ok=%v), want owner-feed counts of 1 each", navMap, ok)
	}
}
