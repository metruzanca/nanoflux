package store

import (
	"testing"

	"github.com/metruzanca/nanoflux/internal/db"
)

// collectionCountsFixture builds a collection with one member feed holding two
// unread and one read item, plus a second feed (not in the collection) whose
// item a view can pull in.
func collectionCountsFixture(t *testing.T, s *Store) (User, Collection, Feed, Feed) {
	t.Helper()
	u := mustUser(t, s, "alice")
	a, err := s.Authors.Create(u.ID, "Metru", "", "")
	if err != nil {
		t.Fatal(err)
	}
	member, err := s.Feeds.Create(u.ID, a.ID, "Blog", "https://metru.dev/rss.xml", "", "", 900)
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.Feeds.Create(u.ID, a.ID, "Other", "https://other.dev/rss.xml", "", "", 900)
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.Collections.Create(u.ID, "Dev")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Collections.AddFeed(u.ID, c.ID, member.ID); err != nil {
		t.Fatal(err)
	}
	for _, it := range []Item{
		{GUID: "m1", Title: "m1", Link: "https://metru.dev/1", FetchedAt: db.Now()},
		{GUID: "m2", Title: "m2", Link: "https://metru.dev/2", FetchedAt: db.Now()},
		{GUID: "m3", Title: "m3", Link: "https://metru.dev/3", FetchedAt: db.Now()},
	} {
		if _, err := s.Items.Upsert(member.ID, it); err != nil {
			t.Fatal(err)
		}
	}
	items, _ := s.Items.List(u.ID, ItemFilter{})
	for _, it := range items {
		if it.GUID == "m3" {
			if err := s.Items.SetRead(u.ID, it.ID, true); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := s.Items.Upsert(other.ID, Item{
		GUID: "x1", Title: "x1", Link: "https://other.dev/1",
		Categories: []string{"u/example-poster"}, FetchedAt: db.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	return u, c, member, other
}

func TestCollectionCountsByFeed(t *testing.T) {
	s := newTestStore(t)
	u, c, _, _ := collectionCountsFixture(t, s)

	counts, err := s.Collections.CountsByFeed(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := counts[c.ID]; got.Unread != 2 || got.Read != 1 {
		t.Fatalf("collection counts = %+v, want unread 2 read 1", got)
	}
}

func TestCollectionCountsByScope(t *testing.T) {
	s := newTestStore(t)
	u, c, _, _ := collectionCountsFixture(t, s)

	pred := ViewFilter{
		AuthorID:  s.mustOnlyAuthor(t, u.ID),
		TagGroups: []TagGroup{{Tags: []string{"u/example-poster"}, All: true}},
	}
	scopes := []CollectionScope{{CollectionID: c.ID, Views: []ViewFilter{pred}}}
	counts, err := s.Collections.CountsByScope(u.ID, scopes)
	if err != nil {
		t.Fatal(err)
	}
	// The member feed contributes 2 unread and 1 read; the view adds one unread
	// item from a feed that is not a member.
	if got := counts[c.ID]; got.Unread != 3 || got.Read != 1 {
		t.Fatalf("scope counts = %+v, want unread 3 read 1", got)
	}

	// It must agree with the per-scope counts the collection page uses.
	wantUnread, _ := s.Items.CountFiltered(u.ID, ItemFilter{CollectionID: c.ID, CollectionViews: []ViewFilter{pred}, UnreadOnly: true})
	wantRead, _ := s.Items.CountFiltered(u.ID, ItemFilter{CollectionID: c.ID, CollectionViews: []ViewFilter{pred}, ReadOnly: true})
	if got := counts[c.ID]; got.Unread != wantUnread || got.Read != wantRead {
		t.Fatalf("scope counts = %+v, want %d/%d from CountFiltered", got, wantUnread, wantRead)
	}
}

func TestListForIndex(t *testing.T) {
	s := newTestStore(t)
	u, c, _, _ := collectionCountsFixture(t, s)
	auto, err := s.Collections.EnsureAuto(u.ID, "other.dev")
	if err != nil {
		t.Fatal(err)
	}
	views, err := s.SavedViews.Create(u.ID, SavedView{AuthorID: s.mustOnlyAuthor(t, u.ID), Title: "Example", Filters: []string{"tag:show:u/example-poster"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SavedViews.SetCollections(u.ID, views.ID, []int64{c.ID}); err != nil {
		t.Fatal(err)
	}

	rows, err := s.Collections.ListForIndex(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("collections = %+v, want 2", rows)
	}
	byID := map[int64]CollectionWithCounts{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	if got := byID[c.ID]; got.FeedCount != 1 || got.ViewCount != 1 || got.Unread != 0 || got.Read != 0 {
		t.Fatalf("user collection = %+v, want 1 feed, 1 view, no item counts", got)
	}
	if got := byID[auto.ID]; !got.IsAuto || got.FeedCount != 0 {
		t.Fatalf("auto collection = %+v, want an empty auto row", got)
	}
}

// TestSyncNoCollectionOnlyWritesWhenStale covers the drift check: the sync is a
// no-op while membership already matches, and it corrects every way the derived
// set can move.
func TestSyncNoCollectionOnlyWritesWhenStale(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, _ := s.Authors.Create(u.ID, "Metru", "", "")
	ungrouped, _ := s.Feeds.Create(u.ID, a.ID, "Blog", "https://metru.dev/rss.xml", "", "", 900)

	id, err := s.Collections.SyncNoCollection(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stale, err := s.Collections.noCollectionStale(u.ID, id); err != nil || stale {
		t.Fatalf("after a sync the collection should be in sync, stale=%v err=%v", stale, err)
	}

	// A new feed is ungrouped, so membership is now behind.
	grouped, _ := s.Feeds.Create(u.ID, a.ID, "Other", "https://other.dev/rss.xml", "", "", 900)
	if stale, _ := s.Collections.noCollectionStale(u.ID, id); !stale {
		t.Fatal("a new ungrouped feed should make the no-collection stale")
	}
	if _, err := s.Collections.SyncNoCollection(u.ID); err != nil {
		t.Fatal(err)
	}
	if stale, _ := s.Collections.noCollectionStale(u.ID, id); stale {
		t.Fatal("sync should clear the drift")
	}

	// Putting that feed in a user collection must remove it from the derived set.
	c, _ := s.Collections.Create(u.ID, "Dev")
	if err := s.Collections.AddFeed(u.ID, c.ID, grouped.ID); err != nil {
		t.Fatal(err)
	}
	if stale, _ := s.Collections.noCollectionStale(u.ID, id); !stale {
		t.Fatal("a feed moved into a collection should make the no-collection stale")
	}
	if _, err := s.Collections.SyncNoCollection(u.ID); err != nil {
		t.Fatal(err)
	}
	in, _ := s.Collections.Feeds(u.ID, id)
	if len(in) != 1 || in[0].ID != ungrouped.ID {
		t.Fatalf("no-collection members = %+v, want only the ungrouped feed", in)
	}
}

// mustOnlyAuthor returns the user's first author id, for building view
// predicates in store tests.
func (s *Store) mustOnlyAuthor(t *testing.T, userID int64) int64 {
	t.Helper()
	authors, err := s.Authors.List(userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(authors) == 0 {
		t.Fatal("fixture has no author")
	}
	return authors[0].ID
}
