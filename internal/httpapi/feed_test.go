package httpapi

import (
	"strings"
	"testing"

	"github.com/metruzanca/nanoflux/internal/db"
)

func TestFeedErrorSurface(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	f, err := s.store.Feeds.Create(u.ID, 0, "Broken Feed", "https://broken.dev/feed.xml", "", "", 900)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.Feeds.SetPollMeta(f.ID, "", "", db.Now(), "boom: dns lookup failed"); err != nil {
		t.Fatal(err)
	}

	// Feed detail page shows the error text.
	body := doGet(h, "/feeds/"+itoa(f.ID), cookie).Body.String()
	if !strings.Contains(body, "last poll failed: boom: dns lookup failed") {
		t.Fatalf("feed page missing error text: %s", body)
	}

	// The feeds list row gets a badge.
	body = doGet(h, "/feeds", cookie).Body.String()
	if !strings.Contains(body, "last poll failed") {
		t.Fatalf("feeds list missing error badge: %s", body)
	}
}

func TestFeedErrorHiddenAfterSuccess(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	f, err := s.store.Feeds.Create(u.ID, 0, "Fine Feed", "https://fine.dev/feed.xml", "", "", 900)
	if err != nil {
		t.Fatal(err)
	}
	body := doGet(h, "/feeds/"+itoa(f.ID), cookie).Body.String()
	if strings.Contains(body, "last poll failed") {
		t.Fatal("healthy feed should not show an error")
	}
}
