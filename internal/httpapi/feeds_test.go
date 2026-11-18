package httpapi

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/store"
)

// TestFeedLibraryAndUnassignedFeed covers the feed library: an add with
// author_id=none creates a library-only feed, /feeds lists it, and its page
// still renders its items.
func TestFeedLibraryAndUnassignedFeed(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")

	rr := doForm(h, "POST", "/feeds", url.Values{
		"title": {"Orphan"}, "feed_url": {"https://orphan.dev/rss.xml"},
		"author_id": {"none"}, "poll_interval_sec": {"900"},
	}, cookie)
	if rr.Code != http.StatusNoContent || !strings.HasPrefix(rr.Header().Get("HX-Redirect"), "/feeds/") {
		t.Fatalf("unassigned feed create: %d %q %s", rr.Code, rr.Header().Get("HX-Redirect"), rr.Body.String())
	}
	feeds, _ := s.store.Feeds.List(u.ID)
	if len(feeds) != 1 || feeds[0].AuthorID != 0 {
		t.Fatalf("feeds = %+v, want one unassigned feed", feeds)
	}
	f := feeds[0]
	s.store.Items.Upsert(f.ID, store.Item{GUID: "g", Title: "Orphan post", Link: "https://orphan.dev/1", FetchedAt: db.Now()})

	body := doGet(h, "/feeds", cookie).Body.String()
	if !strings.Contains(body, "Orphan") || !strings.Contains(body, "unassigned") {
		t.Fatalf("/feeds should list the unassigned feed: %s", body)
	}

	body = doGet(h, "/feeds/"+itoa(f.ID), cookie).Body.String()
	if !strings.Contains(body, "Orphan post") {
		t.Fatalf("feed page should list its items: %s", body)
	}
}

// TestAuthorDeleteUnassignsFeeds covers the library lifecycle: deleting an
// author keeps their feeds and items, showing them as unassigned.
func TestAuthorDeleteUnassignsFeeds(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://b.dev/rss.xml", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{GUID: "g", Title: "Post", Link: "https://b.dev/1", FetchedAt: db.Now()})

	rr := doForm(h, "POST", "/authors/"+itoa(a.ID)+"/delete", nil, cookie)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("delete author: %d %s", rr.Code, rr.Body.String())
	}
	got, err := s.store.Feeds.ByID(u.ID, f.ID)
	if err != nil || got.AuthorID != 0 {
		t.Fatalf("feed should survive unassigned: %+v %v", got, err)
	}
	if n, _ := s.store.Items.CountUnread(u.ID, f.ID); n != 1 {
		t.Fatalf("feed items should survive, got %d unread", n)
	}
}
