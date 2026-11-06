package store

import (
	"testing"

	"github.com/metruzanca/nanoflux/internal/db"
)

// TestViewFilterAuthorScope covers the OR between an author's own feeds and the
// saved views assigned to them, including counts, stats, tag options and
// filtered mark-all.
func TestViewFilterAuthorScope(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")

	aggregate, _ := s.Authors.Create(u.ID, "Aggregate", "", "")
	feed, _ := s.Feeds.Create(u.ID, aggregate.ID, "Sub", "https://sub.example/rss.xml", "", "", 900)
	viewAuthor, _ := s.Authors.Create(u.ID, "Captain", "", "")
	if _, err := s.SavedViews.Create(u.ID, SavedView{
		AuthorID: viewAuthor.ID, Title: "Captain posts",
		Filters: []string{"tag:show:u/Captain"},
	}); err != nil {
		t.Fatalf("saved view: %v", err)
	}

	for _, it := range []Item{
		{GUID: "a", Title: "Captain post", Link: "https://x/a", Categories: []string{"u/Captain"}, FetchedAt: db.Now()},
		{GUID: "b", Title: "Other post", Link: "https://x/b", Categories: []string{"u/Other"}, FetchedAt: db.Now()},
	} {
		if _, err := s.Items.Upsert(feed.ID, it); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}

	view := ViewFilter{TagGroups: []TagGroup{{Tags: []string{"u/Captain"}, All: true}}}

	// The view author has no feeds, so its scope is only the view.
	onlyView := ItemFilter{AuthorID: viewAuthor.ID, Views: []ViewFilter{view}}
	items, _, err := s.Items.ListPage(u.ID, onlyView)
	if err != nil {
		t.Fatalf("list view scope: %v", err)
	}
	if len(items) != 1 || items[0].Title != "Captain post" {
		t.Fatalf("view-only author scope = %+v", items)
	}
	if n, _ := s.Items.CountFiltered(u.ID, onlyView); n != 1 {
		t.Fatalf("view-only count = %d", n)
	}
	stats, err := s.Items.StatsFilter(u.ID, onlyView)
	if err != nil || stats.Total != 1 || stats.Unread != 1 {
		t.Fatalf("view-only stats = %+v, %v", stats, err)
	}
	cats, err := s.Items.ListCategoriesFilter(u.ID, onlyView)
	if err != nil || len(cats) != 1 || cats[0].Category != "u/Captain" {
		t.Fatalf("view-only categories = %+v, %v", cats, err)
	}

	// The aggregate author's own feed plus the view union keeps both posts once.
	union := ItemFilter{AuthorID: aggregate.ID, Views: []ViewFilter{view}}
	items, _, err = s.Items.ListPage(u.ID, union)
	if err != nil {
		t.Fatalf("list union: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("union should return both posts once: %+v", items)
	}
	if n, _ := s.Items.CountFiltered(u.ID, union); n != 2 {
		t.Fatalf("union count = %d", n)
	}
	if times, err := s.Items.RecentTimesFilter(u.ID, union, 10); err != nil || len(times) != 2 {
		t.Fatalf("union recent times = %v, %v", times, err)
	}

	// Mark-all on the selected view must leave the non-matching post unread.
	if _, err := s.Items.MarkAllReadFiltered(u.ID, onlyView); err != nil {
		t.Fatalf("mark view read: %v", err)
	}
	aID, _ := s.Items.ByFeedIdentity(feed.ID, "a")
	bID, _ := s.Items.ByFeedIdentity(feed.ID, "b")
	aIt, _ := s.Items.ByID(u.ID, aID)
	bIt, _ := s.Items.ByID(u.ID, bID)
	if !aIt.Read {
		t.Fatalf("matching post should be read")
	}
	if bIt.Read {
		t.Fatalf("non-matching post should stay unread")
	}
}

// TestViewFilterCollectionScope covers a collection's union of member feeds and
// member views, including counts and tag options.
func TestViewFilterCollectionScope(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	author, _ := s.Authors.Create(u.ID, "Blog", "", "")
	inCollection, _ := s.Feeds.Create(u.ID, author.ID, "In", "https://in.example/rss.xml", "", "", 900)
	outside, _ := s.Feeds.Create(u.ID, author.ID, "Out", "https://out.example/rss.xml", "", "", 900)

	for _, it := range []Item{
		{GUID: "in", Title: "Feed post", Link: "https://in.example/1", FetchedAt: db.Now()},
		{GUID: "view", Title: "View post", Link: "https://out.example/1", Categories: []string{"r/golang"}, FetchedAt: db.Now()},
		{GUID: "other", Title: "Other post", Link: "https://out.example/2", FetchedAt: db.Now()},
	} {
		feedID := inCollection.ID
		if it.GUID != "in" {
			feedID = outside.ID
		}
		if _, err := s.Items.Upsert(feedID, it); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}

	col, _ := s.Collections.Create(u.ID, "Dev")
	if err := s.Collections.AddFeed(u.ID, col.ID, inCollection.ID); err != nil {
		t.Fatalf("add feed: %v", err)
	}
	view, err := s.SavedViews.Create(u.ID, SavedView{
		Title: "Go", Filters: []string{"tag:show:r/golang"},
	})
	if err != nil {
		t.Fatalf("saved view: %v", err)
	}
	if err := s.SavedViews.SetCollections(u.ID, view.ID, []int64{col.ID}); err != nil {
		t.Fatalf("set collections: %v", err)
	}

	scope := ItemFilter{
		CollectionID: col.ID,
		CollectionViews: []ViewFilter{
			{TagGroups: []TagGroup{{Tags: []string{"r/golang"}, All: true}}},
		},
	}
	items, _, err := s.Items.ListPage(u.ID, scope)
	if err != nil {
		t.Fatalf("list collection union: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("collection union should include the feed post and the view post: %+v", items)
	}
	if n, _ := s.Items.CountFiltered(u.ID, scope); n != 2 {
		t.Fatalf("collection union count = %d", n)
	}
	cats, err := s.Items.ListCategoriesFilter(u.ID, scope)
	if err != nil || len(cats) != 1 || cats[0].Category != "r/golang" {
		t.Fatalf("collection union categories = %+v, %v", cats, err)
	}
}
