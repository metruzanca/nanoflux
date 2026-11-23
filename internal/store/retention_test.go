package store

import (
	"database/sql"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/metruzanca/nanoflux/internal/db"
)

func retentionFeed(t *testing.T, s *Store, u User, authorID int64) Feed {
	t.Helper()
	url := fmt.Sprintf("https://example.dev/%d.xml", time.Now().UnixNano())
	f, err := s.Feeds.Create(u.ID, authorID, "Feed "+url, url, "", "", 900)
	if err != nil {
		t.Fatalf("create feed: %v", err)
	}
	return f
}

func retentionItem(t *testing.T, s *Store, feedID int64, guid string) int64 {
	t.Helper()
	res, err := s.Items.UpsertMany(feedID, []Item{{GUID: guid, Title: guid, FetchedAt: db.Now()}})
	if err != nil {
		t.Fatalf("upsert %s: %v", guid, err)
	}
	return res[0].ItemID
}

// markReadAt sets a read item's read_at directly so the sweep can be tested
// without waiting real days. at == "" leaves read_at NULL (a legacy read row).
func markRetentionReadAt(t *testing.T, s *Store, itemID int64, at string) {
	t.Helper()
	var readAt any
	if at != "" {
		readAt = at
	}
	if _, err := s.DB().Exec(`UPDATE items SET read = 1, read_at = ? WHERE id = ?`, readAt, itemID); err != nil {
		t.Fatalf("mark read at: %v", err)
	}
}

func setRetentionUnprotectedAt(t *testing.T, s *Store, itemID int64, at string) {
	t.Helper()
	if _, err := s.DB().Exec(`UPDATE items SET unprotected_at = ? WHERE id = ?`, at, itemID); err != nil {
		t.Fatalf("set unprotected_at: %v", err)
	}
}

func readRetentionUnprotectedAt(t *testing.T, s *Store, itemID int64) sql.NullString {
	t.Helper()
	var v sql.NullString
	if err := s.DB().QueryRow(`SELECT unprotected_at FROM items WHERE id = ?`, itemID).Scan(&v); err != nil {
		t.Fatalf("read unprotected_at: %v", err)
	}
	return v
}

func daysAgo(n int) string {
	return db.FormatTime(time.Now().AddDate(0, 0, -n))
}

