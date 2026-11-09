package store

import (
	"fmt"
	"testing"

	"github.com/metruzanca/nanoflux/internal/db"
)

// benchmarkStore builds a store with a user, authors, one feed per author, and
// itemsPerFeed items each. It inserts items in one transaction so the fixture is
// cheap to build.
func benchmarkStore(b *testing.B, authors, itemsPerFeed int) (*Store, int64, []int64) {
	b.Helper()
	sqldb, err := db.Open(":memory:")
	if err != nil {
		b.Fatalf("open: %v", err)
	}
	b.Cleanup(func() { sqldb.Close() })
	if err := db.Migrate(sqldb); err != nil {
		b.Fatalf("migrate: %v", err)
	}
	s := New(sqldb)
	u, err := s.Users.Create("alice", "hash")
	if err != nil {
		b.Fatalf("create user: %v", err)
	}

	authorIDs := make([]int64, 0, authors)
	feedIDs := make([]int64, 0, authors)
	for ai := 0; ai < authors; ai++ {
		a, err := s.Authors.Create(u.ID, fmt.Sprintf("author-%02d", ai), "", "")
		if err != nil {
			b.Fatalf("create author: %v", err)
		}
		authorIDs = append(authorIDs, a.ID)
		f, err := s.Feeds.Create(u.ID, a.ID, fmt.Sprintf("feed-%02d", ai),
			fmt.Sprintf("https://example%02d.dev/rss.xml", ai), "", "", 900)
		if err != nil {
			b.Fatalf("create feed: %v", err)
		}
		feedIDs = append(feedIDs, f.ID)
	}

	now := db.Now()
	tx, err := sqldb.Begin()
	if err != nil {
		b.Fatalf("begin: %v", err)
	}
	for ai, feedID := range feedIDs {
		for ii := 0; ii < itemsPerFeed; ii++ {
			guid := fmt.Sprintf("g-%02d-%05d", ai, ii)
			read := 0
			if ii%2 == 0 {
				read = 1
			}
			if _, err := tx.Exec(
				`INSERT INTO items (feed_id, user_id, guid, dedup_key, title, published_at, fetched_at, read)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				feedID, u.ID, guid, guid, "item", now, now, read,
			); err != nil {
				tx.Rollback()
				b.Fatalf("insert item: %v", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatalf("commit: %v", err)
	}
	return s, u.ID, authorIDs
}

func BenchmarkListAuthorsWithFeedCount(b *testing.B) {
	s, userID, _ := benchmarkStore(b, 20, 1000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.Authors.ListWithFeedCount(userID); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkLegacyListAuthorsWithFeedCount measures the pre-change author list:
// one join to visible_items with COUNT(DISTINCT), which multiplied rows per
// author and made the unread count part of the same scan.
func BenchmarkLegacyListAuthorsWithFeedCount(b *testing.B) {
	s, userID, _ := benchmarkStore(b, 20, 1000)
	const legacy = `
SELECT a.id, a.user_id, a.name, a.avatar_url, a.avatar_key, a.last_fetched_at, a.description, a.is_system, a.auto_read_after_days, a.created_at,
       COUNT(DISTINCT f.id) AS feed_count,
       COUNT(DISTINCT i.id) AS unread_count,
       (SELECT COALESCE(MAX(f2.rank), 0) FROM feeds f2
          WHERE f2.author_id = a.id AND f2.user_id = a.user_id) AS max_rank,
       (SELECT COUNT(*) FROM items i2 JOIN feeds f2 ON f2.id = i2.feed_id
          WHERE f2.author_id = a.id AND f2.user_id = a.user_id AND i2.favorite = 1) AS fav_count
FROM authors a
LEFT JOIN feeds f ON f.author_id = a.id AND f.user_id = a.user_id
LEFT JOIN visible_items i ON i.feed_id = f.id AND i.read = 0
WHERE a.user_id = ? AND a.is_system = 0
GROUP BY a.id
ORDER BY a.name`
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows, err := s.DB().Query(legacy, userID)
		if err != nil {
			b.Fatal(err)
		}
		for rows.Next() {
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkUnreadCountsByAuthor(b *testing.B) {
	s, userID, _ := benchmarkStore(b, 20, 1000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.Items.UnreadCountsByAuthor(userID); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkUnreadCountsByAuthorScope(b *testing.B) {
	s, userID, authorIDs := benchmarkStore(b, 20, 1000)
	scopes := make([]AuthorScope, 0, 3)
	for i := 0; i < 3; i++ {
		scopes = append(scopes, AuthorScope{
			AuthorID: authorIDs[i],
			Views:    []ViewFilter{{AuthorID: authorIDs[i], TagGroups: []TagGroup{{Tags: []string{"bench"}}}}},
		})
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.Items.UnreadCountsByAuthorScope(userID, scopes); err != nil {
			b.Fatal(err)
		}
	}
}
