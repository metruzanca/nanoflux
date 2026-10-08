package store

import (
	"testing"

	"github.com/metruzanca/nanoflux/internal/db"
)

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