func deletedIDs(t *testing.T, s *Store, userID int64) []int64 {
	t.Helper()
	if _, _, err := s.Items.DeleteOldReadItems(userID); err != nil {
		t.Fatalf("DeleteOldReadItems: %v", err)
	}
	var ids []int64
	rows, err := s.DB().Query(`SELECT id FROM items WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		t.Fatalf("list items: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

func hasID(ids []int64, want int64) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// TestDeleteOldReadItems covers the eligibility rules: the read_at clock,
// favorites/bookmarks/lists, legacy NULL read_at, the system feed, and the
// per-author override.
func TestDeleteOldReadItems(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, err := s.Authors.Create(u.ID, "A", "", "")
	if err != nil {
		t.Fatal(err)
	}
	f := retentionFeed(t, s, u, a.ID)
	other, err := s.Authors.Create(u.ID, "B", "", "")
	if err != nil {
		t.Fatal(err)
	}
	offFeed := retentionFeed(t, s, u, other.ID)

	if err := s.Users.SetAutoDeleteAfterDays(u.ID, 30); err != nil {
		t.Fatal(err)
	}

	old := retentionItem(t, s, f.ID, "old")
	recent := retentionItem(t, s, f.ID, "recent")
	unread := retentionItem(t, s, f.ID, "unread")
	fav := retentionItem(t, s, f.ID, "fav")
	bookmark := retentionItem(t, s, f.ID, "bookmark")
	listed := retentionItem(t, s, f.ID, "listed")
	legacy := retentionItem(t, s, f.ID, "legacy")
	offItem := retentionItem(t, s, offFeed.ID, "author-off")
	unassigned := retentionItem(t, s, retentionFeed(t, s, u, 0).ID, "unassigned")

	markRetentionReadAt(t, s, old, daysAgo(40))
	markRetentionReadAt(t, s, recent, daysAgo(2))
	markRetentionReadAt(t, s, fav, daysAgo(40))
	markRetentionReadAt(t, s, bookmark, daysAgo(40))
	markRetentionReadAt(t, s, listed, daysAgo(40))
	markRetentionReadAt(t, s, legacy, "")
	markRetentionReadAt(t, s, offItem, daysAgo(40))
	markRetentionReadAt(t, s, unassigned, daysAgo(40))
	_ = unread // stays unread

	if err := s.Items.SetFavorite(u.ID, fav, true); err != nil {
		t.Fatal(err)
	}
	if err := s.Items.SetBookmark(u.ID, bookmark, true); err != nil {
		t.Fatal(err)
	}
	l, err := s.Lists.Create(u.ID, "later")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Lists.AddItem(u.ID, l.ID, listed); err != nil {
		t.Fatal(err)
	}
	zero := 0
	if err := s.Authors.SetAutoDeleteAfterDays(u.ID, other.ID, &zero); err != nil {
		t.Fatal(err)
	}
	// A saved page lives on the hidden system feed and is never deleted.
	saved, _, err := s.SavePage(u.ID, 0, "page:https://example.dev/x", "saved", "https://example.dev/x", "", "")
	if err != nil {
		t.Fatal(err)
	}
	markRetentionReadAt(t, s, saved, daysAgo(40))

	left := deletedIDs(t, s, u.ID)

	if hasID(left, old) {
		t.Fatalf("old read item was not deleted")
	}
	if hasID(left, unassigned) {
		t.Fatalf("unassigned feed should inherit the account window and be deleted")
	}
	for name, id := range map[string]int64{
		"recent": recent, "unread": unread, "favorite": fav, "bookmark": bookmark,
		"listed": listed, "legacy": legacy, "author-off": offItem, "saved page": saved,
	} {
		if !hasID(left, id) {
			t.Fatalf("%s item %d should have survived", name, id)
		}
	}
}

// TestDeleteOldReadItemsGrace verifies that an item which just left a list,
// favorite or bookmark waits one full window, and that removing it earlier is
// harmless.
func TestDeleteOldReadItemsGrace(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, _ := s.Authors.Create(u.ID, "A", "", "")
	f := retentionFeed(t, s, u, a.ID)
	if err := s.Users.SetAutoDeleteAfterDays(u.ID, 30); err != nil {
		t.Fatal(err)
	}
	item := retentionItem(t, s, f.ID, "item")
	markRetentionReadAt(t, s, item, daysAgo(400))

	if err := s.Items.SetFavorite(u.ID, item, true); err != nil {
		t.Fatal(err)
	}
	if err := s.Items.SetFavorite(u.ID, item, false); err != nil {
		t.Fatal(err)
	}
	if got := readRetentionUnprotectedAt(t, s, item); !got.Valid || got.String == "" {
		t.Fatalf("unfavoriting should stamp unprotected_at, got %+v", got)
	}
	if left := deletedIDs(t, s, u.ID); !hasID(left, item) {
		t.Fatalf("just-unfavorited item was deleted before its grace window")
	}

	// Once the grace window has passed it is eligible again.
	setRetentionUnprotectedAt(t, s, item, daysAgo(40))
	if left := deletedIDs(t, s, u.ID); hasID(left, item) {
		t.Fatalf("item was not deleted after its grace window")
	}
}

// TestUnprotectedAtLifecycle covers the protection bookkeeping for favorites,
// bookmarks, list membership and list deletion.
func TestUnprotectedAtLifecycle(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, _ := s.Authors.Create(u.ID, "A", "", "")
	f := retentionFeed(t, s, u, a.ID)
	item := retentionItem(t, s, f.ID, "item")

	// Favorite on protects, off stamps.
	if err := s.Items.SetFavorite(u.ID, item, true); err != nil {
		t.Fatal(err)
	}
	if v := readRetentionUnprotectedAt(t, s, item); v.Valid {
		t.Fatalf("favorite should clear unprotected_at, got %+v", v)
	}
	if err := s.Items.SetFavorite(u.ID, item, false); err != nil {
		t.Fatal(err)
	}
	if v := readRetentionUnprotectedAt(t, s, item); !v.Valid {
		t.Fatalf("unfavorite should stamp unprotected_at")
	}

	// Bookmark on protects, off stamps.
	if err := s.Items.SetBookmark(u.ID, item, true); err != nil {
		t.Fatal(err)
	}
	if v := readRetentionUnprotectedAt(t, s, item); v.Valid {
		t.Fatalf("bookmark should clear unprotected_at, got %+v", v)
	}
	if err := s.Items.SetBookmark(u.ID, item, false); err != nil {
		t.Fatal(err)
	}
	if v := readRetentionUnprotectedAt(t, s, item); !v.Valid {
		t.Fatalf("unbookmark should stamp unprotected_at")
	}

	// List add protects, remove stamps, list delete stamps.
	l, err := s.Lists.Create(u.ID, "later")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Lists.AddItem(u.ID, l.ID, item); err != nil {
		t.Fatal(err)
	}
	if v := readRetentionUnprotectedAt(t, s, item); v.Valid {
		t.Fatalf("list add should clear unprotected_at, got %+v", v)
	}
	if err := s.Lists.RemoveItem(u.ID, l.ID, item); err != nil {
		t.Fatal(err)
	}
	if v := readRetentionUnprotectedAt(t, s, item); !v.Valid {
		t.Fatalf("list remove should stamp unprotected_at")
	}
	if err := s.Lists.AddItem(u.ID, l.ID, item); err != nil {
		t.Fatal(err)
	}
	if err := s.Lists.Delete(u.ID, l.ID); err != nil {
		t.Fatal(err)
	}
	if v := readRetentionUnprotectedAt(t, s, item); !v.Valid {
		t.Fatalf("list delete should stamp unprotected_at for former members")
	}
}

// TestDeleteOldReadItemsCacheKeys verifies the sweep returns every cached-media
// key of the items it removed, so the caller can purge the blobs.
func TestDeleteOldReadItemsCacheKeys(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, _ := s.Authors.Create(u.ID, "A", "", "")
	f := retentionFeed(t, s, u, a.ID)
	if err := s.Users.SetAutoDeleteAfterDays(u.ID, 30); err != nil {
		t.Fatal(err)
	}
	item := retentionItem(t, s, f.ID, "item")
	markRetentionReadAt(t, s, item, daysAgo(40))
	if _, err := s.DB().Exec(`UPDATE items SET image_cache_key = 'cache/feeds/1/0.jpg' WHERE id = ?`, item); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`INSERT INTO item_enclosures (item_id, url, cache_key) VALUES (?, 'https://example.dev/a.mp3', 'cache/feeds/1/1.mp3')`, item); err != nil {
		t.Fatal(err)
	}

	n, keys, err := s.Items.DeleteOldReadItems(u.ID)
	if err != nil {
		t.Fatalf("DeleteOldReadItems: %v", err)
	}
	if n != 1 {
		t.Fatalf("deleted = %d; want 1", n)
	}
	sort.Strings(keys)
	want := []string{"cache/feeds/1/0.jpg", "cache/feeds/1/1.mp3"}
	if len(keys) != len(want) {
		t.Fatalf("keys = %v; want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("keys = %v; want %v", keys, want)
		}
	}
}
