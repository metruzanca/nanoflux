package httpapi

import (
	"strings"
	"testing"
	"time"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/store"
)

// collectionCard returns one collection's card markup from the index body.
func collectionCard(body string, id int64) string {
	start := strings.Index(body, `id="collection-`+itoa(id)+`"`)
	if start < 0 {
		return ""
	}
	end := strings.Index(body[start:], "</li>")
	if end < 0 {
		return body[start:]
	}
	return body[start : start+end]
}

func TestCollectionCountsCacheExpires(t *testing.T) {
	c := newCollectionCountsCache()
	c.ttl = 10 * time.Millisecond
	c.set(1, "UTC", map[int64]store.CollectionCounts{7: {Unread: 2, Read: 1}})
	if got, ok := c.get(1, "UTC"); !ok || got[7].Unread != 2 {
		t.Fatalf("cached counts = %+v, ok=%v", got, ok)
	}
	// A member view can carry a time filter, so a timezone change invalidates.
	if _, ok := c.get(1, "Europe/Berlin"); ok {
		t.Fatal("a timezone change should miss")
	}
	c.set(1, "UTC", map[int64]store.CollectionCounts{7: {Unread: 2, Read: 1}})
	time.Sleep(15 * time.Millisecond)
	if _, ok := c.get(1, "UTC"); ok {
		t.Fatal("expired entry should miss")
	}
}

// TestCollectionsIndexCountsAreCached pins the intended staleness: the index
// serves its item counts from a per-user TTL cache, so a read write is not
// reflected until the entry expires.
func TestCollectionsIndexCountsAreCached(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://cache.dev/rss.xml", "", "", 900)
	c, _ := s.store.Collections.Create(u.ID, "Dev")
	if err := s.store.Collections.AddFeed(u.ID, c.ID, f.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.Items.Upsert(f.ID, store.Item{GUID: "g1", Title: "One", Link: "https://cache.dev/1", FetchedAt: db.Now()}); err != nil {
		t.Fatal(err)
	}

	card := collectionCard(doGet(h, "/collections", cookie).Body.String(), c.ID)
	if !strings.Contains(card, "1 unread") {
		t.Fatalf("collection card should start at one unread: %s", card)
	}

	// A new item arrives, but the card answers from its cache.
	if _, err := s.store.Items.Upsert(f.ID, store.Item{GUID: "g2", Title: "Two", Link: "https://cache.dev/2", FetchedAt: db.Now()}); err != nil {
		t.Fatal(err)
	}
	card = collectionCard(doGet(h, "/collections", cookie).Body.String(), c.ID)
	if !strings.Contains(card, "1 unread") || strings.Contains(card, "2 unread") {
		t.Fatalf("collection card should serve the cached count: %s", card)
	}

	// Once the entry expires it recomputes from the store.
	s.collectionCounts.ttl = 0
	card = collectionCard(doGet(h, "/collections", cookie).Body.String(), c.ID)
	if !strings.Contains(card, "2 unread") {
		t.Fatalf("collection card should refresh after expiry: %s", card)
	}
}

// TestCollectionsIndexCountsIncludeMemberViews guards the merged scope: a view
// attached to a collection pulls in items from feeds outside the collection, and
// the index card must count them.
func TestCollectionsIndexCountsIncludeMemberViews(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")
	member, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://scope.dev/rss.xml", "", "", 900)
	elsewhere, _ := s.store.Feeds.Create(u.ID, a.ID, "Other", "https://elsewhere.dev/rss.xml", "", "", 900)
	c, _ := s.store.Collections.Create(u.ID, "Dev")
	if err := s.store.Collections.AddFeed(u.ID, c.ID, member.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.Items.Upsert(member.ID, store.Item{GUID: "own", Title: "Own", Link: "https://scope.dev/own", FetchedAt: db.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.Items.Upsert(elsewhere.ID, store.Item{
		GUID: "tagged", Title: "Tagged", Link: "https://elsewhere.dev/tagged",
		Categories: []string{"u/example-poster"}, FetchedAt: db.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	v, err := s.store.SavedViews.Create(u.ID, store.SavedView{
		AuthorID: a.ID,
		Title:    "Example",
		Filters:  []string{"tag:show:u/example-poster"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.SavedViews.SetCollections(u.ID, v.ID, []int64{c.ID}); err != nil {
		t.Fatal(err)
	}

	card := collectionCard(doGet(h, "/collections", cookie).Body.String(), c.ID)
	if !strings.Contains(card, "1 views") || !strings.Contains(card, "2 unread") {
		t.Fatalf("collection card should count the member feed and the view: %s", card)
	}
}
