package httpapi

import (
	"testing"

	"github.com/metruzanca/nanoflux/internal/store"
)

// A reddit post renders as "r/cats by u/sam"; both parts link internally when
// the user subscribes to both the sub and the user feed.
func TestRedditAttributionInternalLinks(t *testing.T) {
	it := store.ItemWithFeed{
		Item:       store.Item{FeedID: 10, Categories: []string{"r/cats", "u/sam"}},
		FeedTitle:  "r/cats",
		AuthorID:   7,
		AuthorName: "r/cats",
		Sources: []store.ItemSource{
			{FeedID: 11, FeedTitle: "u/sam", AuthorID: 8, AuthorName: "sam"},
		},
	}
	parts := itemAttribution(it)
	if len(parts) != 3 {
		t.Fatalf("want 3 parts, got %+v", parts)
	}
	if parts[0].Text != "r/cats" || parts[0].URL != "/authors/7" {
		t.Fatalf("sub part: %+v", parts[0])
	}
	if parts[1].Text != "by" || parts[1].URL != "" {
		t.Fatalf("separator: %+v", parts[1])
	}
	if parts[2].Text != "u/sam" || parts[2].URL != "/authors/8" {
		t.Fatalf("user part should link the internal author: %+v", parts[2])
	}
}

// When the poster's user feed is not subscribed, u/sam links to the reddit
// profile as an external link.
func TestRedditAttributionExternalPoster(t *testing.T) {
	it := store.ItemWithFeed{
		Item:       store.Item{FeedID: 10, Categories: []string{"r/cats", "u/sam"}},
		FeedTitle:  "r/cats",
		AuthorID:   7,
		AuthorName: "r/cats",
	}
	parts := itemAttribution(it)
	if len(parts) != 3 {
		t.Fatalf("want 3 parts, got %+v", parts)
	}
	if parts[2].URL != "https://www.reddit.com/user/sam/" || !parts[2].External {
		t.Fatalf("poster should link externally to reddit: %+v", parts[2])
	}
	// The sub is still linked internally to its author page.
	if parts[0].URL != "/authors/7" {
		t.Fatalf("sub part: %+v", parts[0])
	}
}

// A non-reddit item has no attribution parts, so templates keep the existing
// author/feed source line.
func TestNonRedditAttributionEmpty(t *testing.T) {
	it := store.ItemWithFeed{
		Item:       store.Item{FeedID: 1},
		FeedTitle:  "Blog",
		AuthorID:   2,
		AuthorName: "Metru",
	}
	if parts := itemAttribution(it); parts != nil {
		t.Fatalf("non-reddit item should have no attribution parts, got %+v", parts)
	}
}

func TestRedditAttributionUnsubscribedSub(t *testing.T) {
	it := store.ItemWithFeed{
		Item:       store.Item{FeedID: 10, Categories: []string{"r/dogs", "u/sam"}},
		FeedTitle:  "u/sam",
		AuthorID:   8,
		AuthorName: "sam",
	}
	parts := itemAttribution(it)
	if parts[0].URL != "https://www.reddit.com/r/dogs/" || !parts[0].External {
		t.Fatalf("unsubscribed sub should link to reddit: %+v", parts[0])
	}
	if parts[2].URL != "/authors/8" {
		t.Fatalf("the owner user feed should link the poster internally: %+v", parts[2])
	}
}
