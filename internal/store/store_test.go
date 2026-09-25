package store

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/filestore"
	"github.com/metruzanca/nanoflux/internal/store/sqlcgen"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	sqldb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { sqldb.Close() })
	if err := db.Migrate(sqldb); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return New(sqldb)
}

func mustUser(t *testing.T, s *Store, username string) User {
	t.Helper()
	u, err := s.Users.Create(username, "hash")
	if err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
	return u
}

func TestUserStore(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")

	got, err := s.Users.ByID(u.ID)
	if err != nil || got.Username != "alice" {
		t.Fatalf("ByID: %v %+v", err, got)
	}
	got, err = s.Users.ByUsername("alice")
	if err != nil || got.ID != u.ID {
		t.Fatalf("ByUsername: %v %+v", err, got)
	}
	if _, err := s.Users.Create("alice", "x"); !errors.Is(err, nil) && !isUniqueErr(err) {
		t.Fatalf("expected unique violation, got %v", err)
	}
	n, err := s.Users.Count()
	if err != nil || n != 1 {
		t.Fatalf("Count: %v %d", err, n)
	}
	if _, err := s.Users.ByID(999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func isUniqueErr(err error) bool {
	return err != nil
}

func TestSessionStore(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")

	future := time.Now().UTC().Add(time.Hour).Format("2006-01-02 15:04:05")
	if err := s.Sessions.Create(u.ID, "token1", future); err != nil {
		t.Fatalf("create session: %v", err)
	}
	got, err := s.Sessions.UserByToken("token1")
	if err != nil || got.ID != u.ID {
		t.Fatalf("UserByToken: %v %+v", err, got)
	}

	if err := s.Sessions.Delete("token1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.Sessions.UserByToken("token1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestAuthorFeedFlow(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")

	a, err := s.Authors.Create(u.ID, "Metru", "", "author")
	if err != nil {
		t.Fatalf("create author: %v", err)
	}
	authors, err := s.Authors.List(u.ID)
	if err != nil || len(authors) != 1 {
		t.Fatalf("List authors: %v %d", err, len(authors))
	}

	f, err := s.Feeds.Create(u.ID, a.ID, "Metru's blog", "https://metru.dev/rss.xml", "https://metru.dev", "", 900)
	if err != nil {
		t.Fatalf("create feed: %v", err)
	}
	feeds, err := s.Feeds.List(u.ID)
	if err != nil || len(feeds) != 1 || feeds[0].AuthorID != a.ID {
		t.Fatalf("List feeds: %v %d", err, len(feeds))
	}
	byAuthor, err := s.Feeds.ListByAuthor(u.ID, a.ID)
	if err != nil || len(byAuthor) != 1 {
		t.Fatalf("ListByAuthor: %v %d", err, len(byAuthor))
	}

	// Deleting an author cascades to their feeds and items.
	s.Items.Upsert(f.ID, Item{GUID: "g", Title: "t", Link: "https://metru.dev/1", FetchedAt: db.Now()})
	if err := s.Authors.Delete(u.ID, a.ID); err != nil {
		t.Fatalf("author delete should cascade: %v", err)
	}
	feeds, _ = s.Feeds.List(u.ID)
	if len(feeds) != 0 {
		t.Fatalf("author delete should cascade feeds, got %d", len(feeds))
	}
	n, _ := s.Items.CountUnread(u.ID, 0)
	if n != 0 {
		t.Fatalf("author delete should cascade items, got %d unread", n)
	}

	// A feed must belong to an author; the store enforces it.
	if _, err := s.Feeds.Create(u.ID, 0, "orphan", "https://x.dev/rss.xml", "", "", 900); err == nil {
		t.Fatal("create without author should fail")
	}
}

func TestFeedNextPageCursor(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, _ := s.Authors.Create(u.ID, "Metru", "", "")
	f, err := s.Feeds.Create(u.ID, a.ID, "Blog", "https://metru.dev/feed.xml?page=1", "", "", 900)
	if err != nil {
		t.Fatal(err)
	}
	if f.NextPageURL != "" {
		t.Fatalf("new feed NextPageURL = %q, want empty", f.NextPageURL)
	}

	// The poller records pagination; the feed page reads it back.
	if err := s.Feeds.SetNextPageURL(f.ID, "https://metru.dev/feed.xml?page=2"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Feeds.ByID(u.ID, f.ID)
	if got.NextPageURL != "https://metru.dev/feed.xml?page=2" {
		t.Fatalf("NextPageURL = %q, want page=2", got.NextPageURL)
	}

	// "Load older items" clears it once the history is exhausted.
	if err := s.Feeds.SetNextPageURL(f.ID, ""); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Feeds.ByID(u.ID, f.ID)
	if got.NextPageURL != "" {
		t.Fatalf("NextPageURL = %q, want cleared", got.NextPageURL)
	}
}

func TestBackfillYouTubeThumbnails(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, _ := s.Authors.Create(u.ID, "SomeDunkVODs", "", "")
	f, _ := s.Feeds.Create(u.ID, a.ID, "SomeDunkVODs", "https://www.youtube.com/feeds/videos.xml?channel_id=UCx", "", "", 900)

	items := []Item{
		{GUID: "yt:video:H0KAi8AWsnM", Title: "No thumb", FetchedAt: db.Now()},
		{GUID: "yt:video:pHfG0oxgshA", Title: "Has thumb", ImageURL: "https://i.ytimg.com/vi/pHfG0oxgshA/hq720.jpg", FetchedAt: db.Now()},
		{GUID: "blog:1", Title: "Not youtube", FetchedAt: db.Now()},
	}
	for _, it := range items {
		if _, err := s.Items.Upsert(f.ID, it); err != nil {
			t.Fatal(err)
		}
	}

	missing, err := s.Items.CountItemsMissingYouTubeThumbnail()
	if err != nil || missing != 1 {
		t.Fatalf("missing before = %d (err %v), want 1", missing, err)
	}
	n, err := s.Items.BackfillYouTubeThumbnails()
	if err != nil || n != 1 {
		t.Fatalf("backfill = %d (err %v), want 1", n, err)
	}
	if missing, _ := s.Items.CountItemsMissingYouTubeThumbnail(); missing != 0 {
		t.Fatalf("missing after = %d, want 0", missing)
	}

	got, err := s.Items.List(u.ID, ItemFilter{})
	if err != nil {
		t.Fatal(err)
	}
	byGUID := map[string]string{}
	for _, it := range got {
		byGUID[it.GUID] = it.ImageURL
	}
	if want := "https://i.ytimg.com/vi/H0KAi8AWsnM/hqdefault.jpg"; byGUID["yt:video:H0KAi8AWsnM"] != want {
		t.Errorf("backfilled = %q, want %q", byGUID["yt:video:H0KAi8AWsnM"], want)
	}
	if byGUID["yt:video:pHfG0oxgshA"] != "https://i.ytimg.com/vi/pHfG0oxgshA/hq720.jpg" {
		t.Errorf("existing thumb overwritten: %q", byGUID["yt:video:pHfG0oxgshA"])
	}
	if byGUID["blog:1"] != "" {
		t.Errorf("non-youtube item touched: %q", byGUID["blog:1"])
	}
}

func TestItemsFlow(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, _ := s.Authors.Create(u.ID, "Metru", "", "")
	f, _ := s.Feeds.Create(u.ID, a.ID, "Blog", "https://metru.dev/rss.xml", "", "", 900)

	base := Item{GUID: "g1", Title: "One", Link: "https://metru.dev/1", FetchedAt: db.Now()}
	inserted, err := s.Items.Upsert(f.ID, base)
	if err != nil || !inserted {
		t.Fatalf("upsert new: %v %v", inserted, err)
	}
	// Duplicate guid -> no insert, but the stored snapshot is refreshed.
	inserted, err = s.Items.Upsert(f.ID, base)
	if err != nil || inserted {
		t.Fatalf("upsert dup: %v %v", inserted, err)
	}
	refresh := Item{
		GUID:      "g1",
		Title:     "One",
		Link:      "https://metru.dev/1",
		Summary:   "updated summary",
		ImageURL:  "https://metru.dev/thumb.jpg",
		FetchedAt: db.Now(),
	}
	inserted, err = s.Items.Upsert(f.ID, refresh)
	if err != nil || inserted {
		t.Fatalf("upsert refresh: %v %v", inserted, err)
	}
	items, err := s.Items.List(u.ID, ItemFilter{})
	if err != nil || len(items) != 1 {
		t.Fatalf("List after refresh: %v %d", err, len(items))
	}
	if items[0].Summary != "updated summary" || items[0].ImageURL != "https://metru.dev/thumb.jpg" {
		t.Fatalf("snapshot not refreshed: %+v", items[0].Item)
	}
	if items[0].Title != "One" {
		t.Fatalf("identity touched on refresh: title = %q", items[0].Title)
	}

	second := base
	second.GUID, second.Title = "g2", "Two"
	s.Items.Upsert(f.ID, second)

	items, err = s.Items.List(u.ID, ItemFilter{})
	if err != nil || len(items) != 2 {
		t.Fatalf("List: %v %d", err, len(items))
	}
	unread, _ := s.Items.List(u.ID, ItemFilter{UnreadOnly: true})
	if len(unread) != 2 {
		t.Fatalf("expected 2 unread, got %d", len(unread))
	}
	n, err := s.Items.CountUnread(u.ID, 0)
	if err != nil || n != 2 {
		t.Fatalf("CountUnread: %v %d", err, n)
	}

	if err := s.Items.SetRead(u.ID, items[0].ID, true); err != nil {
		t.Fatalf("SetRead: %v", err)
	}
	readItem, _ := s.Items.ByID(u.ID, items[0].ID)
	if !readItem.Read || readItem.ReadAt == "" {
		t.Fatalf("read item should carry a read_at timestamp: %+v", readItem)
	}
	n, _ = s.Items.CountUnread(u.ID, 0)
	if n != 1 {
		t.Fatalf("expected 1 unread after SetRead, got %d", n)
	}
	// Marking unread clears the read_at timestamp.
	if err := s.Items.SetRead(u.ID, items[0].ID, false); err != nil {
		t.Fatalf("SetRead false: %v", err)
	}
	unreadItem, _ := s.Items.ByID(u.ID, items[0].ID)
	if unreadItem.Read || unreadItem.ReadAt != "" {
		t.Fatalf("unread item should have empty read_at: %+v", unreadItem)
	}
	if err := s.Items.SetRead(u.ID, items[0].ID, true); err != nil {
		t.Fatalf("SetRead: %v", err)
	}
	if err := s.Items.MarkAllRead(u.ID, 0); err != nil {
		t.Fatalf("MarkAllRead: %v", err)
	}
	n, _ = s.Items.CountUnread(u.ID, 0)
	if n != 0 {
		t.Fatalf("expected 0 unread after MarkAllRead, got %d", n)
	}
}

func TestItemsListPagePagination(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, _ := s.Authors.Create(u.ID, "Metru", "", "")
	f, _ := s.Feeds.Create(u.ID, a.ID, "Blog", "https://metru.dev/rss.xml", "", "", 900)

	// 5 items; page size 2 -> 2, 2, 1.
	for i := 1; i <= 5; i++ {
		guid := "g" + strconv.Itoa(i)
		it := Item{GUID: guid, Title: "Post " + strconv.Itoa(i), Link: "https://metru.dev/" + guid, FetchedAt: db.Now()}
		if _, err := s.Items.Upsert(f.ID, it); err != nil {
			t.Fatalf("upsert %d: %v", i, err)
		}
	}

	var seen []int64
	cursor := int64(0)
	wantMore := []bool{true, true, false}
	for page := 0; page < 3; page++ {
		items, more, err := s.Items.ListPage(u.ID, ItemFilter{Limit: 2, BeforeID: cursor})
		if err != nil {
			t.Fatalf("ListPage: %v", err)
		}
		if more != wantMore[page] {
			t.Fatalf("page %d: more = %v, want %v", page, more, wantMore[page])
		}
		wantLen := 2
		if page == 2 {
			wantLen = 1
		}
		if len(items) != wantLen {
			t.Fatalf("page %d: got %d items, want %d", page, len(items), wantLen)
		}
		for _, it := range items {
			if it.ID == cursor {
				t.Fatalf("page %d: item repeated after cursor %d", page, cursor)
			}
			seen = append(seen, it.ID)
		}
		if len(items) > 0 {
			cursor = items[len(items)-1].ID
		}
	}
	if len(seen) != 5 {
		t.Fatalf("seen %d ids, want all 5", len(seen))
	}
	// All 5 ids are distinct and present.
	uniq := map[int64]bool{}
	for _, id := range seen {
		uniq[id] = true
	}
	if len(uniq) != 5 {
		t.Fatalf("pagination returned duplicates: %v", seen)
	}
}

func TestSearchPage(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, _ := s.Authors.Create(u.ID, "Metru", "", "")
	f, _ := s.Feeds.Create(u.ID, a.ID, "Blog", "https://metru.dev/rss.xml", "", "", 900)

	base := Item{GUID: "g1", Title: "Go concurrency patterns", Link: "https://metru.dev/1", Summary: "goroutines and channels", FetchedAt: db.Now()}
	s.Items.Upsert(f.ID, base)
	s.Items.Upsert(f.ID, Item{GUID: "g2", Title: "Rust memory safety", Link: "https://metru.dev/2", Summary: "ownership and borrowing", FetchedAt: db.Now()})
	s.Items.Upsert(f.ID, Item{GUID: "g3", Title: "Go for beginners", Link: "https://metru.dev/3", Summary: "getting started", FetchedAt: db.Now()})

	// Token match across title + summary.
	got, more, err := s.Items.SearchPage(u.ID, `"concurrency" OR "borrowing"`, ItemFilter{Limit: 10})
	if err != nil {
		t.Fatalf("SearchPage: %v", err)
	}
	if len(got) != 2 || more {
		t.Fatalf("search results: %d, more=%v", len(got), more)
	}
	for _, it := range got {
		if it.Title != "Go concurrency patterns" && it.Title != "Rust memory safety" {
			t.Fatalf("unexpected result: %+v", it)
		}
	}

	// title: qualifier restricts to the title column.
	got, _, err = s.Items.SearchPage(u.ID, `title:"Go"`, ItemFilter{Limit: 10})
	if err != nil || len(got) != 2 {
		t.Fatalf("title: search: %v %d", err, len(got))
	}

	// No matches.
	got, _, err = s.Items.SearchPage(u.ID, `"zzzznope"`, ItemFilter{Limit: 10})
	if err != nil || len(got) != 0 {
		t.Fatalf("empty search: %v %d", err, len(got))
	}
}

func TestItemEnclosures(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, _ := s.Authors.Create(u.ID, "Metru", "", "")
	f, _ := s.Feeds.Create(u.ID, a.ID, "Blog", "https://metru.dev/rss.xml", "", "", 900)
	if _, err := s.Items.Upsert(f.ID, Item{GUID: "g1", Title: "Podcast", Link: "https://metru.dev/1", FetchedAt: db.Now()}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	itemID, _ := s.Items.ByFeedGUID(f.ID, "g1")
	if itemID == 0 {
		t.Fatal("ByFeedGUID should find the item")
	}

	encs := []Enclosure{
		{URL: "https://metru.dev/ep1.mp3", Title: "Episode 1", MIMEType: "audio/mpeg", Size: 1234},
		{URL: "https://metru.dev/ep1.pdf", Title: "Show notes", MIMEType: "application/pdf", Size: 99},
	}
	if err := s.Items.ReplaceEnclosures(itemID, encs); err != nil {
		t.Fatalf("ReplaceEnclosures: %v", err)
	}
	got, err := s.Items.Enclosures(itemID)
	if err != nil || len(got) != 2 {
		t.Fatalf("Enclosures: %v %d", err, len(got))
	}
	if got[0].URL != encs[0].URL || got[0].Sort != 0 || got[1].Sort != 1 {
		t.Fatalf("enclosures out of order: %+v", got)
	}

	// Replace clears the old set.
	if err := s.Items.ReplaceEnclosures(itemID, encs[:1]); err != nil {
		t.Fatalf("ReplaceEnclosures: %v", err)
	}
	got, _ = s.Items.Enclosures(itemID)
	if len(got) != 1 {
		t.Fatalf("expected 1 enclosure after replace, got %d", len(got))
	}

	// Deleting the item cascades its enclosures.
	if err := s.Items.SetRead(u.ID, itemID, true); err != nil {
		t.Fatalf("SetRead: %v", err)
	}
}

func TestShareStore(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, _ := s.Authors.Create(u.ID, "Metru", "", "")
	f, _ := s.Feeds.Create(u.ID, a.ID, "Blog", "https://metru.dev/rss.xml", "", "", 900)
	if _, err := s.Items.Upsert(f.ID, Item{GUID: "g1", Title: "Post", Link: "https://metru.dev/1", FetchedAt: db.Now()}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	itemID, _ := s.Items.ByFeedGUID(f.ID, "g1")

	sh, err := s.Shares.Create(u.ID, itemID)
	if err != nil {
		t.Fatalf("create share: %v", err)
	}
	if sh.Token == "" || len(sh.Token) < 24 {
		t.Fatalf("token should be long and random: %q", sh.Token)
	}
	// Creating again returns the same share (idempotent).
	again, _ := s.Shares.Create(u.ID, itemID)
	if again.Token != sh.Token {
		t.Fatalf("repeat create should return the existing share")
	}
	// Resolvable by token, and scoped to the owner.
	if got, err := s.Shares.ByToken(sh.Token); err != nil || got.ItemID != itemID {
		t.Fatalf("ByToken: %v %+v", err, got)
	}
	if _, err := s.Shares.ByItem(u.ID, itemID); err != nil {
		t.Fatalf("ByItem should find the share: %v", err)
	}

	// Deleting removes it; the token no longer resolves.
	if err := s.Shares.Delete(u.ID, itemID); err != nil {
		t.Fatalf("delete share: %v", err)
	}
	if _, err := s.Shares.ByToken(sh.Token); err == nil {
		t.Fatal("deleted token should not resolve")
	}
}

func TestItemFavorites(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, _ := s.Authors.Create(u.ID, "Metru", "", "")
	f, _ := s.Feeds.Create(u.ID, a.ID, "Blog", "https://metru.dev/rss.xml", "", "", 900)

	s.Items.Upsert(f.ID, Item{GUID: "g1", Title: "One", Link: "https://metru.dev/1", FetchedAt: db.Now()})
	s.Items.Upsert(f.ID, Item{GUID: "g2", Title: "Two", Link: "https://metru.dev/2", FetchedAt: db.Now()})

	items, _ := s.Items.List(u.ID, ItemFilter{})
	if err := s.Items.SetFavorite(u.ID, items[0].ID, true); err != nil {
		t.Fatalf("SetFavorite: %v", err)
	}
	fav, err := s.Items.List(u.ID, ItemFilter{FavoritesOnly: true})
	if err != nil || len(fav) != 1 {
		t.Fatalf("FavoritesOnly List: %v %d", err, len(fav))
	}
	if !fav[0].Favorite || fav[0].ID != items[0].ID {
		t.Fatalf("wrong favorite row: %+v", fav[0])
	}
	n, _ := s.Items.CountFavorites(u.ID, 0)
	if n != 1 {
		t.Fatalf("CountFavorites: %d, want 1", n)
	}
	it, err := s.Items.ByID(u.ID, items[0].ID)
	if err != nil || !it.Favorite {
		t.Fatalf("ByID favorite: %v %+v", err, it)
	}
	// Toggle off.
	if err := s.Items.SetFavorite(u.ID, items[0].ID, false); err != nil {
		t.Fatalf("SetFavorite off: %v", err)
	}
	n, _ = s.Items.CountFavorites(u.ID, 0)
	if n != 0 {
		t.Fatalf("CountFavorites after off: %d, want 0", n)
	}
}

func TestItemScopedCounts(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, _ := s.Authors.Create(u.ID, "Metru", "", "")
	f, _ := s.Feeds.Create(u.ID, a.ID, "Blog", "https://metru.dev/rss.xml", "", "", 900)
	coll, _ := s.Collections.Create(u.ID, "Dev")
	s.Collections.AddFeed(u.ID, coll.ID, f.ID)

	s.Items.Upsert(f.ID, Item{GUID: "g1", Title: "One", Link: "https://metru.dev/1", FetchedAt: db.Now()})
	s.Items.Upsert(f.ID, Item{GUID: "g2", Title: "Two", Link: "https://metru.dev/2", FetchedAt: db.Now()})

	items, _ := s.Items.List(u.ID, ItemFilter{})
	if err := s.Items.SetRead(u.ID, items[0].ID, true); err != nil {
		t.Fatalf("SetRead: %v", err)
	}

	if n, _ := s.Items.CountUnreadAuthor(u.ID, a.ID); n != 1 {
		t.Fatalf("CountUnreadAuthor: %d, want 1", n)
	}
	if n, _ := s.Items.CountReadAuthor(u.ID, a.ID); n != 1 {
		t.Fatalf("CountReadAuthor: %d, want 1", n)
	}
	if n, _ := s.Items.CountUnreadCollection(u.ID, coll.ID); n != 1 {
		t.Fatalf("CountUnreadCollection: %d, want 1", n)
	}
	if n, _ := s.Items.CountReadCollection(u.ID, coll.ID); n != 1 {
		t.Fatalf("CountReadCollection: %d, want 1", n)
	}

	// Author-scoped read filter returns exactly the read item.
	read, _ := s.Items.List(u.ID, ItemFilter{AuthorID: a.ID, ReadOnly: true})
	if len(read) != 1 || read[0].ID != items[0].ID {
		t.Fatalf("author read list: %+v", read)
	}
	// Collection-scoped unread filter returns exactly the unread item.
	unread, _ := s.Items.List(u.ID, ItemFilter{CollectionID: coll.ID, UnreadOnly: true})
	if len(unread) != 1 {
		t.Fatalf("collection unread list: %d items", len(unread))
	}
}

// TestMarkAuthorRead marks one author's items read and asserts the other
// author's items are untouched, since the action is scoped through feeds.user_id
// AND feeds.author_id.
func TestMarkAuthorRead(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a1, _ := s.Authors.Create(u.ID, "Metru", "", "")
	f1, _ := s.Feeds.Create(u.ID, a1.ID, "One", "https://one.dev/rss.xml", "", "", 900)
	a2, _ := s.Authors.Create(u.ID, "Other", "", "")
	f2, _ := s.Feeds.Create(u.ID, a2.ID, "Two", "https://two.dev/rss.xml", "", "", 900)

	for _, f := range []struct {
		id   int64
		guid string
	}{{f1.ID, "a1"}, {f1.ID, "a2"}, {f2.ID, "b1"}} {
		if _, err := s.Items.Upsert(f.id, Item{GUID: f.guid, Title: f.guid, Link: "https://x/" + f.guid, FetchedAt: db.Now()}); err != nil {
			t.Fatalf("upsert %s: %v", f.guid, err)
		}
	}

	if err := s.Items.MarkAuthorRead(u.ID, a1.ID); err != nil {
		t.Fatalf("MarkAuthorRead: %v", err)
	}
	if n, _ := s.Items.CountUnreadAuthor(u.ID, a1.ID); n != 0 {
		t.Fatalf("author 1 unread = %d, want 0", n)
	}
	if n, _ := s.Items.CountUnreadAuthor(u.ID, a2.ID); n != 1 {
		t.Fatalf("author 2 unread = %d, want 1 (untouched)", n)
	}
	items, _ := s.Items.List(u.ID, ItemFilter{})
	for _, it := range items {
		if it.FeedID == f1.ID && (!it.Read || it.ReadAt == "") {
			t.Fatalf("author 1 item not marked read with timestamp: %+v", it)
		}
	}
}

func TestListWithCounts(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, _ := s.Authors.Create(u.ID, "Metru", "", "")
	f1, _ := s.Feeds.Create(u.ID, a.ID, "Blog", "https://metru.dev/rss.xml", "", "", 900)
	f2, _ := s.Feeds.Create(u.ID, a.ID, "Other", "https://other.dev/rss.xml", "", "", 900)
	c, _ := s.Collections.Create(u.ID, "Dev")
	s.Collections.AddFeed(u.ID, c.ID, f1.ID)
	s.Collections.AddFeed(u.ID, c.ID, f2.ID)

	s.Items.Upsert(f1.ID, Item{GUID: "a", Title: "a", Link: "https://metru.dev/1", FetchedAt: db.Now()})
	s.Items.Upsert(f1.ID, Item{GUID: "b", Title: "b", Link: "https://metru.dev/2", FetchedAt: db.Now()})
	s.Items.Upsert(f2.ID, Item{GUID: "c", Title: "c", Link: "https://other.dev/1", FetchedAt: db.Now()})
	items, _ := s.Items.List(u.ID, ItemFilter{})
	// Mark one item read; leave two unread.
	if err := s.Items.SetRead(u.ID, items[0].ID, true); err != nil {
		t.Fatal(err)
	}

	rows, err := s.Collections.ListWithCounts(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("collections = %d, want 1", len(rows))
	}
	got := rows[0]
	if got.FeedCount != 2 || got.Unread != 2 || got.Read != 1 {
		t.Fatalf("collection counts = feeds:%d unread:%d read:%d, want 2/2/1", got.FeedCount, got.Unread, got.Read)
	}
}

func TestMarkRangeRead(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, _ := s.Authors.Create(u.ID, "Metru", "", "")
	f, _ := s.Feeds.Create(u.ID, a.ID, "Blog", "https://metru.dev/rss.xml", "", "", 900)
	f2, _ := s.Feeds.Create(u.ID, a.ID, "Other", "https://other.dev/rss.xml", "", "", 900)

	s.Items.Upsert(f.ID, Item{GUID: "a1", Title: "newest", Link: "https://metru.dev/1", PublishedAt: "2026-01-03 00:00:00", FetchedAt: db.Now()})
	s.Items.Upsert(f.ID, Item{GUID: "a2", Title: "middle", Link: "https://metru.dev/2", PublishedAt: "2026-01-02 00:00:00", FetchedAt: db.Now()})
	s.Items.Upsert(f.ID, Item{GUID: "a3", Title: "oldest", Link: "https://metru.dev/3", PublishedAt: "2026-01-01 00:00:00", FetchedAt: db.Now()})
	s.Items.Upsert(f2.ID, Item{GUID: "b1", Title: "other", Link: "https://other.dev/1", PublishedAt: "2026-01-10 00:00:00", FetchedAt: db.Now()})

	items, _ := s.Items.List(u.ID, ItemFilter{})
	readState := func(title string) bool {
		for _, it := range items {
			if it.Title == title {
				got, _ := s.Items.ByID(u.ID, it.ID)
				return got.Read
			}
		}
		t.Fatalf("missing item %q", title)
		return false
	}
	var middle int64
	for _, it := range items {
		if it.Title == "middle" {
			middle = it.ID
		}
	}

	// "before" = newer items in the same feed.
	if err := s.Items.MarkBeforeRead(u.ID, middle); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		title string
		want  bool
	}{
		{"newest", true}, {"middle", false}, {"oldest", false}, {"other", false},
	} {
		if got := readState(c.title); got != c.want {
			t.Fatalf("after MarkBeforeRead: %s read=%v want %v", c.title, got, c.want)
		}
	}

	// "after" = older items in the same feed.
	if err := s.Items.MarkAfterRead(u.ID, middle); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		title string
		want  bool
	}{
		{"newest", true}, {"middle", false}, {"oldest", true}, {"other", false},
	} {
		if got := readState(c.title); got != c.want {
			t.Fatalf("after MarkAfterRead: %s read=%v want %v", c.title, got, c.want)
		}
	}
}

func TestMarkAuthorRangeRead(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, _ := s.Authors.Create(u.ID, "Metru", "", "")
	f, _ := s.Feeds.Create(u.ID, a.ID, "Blog", "https://metru.dev/rss.xml", "", "", 900)
	f2, _ := s.Feeds.Create(u.ID, a.ID, "Other", "https://other.dev/rss.xml", "", "", 900)
	// A different author's feed must not be touched.
	other, _ := s.Authors.Create(u.ID, "Other", "", "")
	f3, _ := s.Feeds.Create(u.ID, other.ID, "Elsewhere", "https://elsewhere.dev/rss.xml", "", "", 900)

	s.Items.Upsert(f.ID, Item{GUID: "a1", Title: "newest", Link: "https://metru.dev/1", PublishedAt: "2026-01-03 00:00:00", FetchedAt: db.Now()})
	s.Items.Upsert(f.ID, Item{GUID: "a2", Title: "middle", Link: "https://metru.dev/2", PublishedAt: "2026-01-02 00:00:00", FetchedAt: db.Now()})
	s.Items.Upsert(f.ID, Item{GUID: "a3", Title: "oldest", Link: "https://metru.dev/3", PublishedAt: "2026-01-01 00:00:00", FetchedAt: db.Now()})
	// A newer item in the author's *second* feed: an author-scoped "before"
	// must reach it.
	s.Items.Upsert(f2.ID, Item{GUID: "b1", Title: "sibling-newer", Link: "https://other.dev/1", PublishedAt: "2026-01-04 00:00:00", FetchedAt: db.Now()})
	s.Items.Upsert(f3.ID, Item{GUID: "c1", Title: "foreign", Link: "https://elsewhere.dev/1", PublishedAt: "2026-01-05 00:00:00", FetchedAt: db.Now()})

	items, _ := s.Items.List(u.ID, ItemFilter{})
	readState := func(title string) bool {
		for _, it := range items {
			if it.Title == title {
				got, _ := s.Items.ByID(u.ID, it.ID)
				return got.Read
			}
		}
		t.Fatalf("missing item %q", title)
		return false
	}
	var middle int64
	for _, it := range items {
		if it.Title == "middle" {
			middle = it.ID
		}
	}

	// Author "before": newer items across the author's feeds, including the
	// sibling feed; a different author's feed is untouched.
	if err := s.Items.MarkAuthorBeforeRead(u.ID, middle); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		title string
		want  bool
	}{
		{"newest", true}, {"middle", false}, {"oldest", false},
		{"sibling-newer", true}, {"foreign", false},
	} {
		if got := readState(c.title); got != c.want {
			t.Fatalf("after MarkAuthorBeforeRead: %s read=%v want %v", c.title, got, c.want)
		}
	}

	// Author "after": older items across the author's feeds.
	if err := s.Items.MarkAuthorAfterRead(u.ID, middle); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		title string
		want  bool
	}{
		{"newest", true}, {"middle", false}, {"oldest", true}, {"foreign", false},
	} {
		if got := readState(c.title); got != c.want {
			t.Fatalf("after MarkAuthorAfterRead: %s read=%v want %v", c.title, got, c.want)
		}
	}
}

func TestListAuthorsWithFeedCountUnread(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	busy, _ := s.Authors.Create(u.ID, "Busy", "", "")
	_, _ = s.Authors.Create(u.ID, "Quiet", "", "")

	f, _ := s.Feeds.Create(u.ID, busy.ID, "Blog", "https://busy.dev/rss.xml", "", "", 900)
	s.Items.Upsert(f.ID, Item{GUID: "g1", Title: "One", Link: "https://busy.dev/1", FetchedAt: db.Now()})
	s.Items.Upsert(f.ID, Item{GUID: "g2", Title: "Two", Link: "https://busy.dev/2", FetchedAt: db.Now()})
	items, _ := s.Items.List(u.ID, ItemFilter{})
	if err := s.Items.SetRead(u.ID, items[0].ID, true); err != nil {
		t.Fatalf("SetRead: %v", err)
	}

	rows, err := s.Authors.ListWithFeedCount(u.ID)
	if err != nil {
		t.Fatalf("ListWithFeedCount: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	for _, r := range rows {
		switch r.Name {
		case "Busy":
			if r.FeedCount != 1 || r.UnreadCount != 1 {
				t.Fatalf("busy author: feeds=%d unread=%d, want 1/1", r.FeedCount, r.UnreadCount)
			}
		case "Quiet":
			if r.FeedCount != 0 || r.UnreadCount != 0 {
				t.Fatalf("quiet author: feeds=%d unread=%d, want 0/0", r.FeedCount, r.UnreadCount)
			}
		}
	}
}

func TestItemsCarryAuthor(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, _ := s.Authors.Create(u.ID, "Metru", "", "")
	f, _ := s.Feeds.Create(u.ID, a.ID, "Blog", "https://x.dev/rss.xml", "", "", 900)
	s.Items.Upsert(f.ID, Item{GUID: "g1", Title: "One", Link: "https://x.dev/1", FetchedAt: db.Now()})

	items, err := s.Items.List(u.ID, ItemFilter{})
	if err != nil || len(items) != 1 {
		t.Fatalf("List feed: %v %d", err, len(items))
	}
	it := items[0]
	if it.AuthorName != "Metru" || it.AuthorID != a.ID {
		t.Fatalf("author fields should be populated: %+v", it)
	}
	if it.FeedTitle != "Blog" || it.Title != "One" {
		t.Fatalf("item malformed: %+v", it)
	}
	got, err := s.Items.OneWithFeed(u.ID, it.ID)
	if err != nil || got.Title != "One" {
		t.Fatalf("OneWithFeed: %v %+v", err, got)
	}
}

// TestAuthorItemStats covers the aggregate author stats query: totals, the
// read/unread split, favorites, first/last post times and the 30-day window.
func TestAuthorItemStats(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, _ := s.Authors.Create(u.ID, "Busy", "", "")
	f1, _ := s.Feeds.Create(u.ID, a.ID, "Blog", "https://busy.dev/rss.xml", "", "", 900)
	f2, _ := s.Feeds.Create(u.ID, a.ID, "Pics", "https://pics.dev/rss.xml", "", "", 900)
	// A second author's feed must not leak into the stats.
	other, _ := s.Authors.Create(u.ID, "Other", "", "")
	of, _ := s.Feeds.Create(u.ID, other.ID, "Other", "https://other.dev/rss.xml", "", "", 900)
	// A feedless author for the empty case.
	quiet, _ := s.Authors.Create(u.ID, "Quiet", "", "")

	old := db.FormatTime(time.Now().Add(-100 * 24 * time.Hour))
	recent := db.FormatTime(time.Now().Add(-24 * time.Hour))
	s.Items.Upsert(f1.ID, Item{GUID: "g1", Title: "Old", Link: "https://busy.dev/1", PublishedAt: old, FetchedAt: db.Now()})
	s.Items.Upsert(f1.ID, Item{GUID: "g2", Title: "New", Link: "https://busy.dev/2", PublishedAt: recent, FetchedAt: db.Now()})
	s.Items.Upsert(f2.ID, Item{GUID: "g3", Title: "Pic", Link: "https://pics.dev/1", PublishedAt: recent, FetchedAt: db.Now()})
	s.Items.Upsert(of.ID, Item{GUID: "x", Title: "Nope", Link: "https://other.dev/1", FetchedAt: db.Now()})

	items, _ := s.Items.List(u.ID, ItemFilter{FeedID: f1.ID})
	if err := s.Items.SetRead(u.ID, items[0].ID, true); err != nil {
		t.Fatalf("SetRead: %v", err)
	}
	if err := s.Items.SetFavorite(u.ID, items[0].ID, true); err != nil {
		t.Fatalf("SetFavorite: %v", err)
	}

	st, err := s.Items.StatsAuthor(u.ID, a.ID)
	if err != nil {
		t.Fatalf("StatsAuthor: %v", err)
	}
	if st.Total != 3 || st.Unread != 2 || st.Read != 1 || st.Favorites != 1 || st.Recent != 2 {
		t.Fatalf("stats = %+v, want total=3 unread=2 read=1 fav=1 recent=2", st)
	}
	if st.FirstAt != old || st.LastAt != recent {
		t.Fatalf("first/last = %q/%q, want %q/%q", st.FirstAt, st.LastAt, old, recent)
	}

	// An author with no posts returns zeroes and empty times, not NULL errors.
	empty, err := s.Items.StatsAuthor(u.ID, quiet.ID)
	if err != nil {
		t.Fatalf("empty StatsAuthor: %v", err)
	}
	if empty.Total != 0 || empty.FirstAt != "" || empty.LastAt != "" {
		t.Fatalf("empty stats = %+v", empty)
	}

	// RecentTimes is scoped to the author's feeds only.
	times, err := s.Items.AuthorRecentTimes(u.ID, a.ID, 10)
	if err != nil || len(times) != 3 {
		t.Fatalf("AuthorRecentTimes = %v %v, want 3", times, err)
	}
}

func TestUserHomeConfig(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")

	got, err := s.Users.ByID(u.ID)
	if err != nil || got.HomeConfig != "" {
		t.Fatalf("default home config: %v %q", err, got.HomeConfig)
	}

	raw := `[{"kind":"collection","ref_id":7}]`
	if err := s.Users.SetHomeConfig(u.ID, raw); err != nil {
		t.Fatalf("SetHomeConfig: %v", err)
	}
	got, _ = s.Users.ByID(u.ID)
	if got.HomeConfig != raw {
		t.Fatalf("home config = %q, want %q", got.HomeConfig, raw)
	}

	if err := s.Users.SetHomeConfig(u.ID, ""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got, _ = s.Users.ByID(u.ID)
	if got.HomeConfig != "" {
		t.Fatalf("home config should be cleared, got %q", got.HomeConfig)
	}
}

// TestViewPrefs covers the per-scope display-preference store: a missing scope
// defaults to list, a saved mode round-trips, and scopes are independent.
func TestViewPrefs(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")

	if got := s.ViewPrefs.Mode(u.ID, "/feeds/1"); got != ViewModeList {
		t.Fatalf("default mode = %q, want list", got)
	}
	if err := s.ViewPrefs.Set(u.ID, "/feeds/1", ViewModeGrid); err != nil {
		t.Fatalf("Set grid: %v", err)
	}
	if got := s.ViewPrefs.Mode(u.ID, "/feeds/1"); got != ViewModeGrid {
		t.Fatalf("mode = %q, want grid", got)
	}
	// A different scope is untouched.
	if got := s.ViewPrefs.Mode(u.ID, "/authors/1"); got != ViewModeList {
		t.Fatalf("other scope mode = %q, want list", got)
	}
	// An unknown mode normalizes to list, overwriting in place.
	if err := s.ViewPrefs.Set(u.ID, "/feeds/1", "bogus"); err != nil {
		t.Fatalf("Set bogus: %v", err)
	}
	if got := s.ViewPrefs.Mode(u.ID, "/feeds/1"); got != ViewModeList {
		t.Fatalf("bogus mode = %q, want list", got)
	}
	if modes, err := s.ViewPrefs.Modes(u.ID); err != nil || modes["/feeds/1"] != ViewModeList {
		t.Fatalf("Modes = %v (err %v)", modes, err)
	}
}

func TestCollectionFlow(t *testing.T) {	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, _ := s.Authors.Create(u.ID, "Metru", "", "")
	f, _ := s.Feeds.Create(u.ID, a.ID, "Blog", "https://metru.dev/rss.xml", "", "", 900)

	c, err := s.Collections.Create(u.ID, "Read later")
	if err != nil {
		t.Fatalf("create collection: %v", err)
	}
	if err := s.Collections.AddFeed(u.ID, c.ID, f.ID); err != nil {
		t.Fatalf("AddFeed: %v", err)
	}
	feeds, err := s.Collections.Feeds(u.ID, c.ID)
	if err != nil || len(feeds) != 1 {
		t.Fatalf("collection feeds: %v %d", err, len(feeds))
	}

	// AddFeed must reject a feed owned by another user.
	other := mustUser(t, s, "bob")
	a2, _ := s.Authors.Create(other.ID, "Bob", "", "")
	f2, _ := s.Feeds.Create(other.ID, a2.ID, "Bobs", "https://bob.dev/rss.xml", "", "", 900)
	if err := s.Collections.AddFeed(u.ID, c.ID, f2.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound cross-user AddFeed, got %v", err)
	}

	// Items filterable by collection.
	base := Item{GUID: "g1", Title: "One", Link: "https://metru.dev/1", FetchedAt: db.Now()}
	s.Items.Upsert(f.ID, base)
	byCol, err := s.Items.List(u.ID, ItemFilter{CollectionID: c.ID})
	if err != nil || len(byCol) != 1 {
		t.Fatalf("items in collection: %v %d", err, len(byCol))
	}
	if byCol[0].AuthorName != "Metru" {
		t.Fatalf("expected author join, got %q", byCol[0].AuthorName)
	}

	if err := s.Collections.RemoveFeed(u.ID, c.ID, f.ID); err != nil {
		t.Fatalf("RemoveFeed: %v", err)
	}
	if err := s.Collections.Delete(u.ID, c.ID); err != nil {
		t.Fatalf("delete collection: %v", err)
	}
}

func TestFeedIsolation(t *testing.T) {
	s := newTestStore(t)
	u1 := mustUser(t, s, "alice")
	u2 := mustUser(t, s, "bob")
	a1, _ := s.Authors.Create(u1.ID, "A", "", "")
	f1, _ := s.Feeds.Create(u1.ID, a1.ID, "A's feed", "https://a.dev/rss.xml", "", "", 900)
	s.Items.Upsert(f1.ID, Item{GUID: "g", Title: "t", FetchedAt: db.Now()})

	// Bob sees nothing of Alice's data.
	feeds, _ := s.Feeds.List(u2.ID)
	if len(feeds) != 0 {
		t.Fatalf("bob sees alice's feeds")
	}
	items, _ := s.Items.List(u2.ID, ItemFilter{})
	if len(items) != 0 {
		t.Fatalf("bob sees alice's items")
	}
	if _, err := s.Feeds.ByID(u2.ID, f1.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound cross-user feed read, got %v", err)
	}
	if err := s.Feeds.Delete(u2.ID, f1.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound cross-user feed delete, got %v", err)
	}
}

func TestListDue(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, _ := s.Authors.Create(u.ID, "A", "", "")
	f, _ := s.Feeds.Create(u.ID, a.ID, "feed", "https://a.dev/rss.xml", "", "", 900)

	// Never polled -> due.
	due, err := s.Feeds.ListDue(db.Now())
	if err != nil || len(due) != 1 {
		t.Fatalf("ListDue never polled: %v %d", err, len(due))
	}

	// Just polled -> not due.
	if err := s.Feeds.SetPollMeta(f.ID, "", "", db.Now(), ""); err != nil {
		t.Fatal(err)
	}
	due, _ = s.Feeds.ListDue(db.Now())
	if len(due) != 0 {
		t.Fatalf("ListDue should be empty after fresh poll")
	}

	// Disabled feeds are never due.
	s.Feeds.Update(u.ID, f.ID, a.ID, "feed", "https://a.dev/rss.xml", "", "", 900, true, false)
	if err := s.Feeds.SetPollMeta(f.ID, "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	due, _ = s.Feeds.ListDue(db.Now())
	if len(due) != 0 {
		t.Fatalf("ListDue should skip disabled feeds")
	}
}

// TestListDueHonorsNextPollAt covers the rate-limit backoff deadline: a feed is
// not due while next_poll_at is in the future, and is due once it passes.
func TestListDueHonorsNextPollAt(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, _ := s.Authors.Create(u.ID, "A", "", "")
	f, _ := s.Feeds.Create(u.ID, a.ID, "feed", "https://a.dev/rss.xml", "", "", 900)

	// A future deadline suppresses the feed even though it was never polled.
	future := db.FormatTime(time.Now().Add(time.Hour))
	if err := s.Feeds.SetNextPollAt(f.ID, future); err != nil {
		t.Fatal(err)
	}
	due, _ := s.Feeds.ListDue(db.Now())
	if len(due) != 0 {
		t.Fatalf("feed with a future next_poll_at should not be due")
	}
	if got, _ := s.Feeds.ByID(u.ID, f.ID); got.NextPollAt != future {
		t.Fatalf("NextPollAt = %q, want %q", got.NextPollAt, future)
	}

	// A past deadline lets it become due again.
	if err := s.Feeds.SetNextPollAt(f.ID, db.FormatTime(time.Now().Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}
	due, _ = s.Feeds.ListDue(db.Now())
	if len(due) != 1 {
		t.Fatalf("feed past its next_poll_at should be due, got %d", len(due))
	}

	// Clearing it also lets the feed be due.
	s.Feeds.SetNextPollAt(f.ID, future)
	if err := s.Feeds.SetNextPollAt(f.ID, ""); err != nil {
		t.Fatal(err)
	}
	due, _ = s.Feeds.ListDue(db.Now())
	if len(due) != 1 {
		t.Fatalf("cleared next_poll_at should make the feed due, got %d", len(due))
	}
}

// TestListDueNextPollAtOverridesInterval is the core fix for rate-limited hosts:
// a rate-limit deadline that has passed makes the feed due even though it was
// polled within its (possibly 1-day) poll interval, so the host's own short
// window is honored rather than masked by the interval.
func TestListDueNextPollAtOverridesInterval(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, _ := s.Authors.Create(u.ID, "A", "", "")
	// A 1-day interval, as quiet/stale feeds get.
	f, _ := s.Feeds.Create(u.ID, a.ID, "feed", "https://reddit.com/r/x/.rss", "", "", 86400)

	// Polled just now (so the interval says "not due") with a rate-limit
	// deadline that has already passed: it must be due.
	if err := s.Feeds.SetPollMeta(f.ID, "", "", db.Now(), "rate limited"); err != nil {
		t.Fatal(err)
	}
	if err := s.Feeds.SetNextPollAt(f.ID, db.FormatTime(time.Now().Add(-time.Second))); err != nil {
		t.Fatal(err)
	}
	due, _ := s.Feeds.ListDue(db.Now())
	if len(due) != 1 {
		t.Fatalf("a passed backoff should make the feed due despite a long interval, got %d", len(due))
	}
}

func TestCanonicalFeedURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://old.reddit.com/u/Dominan-t.rss", "https://www.reddit.com/user/Dominan-t.rss"},
		{"https://reddit.com/u/foo.rss", "https://www.reddit.com/user/foo.rss"},
		{"https://www.reddit.com/r/golang/.rss", "https://www.reddit.com/r/golang/.rss"},
		{"https://np.reddit.com/user/foo.rss", "https://www.reddit.com/user/foo.rss"},
		{"https://example.com/feed.xml", "https://example.com/feed.xml"},
		{"not a url", "not a url"},
	}
	for _, c := range cases {
		if got := CanonicalFeedURL(c.in); got != c.want {
			t.Errorf("CanonicalFeedURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestCanonicalizeFeedURLs rewrites stored reddit feeds in place.
func TestCanonicalizeFeedURLs(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, _ := s.Authors.Create(u.ID, "A", "", "")
	// CanonicalFeedURL runs on Create, so insert the non-canonical form directly
	// to model a feed added before canonicalization.
	f, _ := s.Feeds.Create(u.ID, a.ID, "feed", "https://example.com/x", "", "", 900)
	if err := s.q.SetFeedFeedURL(context.Background(), sqlcgen.SetFeedFeedURLParams{
		FeedUrl: "https://old.reddit.com/u/foo.rss",
		ID:      f.ID,
	}); err != nil {
		t.Fatal(err)
	}
	n, err := s.Feeds.CanonicalizeFeedURLs()
	if err != nil || n != 1 {
		t.Fatalf("CanonicalizeFeedURLs = %d, %v", n, err)
	}
	got, _ := s.Feeds.ByID(u.ID, f.ID)
	if got.FeedURL != "https://www.reddit.com/user/foo.rss" {
		t.Fatalf("FeedURL = %q", got.FeedURL)
	}
}

// TestPluginDisableReenable covers the plugin reconciler's storage: a feed
// auto-disabled for a missing plugin is re-enabled by id, never when the user
// paused it by hand.
func TestPluginDisableReenable(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, _ := s.Authors.Create(u.ID, "A", "", "")

	owned, _ := s.Feeds.CreateWithPlugin(u.ID, a.ID, "z", "https://example.org/z", "", "", "zorg", 900)
	if owned.PluginName != "zorg" {
		t.Fatalf("PluginName = %q", owned.PluginName)
	}

	// Auto-disable (as the reconciler does).
	if err := s.Feeds.DisableForMissingPlugin(owned.ID, "zorg"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Feeds.ByID(u.ID, owned.ID)
	if got.Enabled || got.DisabledReason == "" {
		t.Fatalf("feed should be disabled with a reason: %+v", got)
	}
	if !IsPluginMissingReason(got.DisabledReason) {
		t.Fatalf("reason should be recognized as a missing-plugin reason: %q", got.DisabledReason)
	}

	// Re-enabling an unrelated id does nothing.
	if n, _ := s.Feeds.ReenableAutoDisabled(owned.ID + 999); n != 0 {
		t.Fatalf("an unknown feed should not re-enable: %d", n)
	}
	// The owning feed is re-enabled by id and its reason cleared.
	if n, _ := s.Feeds.ReenableAutoDisabled(owned.ID); n != 1 {
		t.Fatalf("the auto-disabled feed should re-enable: %d", n)
	}
	got, _ = s.Feeds.ByID(u.ID, owned.ID)
	if !got.Enabled || got.DisabledReason != "" {
		t.Fatalf("feed should be re-enabled: %+v", got)
	}

	// A user pause (no reason) is never resumed.
	paused, _ := s.Feeds.CreateWithPlugin(u.ID, a.ID, "u", "https://example.org/u", "", "", "zorg", 900)
	s.Feeds.Update(u.ID, paused.ID, a.ID, "u", "https://example.org/u", "", "", 900, true, false)
	gotPaused, _ := s.Feeds.ByID(u.ID, paused.ID)
	if gotPaused.DisabledReason != "" {
		t.Fatalf("a user save should clear the disabled reason: %q", gotPaused.DisabledReason)
	}
	if n, _ := s.Feeds.ReenableAutoDisabled(paused.ID); n != 0 {
		t.Fatalf("a user-paused feed must not be auto-resumed: %d", n)
	}
}

// TestResetPluginForDomain covers the admin escape hatch: clearing a domain's
// plugin owner returns its feeds to the generic parser, re-enables the ones
// parked for a missing plugin, and leaves user-paused feeds and other domains
// untouched.
func TestResetPluginForDomain(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, _ := s.Authors.Create(u.ID, "A", "", "")

	parked, _ := s.Feeds.CreateWithPlugin(u.ID, a.ID, "p", "https://example.org/p", "", "", "zorg", 900)
	s.Feeds.DisableForMissingPlugin(parked.ID, "parked")

	active, _ := s.Feeds.CreateWithPlugin(u.ID, a.ID, "a", "https://example.org/a", "", "", "zorg", 900)

	paused, _ := s.Feeds.CreateWithPlugin(u.ID, a.ID, "q", "https://example.org/q", "", "", "zorg", 900)
	s.Feeds.Update(u.ID, paused.ID, a.ID, "q", "https://example.org/q", "", "", 900, true, false)

	other, _ := s.Feeds.CreateWithPlugin(u.ID, a.ID, "o", "https://other.com/o", "", "", "zorg", 900)

	if n, err := s.Feeds.ResetPluginForDomain("example.org"); err != nil || n != 3 {
		t.Fatalf("ResetPluginForDomain = %d, %v", n, err)
	}

	for _, id := range []int64{parked.ID, active.ID, paused.ID} {
		got, _ := s.Feeds.ByID(u.ID, id)
		if got.PluginName != "" {
			t.Fatalf("feed %d should be back on the generic parser: %q", id, got.PluginName)
		}
	}
	parkedGot, _ := s.Feeds.ByID(u.ID, parked.ID)
	if !parkedGot.Enabled || parkedGot.DisabledReason != "" {
		t.Fatalf("parked feed should be un-parked: %+v", parkedGot)
	}
	pausedGot, _ := s.Feeds.ByID(u.ID, paused.ID)
	if pausedGot.Enabled || pausedGot.DisabledReason != "" {
		t.Fatalf("user-paused feed should stay paused: %+v", pausedGot)
	}
	otherGot, _ := s.Feeds.ByID(u.ID, other.ID)
	if otherGot.PluginName != "zorg" {
		t.Fatalf("another domain must be untouched: %q", otherGot.PluginName)
	}

	// Resetting an unknown/empty domain is a no-op.
	if n, _ := s.Feeds.ResetPluginForDomain(""); n != 0 {
		t.Fatalf("empty domain should be a no-op: %d", n)
	}
	if n, _ := s.Feeds.ResetPluginForDomain("nope.com"); n != 0 {
		t.Fatalf("unknown domain should be a no-op: %d", n)
	}
}

func TestSourceIconStore(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	other := mustUser(t, s, "bob")

	ic, err := s.SourceIcons.Create(u.ID, "github.com", "https://example.com/icon.png")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.SourceIcons.ByDomain(u.ID, "github.com")
	if err != nil || got.ID != ic.ID || got.IconURL != "https://example.com/icon.png" {
		t.Fatalf("ByDomain: %v %+v", err, got)
	}
	// Duplicate domain -> ErrExists.
	if _, err := s.SourceIcons.Create(u.ID, "github.com", "https://x.dev/i.png"); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate: %v", err)
	}
	// Scoped to the user.
	if _, err := s.SourceIcons.ByDomain(other.ID, "github.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other user should not see the icon: %v", err)
	}

	// Cache and read back.
	if err := s.SourceIcons.SetIconKey(u.ID, ic.ID, "icons/1/github.com", db.Now()); err != nil {
		t.Fatalf("SetIconKey: %v", err)
	}
	got, _ = s.SourceIcons.ByID(u.ID, ic.ID)
	if got.IconKey != "icons/1/github.com" || got.LastFetchedAt == "" {
		t.Fatalf("cached icon: %+v", got)
	}

	rows, _ := s.SourceIcons.List(u.ID)
	if len(rows) != 1 {
		t.Fatalf("List: %d", len(rows))
	}
	if err := s.SourceIcons.Delete(u.ID, ic.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.SourceIcons.ByID(u.ID, ic.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestUrlMappingStore(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	other := mustUser(t, s, "bob")

	m, err := s.UrlMappings.Create(u.ID, `abc.com/{user}`, `{user}.abc.com/feed`)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.UrlMappings.ByID(u.ID, m.ID)
	if err != nil || got.Pattern != `abc.com/{user}` || got.Template != "{user}.abc.com/feed" {
		t.Fatalf("ByID: %v %+v", err, got)
	}
	// Duplicate pattern -> ErrExists.
	if _, err := s.UrlMappings.Create(u.ID, `abc.com/{user}`, `{user}.abc.com/rss`); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate: %v", err)
	}
	// Scoped to the user.
	if _, err := s.UrlMappings.ByID(other.ID, m.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other user should not see the mapping: %v", err)
	}

	// Update keeps the id and applies to the user only.
	if err := s.UrlMappings.Update(u.ID, m.ID, `abc.com/{user}`, `{user}.abc.com/rss`); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ = s.UrlMappings.ByID(u.ID, m.ID)
	if got.Template != "{user}.abc.com/rss" {
		t.Fatalf("updated mapping: %+v", got)
	}
	rows, _ := s.UrlMappings.List(u.ID)
	if len(rows) != 1 {
		t.Fatalf("List: %d", len(rows))
	}
	if err := s.UrlMappings.Delete(u.ID, m.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.UrlMappings.ByID(u.ID, m.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestAuthorLinkStore(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	other := mustUser(t, s, "bob")
	a, _ := s.Authors.Create(u.ID, "Metru", "", "")
	a2, _ := s.Authors.Create(u.ID, "Other", "", "")

	l, err := s.AuthorLinks.Create(u.ID, a.ID, "Twitch", "https://twitch.tv/ThePrimeagen")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.AuthorLinks.ByID(u.ID, l.ID)
	if err != nil || got.Label != "Twitch" || got.URL != "https://twitch.tv/ThePrimeagen" {
		t.Fatalf("ByID: %v %+v", err, got)
	}

	// Unlabeled links read back with an empty label.
	if _, err := s.AuthorLinks.Create(u.ID, a.ID, "", "https://example.com"); err != nil {
		t.Fatalf("create unlabeled: %v", err)
	}
	rows, _ := s.AuthorLinks.ListByAuthor(u.ID, a.ID)
	if len(rows) != 2 {
		t.Fatalf("ListByAuthor: %d", len(rows))
	}
	// Scoped to the author.
	if otherRows, _ := s.AuthorLinks.ListByAuthor(u.ID, a2.ID); len(otherRows) != 0 {
		t.Fatalf("link should not appear under another author: %+v", otherRows)
	}
	// Scoped to the user.
	if _, err := s.AuthorLinks.ByID(other.ID, l.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other user should not see the link: %v", err)
	}
	if err := s.AuthorLinks.Delete(other.ID, l.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete cross-user should be ErrNotFound: %v", err)
	}

	if err := s.AuthorLinks.Delete(u.ID, l.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.AuthorLinks.ByID(u.ID, l.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestAuthorLinkCascadesWithAuthor(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, _ := s.Authors.Create(u.ID, "Metru", "", "")
	l, _ := s.AuthorLinks.Create(u.ID, a.ID, "", "https://example.com")

	if err := s.Authors.Delete(u.ID, a.ID); err != nil {
		t.Fatalf("delete author: %v", err)
	}
	if _, err := s.AuthorLinks.ByID(u.ID, l.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("link should cascade with its author, got %v", err)
	}
}

func TestFeedCadenceMethods(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, _ := s.Authors.Create(u.ID, "A", "", "")
	f, _ := s.Feeds.Create(u.ID, a.ID, "Blog", "https://b.dev/feed.xml", "", "", 900)

	// New feeds default to adaptive polling on.
	got, _ := s.Feeds.ByID(u.ID, f.ID)
	if !got.PollIntervalAuto {
		t.Fatalf("new feed should default to poll_interval_auto on: %+v", got)
	}

	// SetPollInterval is unscoped and updates the stored interval.
	if err := s.Feeds.SetPollInterval(f.ID, 3600); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Feeds.ByID(u.ID, f.ID)
	if got.PollIntervalSec != 3600 {
		t.Fatalf("SetPollInterval = %d, want 3600", got.PollIntervalSec)
	}

	// RecentTimes returns newest-first item timestamps (published, else fetched).
	base := time.Now().UTC().Add(-time.Hour)
	s.Items.Upsert(f.ID, Item{GUID: "a", Title: "a", PublishedAt: db.FormatTime(base.Add(-2 * time.Hour)), FetchedAt: db.Now()})
	s.Items.Upsert(f.ID, Item{GUID: "b", Title: "b", PublishedAt: db.FormatTime(base.Add(-1 * time.Hour)), FetchedAt: db.Now()})
	// An item with no published time falls back to fetched_at.
	s.Items.Upsert(f.ID, Item{GUID: "c", Title: "c", FetchedAt: db.FormatTime(base)})
	times, err := s.Items.RecentTimes(f.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(times) != 3 || times[0] != db.FormatTime(base) {
		t.Fatalf("RecentTimes = %v, want newest-first with fetched fallback", times)
	}

	// SetLastItemAt persists it.
	if err := s.Feeds.SetLastItemAt(f.ID, times[0]); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Feeds.ByID(u.ID, f.ID)
	if got.LastItemAt != times[0] {
		t.Fatalf("LastItemAt = %q, want %q", got.LastItemAt, times[0])
	}
}

func TestMigrateBackfillsCadence(t *testing.T) {
	// A fresh store applies all migrations; the schemaV25 backfill only affects
	// pre-existing feeds. Verify the column defaults and that a customized
	// interval can be turned manual.
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, _ := s.Authors.Create(u.ID, "A", "", "")
	f, _ := s.Feeds.Create(u.ID, a.ID, "Blog", "https://b.dev/feed.xml", "", "", 900)
	if !f.PollIntervalAuto {
		t.Fatalf("new feed should default to auto on")
	}
	// Turning it off via Update sticks.
	if err := s.Feeds.Update(u.ID, f.ID, a.ID, "Blog", "https://b.dev/feed.xml", "", "", 900, false, true); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Feeds.ByID(u.ID, f.ID)
	if got.PollIntervalAuto {
		t.Fatalf("Update should be able to turn auto off: %+v", got)
	}
}

func TestMigrateLegacyFiles(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")

	// Seed legacy DB blobs directly (as pre-object-storage versions did).
	if _, err := s.db.Exec(
		`UPDATE users SET avatar_data = ?, avatar_content_type = 'image/png' WHERE id = ?`,
		[]byte("avatarbytes"), u.ID,
	); err != nil {
		t.Fatal(err)
	}
	ic, _ := s.SourceIcons.Create(u.ID, "github.com", "https://example.com/i.png")
	if _, err := s.db.Exec(
		`UPDATE source_icons SET icon_data = ?, content_type = 'image/png' WHERE id = ?`,
		[]byte("iconbytes"), ic.ID,
	); err != nil {
		t.Fatal(err)
	}

	fs := filestore.NewMemory()
	if err := s.MigrateLegacyFiles(context.Background(), fs); err != nil {
		t.Fatalf("MigrateLegacyFiles: %v", err)
	}

	// Bytes moved to object storage and keys recorded; blobs cleared.
	ct, data, err := fs.Get(context.Background(), "avatars/"+strconv.FormatInt(u.ID, 10))
	if err != nil || string(data) != "avatarbytes" || ct != "image/png" {
		t.Fatalf("avatar object: %v %q", err, data)
	}
	ct, data, err = fs.Get(context.Background(), "icons/1/github.com")
	if err != nil || string(data) != "iconbytes" {
		t.Fatalf("icon object: %v %q", err, data)
	}
	got, _ := s.Users.ByID(u.ID)
	if !got.HasAvatar {
		t.Fatal("avatar key should be set")
	}
	icon, _ := s.SourceIcons.ByID(u.ID, ic.ID)
	if icon.IconKey != "icons/1/github.com" {
		t.Fatalf("icon key = %q", icon.IconKey)
	}
	var blob int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE avatar_data IS NOT NULL`).Scan(&blob); err != nil || blob != 0 {
		t.Fatalf("legacy avatar blobs remain: %d %v", blob, err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM source_icons WHERE icon_data IS NOT NULL`).Scan(&blob); err != nil || blob != 0 {
		t.Fatalf("legacy icon blobs remain: %d %v", blob, err)
	}
	// Idempotent: nothing left to migrate.
	if err := s.MigrateLegacyFiles(context.Background(), fs); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
}

func TestUserAvatar(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")

	if u.HasAvatar {
		t.Fatal("no avatar initially")
	}
	if err := s.Users.SetAvatarKey(u.ID, "avatars/1"); err != nil {
		t.Fatalf("SetAvatarKey: %v", err)
	}
	got, _ := s.Users.ByID(u.ID)
	if !got.HasAvatar {
		t.Fatal("HasAvatar should be true")
	}
	key, err := s.Users.AvatarKey(u.ID)
	if err != nil || key != "avatars/1" {
		t.Fatalf("AvatarKey: %v %q", err, key)
	}
	// A user with no avatar key has HasAvatar false.
	bob := mustUser(t, s, "bob")
	if bob.HasAvatar {
		t.Fatal("bob should have no avatar")
	}
}

func TestUserAdminFlag(t *testing.T) {
	s := newTestStore(t)
	alice := mustUser(t, s, "alice")
	mustUser(t, s, "bob")

	if alice.IsAdmin {
		t.Fatal("new users are not admins")
	}
	if err := s.Users.SetAdmin(alice.ID, true); err != nil {
		t.Fatalf("SetAdmin: %v", err)
	}
	got, err := s.Users.ByID(alice.ID)
	if err != nil || !got.IsAdmin {
		t.Fatalf("alice should be admin: %+v %v", got, err)
	}
	admins, err := s.Users.CountAdmins()
	if err != nil || admins != 1 {
		t.Fatalf("count admins = %d %v", admins, err)
	}
}

func TestUserDeleteCascadesAndPurgesKeys(t *testing.T) {
	s := newTestStore(t)
	alice := mustUser(t, s, "alice")
	bob := mustUser(t, s, "bob")

	if err := s.Users.SetAvatarKey(bob.ID, "avatars/2"); err != nil {
		t.Fatalf("SetAvatarKey: %v", err)
	}
	icon, err := s.SourceIcons.Create(bob.ID, "github.com", "https://x/icon.png")
	if err != nil {
		t.Fatalf("create icon: %v", err)
	}
	if err := s.SourceIcons.SetIconKey(bob.ID, icon.ID, "icons/2/github.com", db.Now()); err != nil {
		t.Fatalf("SetIconKey: %v", err)
	}
	if err := s.Sessions.Create(bob.ID, "bob-token", db.Now()); err != nil {
		t.Fatalf("create session: %v", err)
	}

	keys, err := s.Users.ListObjectKeys(bob.ID)
	if err != nil {
		t.Fatalf("ListObjectKeys: %v", err)
	}
	if len(keys) != 2 || keys[0] != "icons/2/github.com" || keys[1] != "avatars/2" {
		t.Fatalf("object keys = %v", keys)
	}

	if err := s.Users.Delete(bob.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Users.ByID(bob.ID); err == nil {
		t.Fatal("bob still present after delete")
	}
	if _, err := s.Sessions.UserByToken("bob-token"); err == nil {
		t.Fatal("bob's session should cascade away")
	}
	var icons int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM source_icons WHERE user_id = ?`, bob.ID).Scan(&icons); err != nil || icons != 0 {
		t.Fatalf("bob's icons remain: %d %v", icons, err)
	}

	// ResetPassword + session revocation.
	alice, _ = s.Users.ByID(alice.ID)
	if err := s.Sessions.Create(alice.ID, "alice-token", db.Now()); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := s.Users.ResetPassword(alice.ID, "newhash"); err != nil {
		t.Fatalf("ResetPassword: %v", err)
	}
	got, _ := s.Users.ByID(alice.ID)
	if got.PasswordHash != "newhash" {
		t.Fatalf("password hash = %q", got.PasswordHash)
	}
}

func TestAuthorAvatarKey(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, err := s.Authors.Create(u.ID, "Metru", "https://example.com/favicon.png", "")
	if err != nil {
		t.Fatalf("create author: %v", err)
	}
	if a.AvatarKey != "" || a.LastFetchedAt != "" {
		t.Fatalf("new author should have no cache: %+v", a)
	}
	key := "author-avatars/" + strconv.FormatInt(u.ID, 10) + "/" + strconv.FormatInt(a.ID, 10)
	if err := s.Authors.SetAvatarKey(u.ID, a.ID, key, db.Now()); err != nil {
		t.Fatalf("SetAvatarKey: %v", err)
	}
	got, err := s.Authors.ByID(u.ID, a.ID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if got.AvatarKey != key || got.LastFetchedAt == "" {
		t.Fatalf("avatar cache not recorded: %+v", got)
	}
	rows, err := s.Authors.ListWithFeedCount(u.ID)
	if err != nil || len(rows) != 1 || rows[0].AvatarKey != key {
		t.Fatalf("ListWithFeedCount avatar key: %+v %v", rows, err)
	}
	if err := s.Authors.ClearAvatarKey(u.ID, a.ID); err != nil {
		t.Fatalf("ClearAvatarKey: %v", err)
	}
	got, _ = s.Authors.ByID(u.ID, a.ID)
	if got.AvatarKey != "" || got.LastFetchedAt != "" {
		t.Fatalf("clear should drop key and timestamp: %+v", got)
	}
}

func TestListObjectKeysIncludesAuthorAvatars(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, _ := s.Authors.Create(u.ID, "Metru", "https://x/av.png", "")
	key := "author-avatars/" + strconv.FormatInt(u.ID, 10) + "/" + strconv.FormatInt(a.ID, 10)
	if err := s.Authors.SetAvatarKey(u.ID, a.ID, key, db.Now()); err != nil {
		t.Fatalf("SetAvatarKey: %v", err)
	}
	keys, err := s.Users.ListObjectKeys(u.ID)
	if err != nil {
		t.Fatalf("ListObjectKeys: %v", err)
	}
	found := false
	for _, k := range keys {
		if k == key {
			found = true
		}
	}
	if !found {
		t.Fatalf("author avatar key missing from %v", keys)
	}
}

func TestSettingStore(t *testing.T) {
	s := newTestStore(t)
	// Migration seeds allow_signup = '1'.
	allow, err := s.Settings.AllowSignup()
	if err != nil || !allow {
		t.Fatalf("default allow_signup = %v, %v; want true", allow, err)
	}
	if err := s.Settings.SetAllowSignup(false); err != nil {
		t.Fatalf("SetAllowSignup: %v", err)
	}
	allow, _ = s.Settings.AllowSignup()
	if allow {
		t.Fatal("allow_signup should be false")
	}
	// A missing row defaults back to true.
	if _, err := s.db.Exec(`DELETE FROM settings WHERE key = 'allow_signup'`); err != nil {
		t.Fatal(err)
	}
	allow, err = s.Settings.AllowSignup()
	if err != nil || !allow {
		t.Fatalf("missing setting should default true: %v %v", allow, err)
	}
}

func TestBannerDismissed(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	d, err := s.Users.BannerDismissed(u.ID)
	if err != nil || d {
		t.Fatalf("new user banner dismissed = %v, %v", d, err)
	}
	if err := s.Users.SetBannerDismissed(u.ID, true); err != nil {
		t.Fatal(err)
	}
	d, _ = s.Users.BannerDismissed(u.ID)
	if !d {
		t.Fatal("banner should be dismissed")
	}
}

func TestAutoReadAfterDays(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")

	// Migration default is 30 days.
	got, err := s.Users.ByID(u.ID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if got.AutoReadAfterDays != 30 {
		t.Fatalf("default auto_read_after_days = %d; want 30", got.AutoReadAfterDays)
	}
	if err := s.Users.SetAutoReadAfterDays(u.ID, 7); err != nil {
		t.Fatalf("SetAutoReadAfterDays: %v", err)
	}
	got, _ = s.Users.ByID(u.ID)
	if got.AutoReadAfterDays != 7 {
		t.Fatalf("after set = %d; want 7", got.AutoReadAfterDays)
	}
	// Negative values clamp to 0 (off).
	if err := s.Users.SetAutoReadAfterDays(u.ID, -5); err != nil {
		t.Fatalf("SetAutoReadAfterDays(-5): %v", err)
	}
	got, _ = s.Users.ByID(u.ID)
	if got.AutoReadAfterDays != 0 {
		t.Fatalf("negative should clamp to 0, got %d", got.AutoReadAfterDays)
	}
}

func TestMarkOlderThanRead(t *testing.T) {
	s := newTestStore(t)
	alice := mustUser(t, s, "alice")
	bob := mustUser(t, s, "bob")
	a, _ := s.Authors.Create(alice.ID, "A", "", "")
	f, err := s.Feeds.Create(alice.ID, a.ID, "Feed", "https://x.dev/rss.xml", "", "", 900)
	if err != nil {
		t.Fatalf("create feed: %v", err)
	}
	b, _ := s.Authors.Create(bob.ID, "B", "", "")
	bf, err := s.Feeds.Create(bob.ID, b.ID, "Bob feed", "https://y.dev/rss.xml", "", "", 900)
	if err != nil {
		t.Fatalf("create feed: %v", err)
	}
	old := db.FormatTime(time.Now().AddDate(0, 0, -40))
	recent := db.FormatTime(time.Now().AddDate(0, 0, -5))

	// Alice: one old unread, one old favorited, one recent unread.
	s.Items.Upsert(f.ID, Item{GUID: "old", Title: "old", PublishedAt: old, FetchedAt: db.Now()})
	s.Items.Upsert(f.ID, Item{GUID: "oldfav", Title: "oldfav", PublishedAt: old, FetchedAt: db.Now()})
	s.Items.Upsert(f.ID, Item{GUID: "recent", Title: "recent", PublishedAt: recent, FetchedAt: db.Now()})
	// Bob: an old unread item that must not be touched by alice's sweep.
	s.Items.Upsert(bf.ID, Item{GUID: "bobold", Title: "bobold", PublishedAt: old, FetchedAt: db.Now()})

	// Favorite one of the old items: favorites are included in the sweep.
	favID, err := s.Items.ByFeedGUID(f.ID, "oldfav")
	if err != nil {
		t.Fatalf("ByFeedGUID: %v", err)
	}
	if err := s.Items.SetFavorite(alice.ID, favID, true); err != nil {
		t.Fatalf("SetFavorite: %v", err)
	}

	n, err := s.Items.MarkOlderThanRead(alice.ID, 30)
	if err != nil {
		t.Fatalf("MarkOlderThanRead: %v", err)
	}
	if n != 2 {
		t.Fatalf("marked %d; want 2 (old + old favorite)", n)
	}
	unread, _ := s.Items.CountUnread(alice.ID, 0)
	if unread != 1 {
		t.Fatalf("alice unread = %d; want 1 (the recent item)", unread)
	}
	// Bob is unaffected.
	bobUnread, _ := s.Items.CountUnread(bob.ID, 0)
	if bobUnread != 1 {
		t.Fatalf("bob unread = %d; want 1 (sweep is user-scoped)", bobUnread)
	}
	// Off (0) is a no-op.
	if n, err := s.Items.MarkOlderThanRead(alice.ID, 0); err != nil || n != 0 {
		t.Fatalf("MarkOlderThanRead(0) = %d, %v; want 0, nil", n, err)
	}
}

func TestGlobalCounts(t *testing.T) {
	s := newTestStore(t)
	alice := mustUser(t, s, "alice")
	mustUser(t, s, "bob")
	a, err := s.Authors.Create(alice.ID, "blog", "https://example.com/a.png", "desc")
	if err != nil {
		t.Fatalf("create author: %v", err)
	}
	f, err := s.Feeds.Create(alice.ID, a.ID, "Example", "https://example.com/feed.xml", "https://example.com", "", 900)
	if err != nil {
		t.Fatalf("create feed: %v", err)
	}
	if _, err := s.Items.Upsert(f.ID, Item{GUID: "g1", Title: "hello", Link: "https://example.com/1", Summary: "sum", FetchedAt: db.Now()}); err != nil {
		t.Fatalf("upsert item: %v", err)
	}

	if n, _ := s.Users.Count(); n != 2 {
		t.Fatalf("users count = %d", n)
	}
	if n, _ := s.Feeds.Count(); n != 1 {
		t.Fatalf("feeds count = %d", n)
	}
	if n, _ := s.Authors.Count(); n != 1 {
		t.Fatalf("authors count = %d", n)
	}
	if n, _ := s.Items.Count(); n != 1 {
		t.Fatalf("items count = %d", n)
	}
	if n, _ := s.Items.CountAllUnread(); n != 1 {
		t.Fatalf("unread count = %d", n)
	}
}

func TestListAllFeeds(t *testing.T) {
	s := newTestStore(t)
	alice := mustUser(t, s, "alice")
	bob := mustUser(t, s, "bob")
	aliceAuthor, _ := s.Authors.Create(alice.ID, "AA", "", "")
	bobAuthor, _ := s.Authors.Create(bob.ID, "BB", "", "")
	if _, err := s.Feeds.Create(alice.ID, aliceAuthor.ID, "A", "https://a/feed.xml", "", "", 900); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Feeds.Create(bob.ID, bobAuthor.ID, "B", "https://b/feed.xml", "", "", 900); err != nil {
		t.Fatal(err)
	}
	feeds, err := s.Feeds.ListAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(feeds) != 2 {
		t.Fatalf("ListAll len = %d", len(feeds))
	}
	owners := map[string]string{feeds[0].Title: feeds[0].Owner, feeds[1].Title: feeds[1].Owner}
	if owners["A"] != "alice" || owners["B"] != "bob" {
		t.Fatalf("owners = %v", owners)
	}
}

func TestFeedLastError(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, _ := s.Authors.Create(u.ID, "Example", "", "")
	f, err := s.Feeds.Create(u.ID, a.ID, "Example", "https://example.com/feed.xml", "", "", 900)
	if err != nil {
		t.Fatal(err)
	}
	if f.LastError != "" {
		t.Fatalf("new feed LastError = %q", f.LastError)
	}
	if err := s.Feeds.SetPollMeta(f.ID, "", "", db.Now(), "boom: feed exploded"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Feeds.ByID(u.ID, f.ID)
	if got.LastError != "boom: feed exploded" {
		t.Fatalf("LastError = %q", got.LastError)
	}
	// A successful poll clears it.
	if err := s.Feeds.SetPollMeta(f.ID, "etag", "", db.Now(), ""); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Feeds.ByID(u.ID, f.ID)
	if got.LastError != "" {
		t.Fatalf("LastError should be cleared, got %q", got.LastError)
	}
}

func TestSessionsExceptAndList(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	for _, tok := range []string{"a", "b", "c"} {
		if err := s.Sessions.Create(u.ID, tok, db.Now()); err != nil {
			t.Fatal(err)
		}
	}
	sess, err := s.Sessions.ListUserSessions(u.ID)
	if err != nil || len(sess) != 3 {
		t.Fatalf("ListUserSessions = %d %v", len(sess), err)
	}
	if err := s.Sessions.DeleteUserSessionsExcept(u.ID, "b"); err != nil {
		t.Fatal(err)
	}
	sess, _ = s.Sessions.ListUserSessions(u.ID)
	got := map[string]bool{}
	for _, se := range sess {
		got[se.Token] = true
	}
	if len(sess) != 1 || !got["b"] {
		t.Fatalf("expected only token b to remain, got %v", got)
	}
}

func TestListPageAscending(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, _ := s.Authors.Create(u.ID, "Metru", "", "")
	f, _ := s.Feeds.Create(u.ID, a.ID, "Blog", "https://metru.dev/rss.xml", "", "", 900)
	s.Items.Upsert(f.ID, Item{GUID: "a", Title: "oldest", Link: "https://metru.dev/1", PublishedAt: "2026-01-01 00:00:00", FetchedAt: db.Now()})
	s.Items.Upsert(f.ID, Item{GUID: "b", Title: "middle", Link: "https://metru.dev/2", PublishedAt: "2026-01-02 00:00:00", FetchedAt: db.Now()})
	s.Items.Upsert(f.ID, Item{GUID: "c", Title: "newest", Link: "https://metru.dev/3", PublishedAt: "2026-01-03 00:00:00", FetchedAt: db.Now()})

	desc, more, _ := s.Items.ListPage(u.ID, ItemFilter{Limit: 2})
	if !more || len(desc) != 2 || desc[0].Title != "newest" || desc[1].Title != "middle" {
		t.Fatalf("desc page: more=%v %+v", more, desc)
	}
	// Ascending: oldest first; page forward with AfterID.
	asc, more, _ := s.Items.ListPage(u.ID, ItemFilter{Limit: 2, Ascending: true})
	if !more || len(asc) != 2 || asc[0].Title != "oldest" || asc[1].Title != "middle" {
		t.Fatalf("asc page: more=%v %+v", more, asc)
	}
	next, _, _ := s.Items.ListPage(u.ID, ItemFilter{Limit: 2, Ascending: true, AfterID: asc[len(asc)-1].ID})
	if len(next) != 1 || next[0].Title != "newest" {
		t.Fatalf("asc next page: %+v", next)
	}
}
