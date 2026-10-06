package store

import (
	"strings"
	"testing"

	"github.com/metruzanca/nanoflux/internal/db"
)

// fakePolicy gives reddit-shaped feed tokens so discovery tests need no plugin.
type fakePolicy struct{}

func (fakePolicy) CanonicalizeFeedURL(raw string) string { return raw }
func (fakePolicy) FeedToken(raw string) string {
	switch {
	case strings.Contains(raw, "/user/"):
		rest := raw[strings.Index(raw, "/user/")+len("/user/"):]
		return "u/" + strings.ToLower(strings.TrimSuffix(rest, "/submitted.rss"))
	case strings.Contains(raw, "/r/"):
		rest := raw[strings.Index(raw, "/r/")+len("/r/"):]
		return "r/" + strings.ToLower(strings.TrimSuffix(rest, ".rss"))
	}
	return ""
}

// fakeAuthorTokenizer names the u/<name> category as the item's author.
type fakeAuthorTokenizer struct{}

func (fakeAuthorTokenizer) AuthorTokens(_ string, cats []string) []string {
	var out []string
	for _, c := range cats {
		if strings.HasPrefix(strings.ToLower(c), "u/") {
			out = append(out, c)
		}
	}
	return out
}

func TestDiscoveryFilter(t *testing.T) {
	s := newTestStore(t)
	s.SetURLPolicy(fakePolicy{})
	s.SetAuthorTokenizer(fakeAuthorTokenizer{})
	u := mustUser(t, s, "alice")

	subAuthor, _ := s.Authors.Create(u.ID, "r/cats", "", "")
	sub, err := s.Feeds.Create(u.ID, subAuthor.ID, "r/cats", "https://www.reddit.com/r/cats.rss", "", "", 900)
	if err != nil {
		t.Fatalf("create sub feed: %v", err)
	}
	samAuthor, _ := s.Authors.Create(u.ID, "sam", "", "")
	if _, err := s.Feeds.Create(u.ID, samAuthor.ID, "u/sam", "https://www.reddit.com/user/sam/submitted.rss", "", "", 900); err != nil {
		t.Fatalf("create author feed: %v", err)
	}

	upsert := func(guid, owner string, cats ...string) {
		t.Helper()
		if _, err := s.Items.Upsert(sub.ID, Item{
			GUID: guid, Title: guid, Link: "https://x.dev/" + guid,
			Categories: cats, FetchedAt: db.Now(),
		}); err != nil {
			t.Fatalf("upsert %s: %v", guid, err)
		}
	}
	upsert("g_sam", "", "r/cats", "u/sam")
	upsert("g_other", "", "r/cats", "u/other")
	upsert("g_late", "", "r/cats", "u/late")

	followed := s.FollowedFeedTokens(u.ID)
	if followed["u/sam"] == 0 || followed["r/cats"] == 0 {
		t.Fatalf("FollowedFeedTokens = %v, want u/sam and r/cats", followed)
	}

	if err := s.Feeds.SetHideFollowedAuthors(u.ID, sub.ID, true); err != nil {
		t.Fatalf("enable discovery: %v", err)
	}
	n, err := s.ApplyDiscoveryFilter(u.ID, sub.ID)
	if err != nil {
		t.Fatalf("ApplyDiscoveryFilter: %v", err)
	}
	if n != 1 {
		t.Fatalf("removed = %d, want 1 (only u/sam)", n)
	}
	assertFeedTitles(t, s, u.ID, sub.ID, "g_other", "g_late")

	// Subscribing to u/late later removes its already-stored post.
	lateAuthor, _ := s.Authors.Create(u.ID, "late", "", "")
	if _, err := s.Feeds.Create(u.ID, lateAuthor.ID, "u/late", "https://www.reddit.com/user/late/submitted.rss", "", "", 900); err != nil {
		t.Fatalf("create late feed: %v", err)
	}
	if _, err := s.RefilterDiscoveryForUser(u.ID); err != nil {
		t.Fatalf("RefilterDiscoveryForUser: %v", err)
	}
	assertFeedTitles(t, s, u.ID, sub.ID, "g_other")
}

func assertFeedTitles(t *testing.T, s *Store, userID, feedID int64, want ...string) {
	t.Helper()
	items, err := s.Items.ListFeedItemsForFilter(feedID)
	if err != nil {
		t.Fatalf("list items: %v", err)
	}
	got := map[string]bool{}
	for _, it := range items {
		got[it.Title] = true
	}
	if len(got) != len(want) {
		t.Fatalf("feed items = %v, want %v", got, want)
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("feed missing %q (have %v)", w, got)
		}
	}
}
