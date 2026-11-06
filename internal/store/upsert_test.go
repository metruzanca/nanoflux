package store

import (
	"testing"

	"github.com/metruzanca/nanoflux/internal/db"
)

// A stray row stored before cross keys existed must not block the cross-key
// upsert. It is folded into the keyed row instead of having the key assigned to
// it, which would violate the per-user cross-key unique index and fail the
// whole page.
func TestUpsertMergesStrayCrossDuplicate(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a1, _ := s.Authors.Create(u.ID, "r/cats", "", "")
	a2, _ := s.Authors.Create(u.ID, "sam", "", "")
	subFeed, _ := s.Feeds.Create(u.ID, a1.ID, "r/cats", "https://example.test/r/cats.rss", "", "", 900)
	userFeed, _ := s.Feeds.Create(u.ID, a2.ID, "u/sam", "https://example.test/u/sam.rss", "", "", 900)

	// Production creates the partial unique index at startup. Create it before
	// the fixtures so the regression actually exercises it.
	if _, err := s.Items.MergeCrossFeedDuplicates(false); err != nil {
		t.Fatalf("create cross index: %v", err)
	}

	post := Item{
		GUID: "t3_stray", SharedKey: "reddit:t3_stray", Title: "Post",
		Link:       "https://example.test/r/cats/comments/stray/post/",
		Categories: []string{"r/cats", "u/sam"}, FetchedAt: db.Now(),
	}
	if ins, err := s.Items.Upsert(subFeed.ID, post); err != nil || !ins {
		t.Fatalf("keyed ingest: inserted=%v err=%v", ins, err)
	}
	// The pre-cross-key duplicate, stored by the same user feed before it
	// derived a SharedKey. It carries read state that must survive the merge.
	if ins, err := s.Items.Upsert(userFeed.ID, Item{
		GUID: "t3_stray", Title: "Post", Read: true, FetchedAt: db.Now(),
	}); err != nil || !ins {
		t.Fatalf("stray ingest: inserted=%v err=%v", ins, err)
	}

	// Polling the user feed now resolves the keyed row and must merge the
	// stray instead of failing on the unique (user_id, cross_key) index.
	if ins, err := s.Items.Upsert(userFeed.ID, post); err != nil || ins {
		t.Fatalf("cross upsert after stray: inserted=%v err=%v", ins, err)
	}

	subItems, _ := s.Items.List(u.ID, ItemFilter{FeedID: subFeed.ID, Limit: 10})
	userItems, _ := s.Items.List(u.ID, ItemFilter{FeedID: userFeed.ID, Limit: 10})
	if len(subItems) != 1 || len(userItems) != 1 {
		t.Fatalf("want one item per feed, got sub=%d user=%d", len(subItems), len(userItems))
	}
	if subItems[0].ID != userItems[0].ID {
		t.Fatalf("both feeds should show the same row: %d vs %d", subItems[0].ID, userItems[0].ID)
	}
	if !subItems[0].Read {
		t.Fatalf("stray row's read state should carry to the keyed survivor")
	}
	// Idempotent: polling again keeps one row and is not new to the feed.
	if ins, err := s.Items.Upsert(userFeed.ID, post); err != nil || ins {
		t.Fatalf("re-upsert: inserted=%v err=%v", ins, err)
	}
	if items, _ := s.Items.List(u.ID, ItemFilter{Limit: 10}); len(items) != 1 {
		t.Fatalf("want one row total, got %d", len(items))
	}
}

// TestUpsertMany stores a page of items in one call, returning each id and
// insert flag, and is idempotent on a re-upsert.
func TestUpsertMany(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, err := s.Authors.Create(u.ID, "Metru", "", "author")
	if err != nil {
		t.Fatalf("create author: %v", err)
	}
	f, err := s.Feeds.Create(u.ID, a.ID, "f", "https://example.com/feed.xml", "", "", 900)
	if err != nil {
		t.Fatalf("create feed: %v", err)
	}

	items := []Item{
		{GUID: "g1", Title: "One", FetchedAt: db.Now()},
		{GUID: "g2", Title: "Two", FetchedAt: db.Now()},
		{GUID: "g3", Title: "Three", FetchedAt: db.Now()},
	}
	res, err := s.Items.UpsertMany(f.ID, items)
	if err != nil {
		t.Fatalf("upsert many: %v", err)
	}
	if len(res) != len(items) {
		t.Fatalf("results = %d, want %d", len(res), len(items))
	}
	seen := map[int64]bool{}
	for i, r := range res {
		if !r.Inserted {
			t.Fatalf("item %d not reported inserted", i)
		}
		if r.ItemID == 0 {
			t.Fatalf("item %d has no id", i)
		}
		if seen[r.ItemID] {
			t.Fatalf("duplicate id %d", r.ItemID)
		}
		seen[r.ItemID] = true
	}

	// Re-upserting the same items adds no membership and resolves to the same ids.
	res2, err := s.Items.UpsertMany(f.ID, items)
	if err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	for i, r := range res2 {
		if r.Inserted {
			t.Fatalf("item %d reported inserted on re-upsert", i)
		}
		if r.ItemID != res[i].ItemID {
			t.Fatalf("item %d id changed: %d -> %d", i, res[i].ItemID, r.ItemID)
		}
	}
}
