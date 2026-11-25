package store

import (
	"testing"

	"github.com/metruzanca/nanoflux/internal/db"
)

// TestSharedFeedAuthorSlices covers the feed-library model: one feed selected by
// an aggregator, a view-backed poster author claiming u/sam, and the per-author
// rules that make their pages different.
func TestSharedFeedAuthorSlices(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")

	agg, err := s.Authors.Create(u.ID, "r/sub", "", "")
	if err != nil {
		t.Fatal(err)
	}
	poster, err := s.Authors.Create(u.ID, "u/sam", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SavedViews.Create(u.ID, SavedView{
		AuthorID: poster.ID, Title: "sam", Filters: []string{"tag:show:u/sam"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Authors.SetAggregator(u.ID, agg.ID, true); err != nil {
		t.Fatal(err)
	}

	f, err := s.Feeds.Create(u.ID, agg.ID, "sub", "https://example.dev/sub.xml", "", "", 900)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Items.Upsert(f.ID, Item{GUID: "sam", Title: "sam post", Link: "https://x/sam", Categories: []string{"u/sam"}, PublishedAt: daysAgo(10), FetchedAt: db.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Items.Upsert(f.ID, Item{GUID: "other", Title: "other post", Link: "https://x/other", Categories: []string{"u/other"}, PublishedAt: daysAgo(10), FetchedAt: db.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claims.RebuildAll(u.ID); err != nil {
		t.Fatal(err)
	}

	titles := func(items []ItemWithFeed) []string {
		out := make([]string, 0, len(items))
		for _, it := range items {
			out = append(out, it.Title)
		}
		return out
	}
	has := func(got []string, want string) bool {
		for _, v := range got {
			if v == want {
				return true
			}
		}
		return false
	}

	aggItems, err := s.Items.List(u.ID, ItemFilter{AuthorID: agg.ID, UnclaimedOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := titles(aggItems); len(got) != 1 || got[0] != "other post" {
		t.Fatalf("aggregator slice = %v, want [other post]", got)
	}
	posterViews, err := s.SavedViews.FiltersForAuthor(u.ID, poster.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	posterItems, err := s.Items.List(u.ID, ItemFilter{AuthorID: poster.ID, Views: posterViews})
	if err != nil {
		t.Fatal(err)
	}
	if got := titles(posterItems); len(got) != 1 || got[0] != "sam post" {
		t.Fatalf("poster slice = %v, want [sam post]", got)
	}
	global, err := s.Items.List(u.ID, ItemFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if got := titles(global); !has(got, "sam post") || !has(got, "other post") {
		t.Fatalf("global union = %v, want both posts", got)
	}

	// Aggregator auto-read 3 days, poster off. The claimed post stays unread.
	three := 3
	if err := s.Authors.SetAutoReadAfterDays(u.ID, agg.ID, &three); err != nil {
		t.Fatal(err)
	}
	off := 0
	if err := s.Authors.SetAutoReadAfterDays(u.ID, poster.ID, &off); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Items.MarkAutoRead(u.ID); err != nil {
		t.Fatal(err)
	}
	byTitle := func(title string) Item {
		t.Helper()
		for _, it := range global {
			if it.Title == title {
				got, err := s.Items.ByID(u.ID, it.ID)
				if err != nil {
					t.Fatal(err)
				}
				return got
			}
		}
		t.Fatalf("missing item %q", title)
		return Item{}
	}
	if got := byTitle("other post"); !got.Read {
		t.Fatalf("aggregator-only post should be auto-read")
	}
	if got := byTitle("sam post"); got.Read {
		t.Fatalf("poster-claimed post must not be auto-read by the aggregator")
	}

	// Delete windows: aggregator 3 days, poster off. Read both posts; only the
	// aggregator-only post may be deleted (the poster's off window protects its
	// claim).
	if err := s.Authors.SetAutoDeleteAfterDays(u.ID, agg.ID, &three); err != nil {
		t.Fatal(err)
	}
	if err := s.Authors.SetAutoDeleteAfterDays(u.ID, poster.ID, &off); err != nil {
		t.Fatal(err)
	}
	var otherID, samID int64
	for _, it := range global {
		if it.Title == "other post" {
			otherID = it.ID
		}
		if it.Title == "sam post" {
			samID = it.ID
		}
		if err := s.Items.SetRead(u.ID, it.ID, true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DB().Exec(`UPDATE items SET read_at = ? WHERE user_id = ?`, daysAgo(10), u.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Items.DeleteOldReadItems(u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Items.ByID(u.ID, otherID); err == nil {
		t.Fatalf("aggregator-only read post should have been deleted")
	}
	if _, err := s.Items.ByID(u.ID, samID); err != nil {
		t.Fatalf("poster-claimed post must survive the delete sweep: %v", err)
	}
}

// TestFindByURLAttachesExistingFeed guards the add-flow rule: a URL already in
// the library is one feed, and adding it for another author only adds
// membership.
func TestFindByURLAttachesExistingFeed(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, _ := s.Authors.Create(u.ID, "A", "", "")
	b, _ := s.Authors.Create(u.ID, "B", "", "")
	f, err := s.Feeds.Create(u.ID, a.ID, "feed", "https://example.dev/feed.xml", "", "", 900)
	if err != nil {
		t.Fatal(err)
	}
	found, ok := s.Feeds.FindByURL(u.ID, "https://example.dev/feed.xml")
	if !ok || found.ID != f.ID {
		t.Fatalf("FindByURL = %+v, %v; want feed %d", found, ok, f.ID)
	}
	if err := s.Feeds.AddAuthor(u.ID, found.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{a.ID, b.ID} {
		feeds, err := s.Feeds.ListByAuthor(u.ID, id)
		if err != nil || len(feeds) != 1 || feeds[0].ID != f.ID {
			t.Fatalf("author %d feeds = %v, %v; want feed %d", id, feeds, err, f.ID)
		}
	}
}
