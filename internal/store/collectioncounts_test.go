package store

import (
	"testing"

	"github.com/metruzanca/nanoflux/internal/db"
)

// collectionCountsFixture builds a collection whose member author owns one
// feed with two unread and one read item, plus a second author with a feed
// outside the collection whose item a view can pull in.
func collectionCountsFixture(t *testing.T, s *Store) (User, Collection, Author, Author) {
	t.Helper()
	u := mustUser(t, s, "alice")
	memberAuthor, err := s.Authors.Create(u.ID, "Metru", "", "")
	if err != nil {
		t.Fatal(err)
	}
	otherAuthor, err := s.Authors.Create(u.ID, "Other", "", "")
	if err != nil {
		t.Fatal(err)
	}
	member, err := s.Feeds.Create(u.ID, memberAuthor.ID, "Blog", "https://metru.dev/rss.xml", "", "", 900)
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.Feeds.Create(u.ID, otherAuthor.ID, "Other", "https://other.dev/rss.xml", "", "", 900)
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.Collections.Create(u.ID, "Dev")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Collections.AddAuthor(u.ID, c.ID, memberAuthor.ID); err != nil {
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
	return u, c, memberAuthor, otherAuthor
}

func TestCollectionCountsByAuthor(t *testing.T) {
	s := newTestStore(t)
	u, c, _, _ := collectionCountsFixture(t, s)

	counts, err := s.Collections.CountsByAuthor(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := counts[c.ID]; got.Unread != 2 || got.Read != 1 {
		t.Fatalf("collection counts = %+v, want unread 2 read 1", got)
	}
}

func TestCollectionCountsByScope(t *testing.T) {
	s := newTestStore(t)
	u, c, _, otherAuthor := collectionCountsFixture(t, s)

	pred := ViewFilter{
		AuthorID:  otherAuthor.ID,
		TagGroups: []TagGroup{{Tags: []string{"u/example-poster"}, All: true}},
	}
	scopes := []CollectionScope{{CollectionID: c.ID, Views: []ViewFilter{pred}}}
	counts, err := s.Collections.CountsByScope(u.ID, scopes)
	if err != nil {
		t.Fatal(err)
	}
	// The member author contributes 2 unread and 1 read; the view adds one
	// unread item from the other author's feed.
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
	u, c, memberAuthor, _ := collectionCountsFixture(t, s)
	if _, err := s.SavedViews.Create(u.ID, SavedView{AuthorID: memberAuthor.ID, Title: "Example", Filters: []string{"tag:show:u/example-poster"}}); err != nil {
		t.Fatal(err)
	}

	rows, err := s.Collections.ListForIndex(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("collections = %+v, want 1", rows)
	}
	if got := rows[0]; got.ID != c.ID || got.AuthorCount != 1 || got.Unread != 0 || got.Read != 0 {
		t.Fatalf("collection = %+v, want one author and no item counts", got)
	}
}
