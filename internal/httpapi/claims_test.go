package httpapi

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/store"
)

// TestAggregatorHidesClaimedAuthorItems covers the endpoint wiring for the
// shared-feed model: a poster author's view claims u/sam, and the aggregator
// author hides it once "aggregator feed" is enabled.
func TestAggregatorHidesClaimedAuthorItems(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")

	agg, _ := s.store.Authors.Create(u.ID, "r/sub", "", "")
	poster, _ := s.store.Authors.Create(u.ID, "u/sam", "", "")
	if _, err := s.store.SavedViews.Create(u.ID, store.SavedView{
		AuthorID: poster.ID, Title: "sam", Filters: []string{"tag:show:u/sam"},
	}); err != nil {
		t.Fatal(err)
	}
	f, err := s.store.Feeds.Create(u.ID, agg.ID, "sub", "https://example.dev/sub.xml", "", "", 900)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.Items.Upsert(f.ID, store.Item{GUID: "sam", Title: "sam post", Link: "https://x/sam", Categories: []string{"u/sam"}, FetchedAt: db.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.Items.Upsert(f.ID, store.Item{GUID: "other", Title: "other post", Link: "https://x/other", Categories: []string{"u/other"}, FetchedAt: db.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.Claims.RebuildAll(u.ID); err != nil {
		t.Fatal(err)
	}

	page := doGet(h, "/authors/"+itoa(agg.ID), cookie).Body.String()
	if !strings.Contains(page, "sam post") || !strings.Contains(page, "other post") {
		t.Fatalf("non-aggregator author should show both posts: %s", page)
	}

	rr := doHtmxForm(h, http.MethodPost, "/authors/"+itoa(agg.ID)+"/edit", url.Values{
		"name": {"r/sub"}, "is_aggregator": {"1"},
	}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("save aggregator: %d %s", rr.Code, rr.Body.String())
	}
	got, _ := s.store.Authors.ByID(u.ID, agg.ID)
	if !got.IsAggregator {
		t.Fatalf("is_aggregator was not saved")
	}
	page = doGet(h, "/authors/"+itoa(agg.ID), cookie).Body.String()
	if strings.Contains(page, "sam post") {
		t.Fatalf("aggregator page must hide the claimed post: %s", page)
	}
	if !strings.Contains(page, "other post") {
		t.Fatalf("aggregator page should keep the unclaimed post: %s", page)
	}
	posterPage := doGet(h, "/authors/"+itoa(poster.ID), cookie).Body.String()
	if !strings.Contains(posterPage, "sam post") {
		t.Fatalf("poster page should show the claimed post: %s", posterPage)
	}
}

// TestAuthorFeedMembershipEndpoint covers the author-side feed picker: one feed
// can be selected by several authors and removed from one without touching the
// other.
func TestAuthorFeedMembershipEndpoint(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "A", "", "")
	b, _ := s.store.Authors.Create(u.ID, "B", "", "")
	f1, _ := s.store.Feeds.Create(u.ID, 0, "One", "https://one.dev/feed.xml", "", "", 900)
	f2, _ := s.store.Feeds.Create(u.ID, 0, "Two", "https://two.dev/feed.xml", "", "", 900)

	rr := doHtmxForm(h, http.MethodPost, "/authors/"+itoa(a.ID)+"/feed-membership", url.Values{
		"author_feed_ids": {itoa(f1.ID), itoa(f2.ID)},
	}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("set memberships: %d %s", rr.Code, rr.Body.String())
	}
	for _, id := range []int64{f1.ID, f2.ID} {
		if !s.store.Feeds.IsAuthorOfFeed(u.ID, id, a.ID) {
			t.Fatalf("feed %d should belong to author A", id)
		}
	}
	if s.store.Feeds.IsAuthorOfFeed(u.ID, f1.ID, b.ID) {
		t.Fatalf("author B should not select the feed yet")
	}

	rr = doHtmxForm(h, http.MethodPost, "/authors/"+itoa(b.ID)+"/feed-membership", url.Values{
		"author_feed_ids": {itoa(f1.ID)},
	}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("set B memberships: %d %s", rr.Code, rr.Body.String())
	}
	if !s.store.Feeds.IsAuthorOfFeed(u.ID, f1.ID, a.ID) || !s.store.Feeds.IsAuthorOfFeed(u.ID, f1.ID, b.ID) {
		t.Fatalf("both authors should select feed one")
	}

	rr = doHtmxForm(h, http.MethodPost, "/authors/"+itoa(a.ID)+"/feed-membership", url.Values{
		"author_feed_ids": {itoa(f1.ID)},
	}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("narrow A memberships: %d %s", rr.Code, rr.Body.String())
	}
	if s.store.Feeds.IsAuthorOfFeed(u.ID, f2.ID, a.ID) {
		t.Fatalf("feed two should be removed from A")
	}
	if !s.store.Feeds.IsAuthorOfFeed(u.ID, f1.ID, a.ID) {
		t.Fatalf("feed one should stay on A")
	}
}
