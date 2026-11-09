package store

import (
	"fmt"
	"testing"

	"github.com/metruzanca/nanoflux/internal/db"
)

func BenchmarkUpsertManyUnchanged(b *testing.B) {
	s, userID, authorIDs := benchmarkStore(b, 1, 100)
	f, err := s.Feeds.Create(userID, authorIDs[0], "re-poll", "https://re.dev/rss.xml", "", "", 900)
	if err != nil {
		b.Fatalf("create feed: %v", err)
	}
	items := make([]Item, 100)
	now := db.Now()
	for i := range items {
		items[i] = Item{
			GUID:      fmt.Sprintf("re-%05d", i),
			Title:     "item",
			Link:      fmt.Sprintf("https://re.dev/%d", i),
			Summary:   "plain summary",
			FetchedAt: now,
		}
	}
	if _, err := s.Items.UpsertMany(f.ID, items); err != nil {
		b.Fatalf("seed upsert: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.Items.UpsertMany(f.ID, items); err != nil {
			b.Fatal(err)
		}
	}
}
