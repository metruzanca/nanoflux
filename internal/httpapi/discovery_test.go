package httpapi

import (
	"net/url"
	"strings"
	"testing"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/store"
)

// discPolicy / discTokenizer give reddit-shaped tokens for the handler test.
type discPolicy struct{}

func (discPolicy) CanonicalizeFeedURL(raw string) string { return raw }
func (discPolicy) FeedToken(raw string) string {
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

type discTokenizer struct{}

func (discTokenizer) AuthorTokens(_ string, cats []string) []string {
	var out []string
	for _, c := range cats {
		if strings.HasPrefix(strings.ToLower(c), "u/") {
			out = append(out, c)
		}
	}
	return out
}

// TestFeedDiscoveryToggle asserts the discovery-mode route flips the feed and
// retroactively removes already-stored posts by a followed author.
func TestFeedDiscoveryToggle(t *testing.T) {
	s, h := newTestServer(t)
	s.store.SetURLPolicy(discPolicy{})
	s.store.SetAuthorTokenizer(discTokenizer{})
	cookie := sessionCookie(t, h)
	u, err := s.store.Users.ByUsername("alice")
	if err != nil {
		t.Fatal(err)
	}

	subA, _ := s.store.Authors.Create(u.ID, "r/cats", "", "")
	sub, _ := s.store.Feeds.Create(u.ID, subA.ID, "r/cats", "https://www.reddit.com/r/cats.rss", "", "", 900)
	samA, _ := s.store.Authors.Create(u.ID, "sam", "", "")
	if _, err := s.store.Feeds.Create(u.ID, samA.ID, "u/sam", "https://www.reddit.com/user/sam/submitted.rss", "", "", 900); err != nil {
		t.Fatal(err)
	}
	for _, it := range []store.Item{
		{GUID: "g_sam", Title: "from sam", Link: "https://x.dev/sam", Categories: []string{"r/cats", "u/sam"}, FetchedAt: db.Now()},
		{GUID: "g_other", Title: "from other", Link: "https://x.dev/other", Categories: []string{"r/cats", "u/other"}, FetchedAt: db.Now()},
	} {
		if _, err := s.store.Items.Upsert(sub.ID, it); err != nil {
			t.Fatal(err)
		}
	}

	rr := doForm(h, "POST", "/feeds/"+itoa(sub.ID)+"/discovery", url.Values{"on": {"1"}}, cookie)
	if rr.Code != 200 {
		t.Fatalf("discovery toggle: got %d, want 200", rr.Code)
	}
	got, _ := s.store.Feeds.ByID(u.ID, sub.ID)
	if !got.HideFollowedAuthors {
		t.Fatal("feed should have discovery mode on")
	}
	items, _ := s.store.Items.ListFeedItemsForFilter(sub.ID)
	if len(items) != 1 || items[0].Title != "from other" {
		t.Fatalf("feed items = %+v, want only from-other", items)
	}
}
