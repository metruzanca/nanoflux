package store

import (
	"strconv"
	"testing"

	"github.com/metruzanca/nanoflux/internal/db"
)

func TestAuthorFilterRules(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, _ := s.Authors.Create(u.ID, "Author", "", "")
	f1, _ := s.Feeds.Create(u.ID, a.ID, "One", "https://one.dev/feed", "", "", 900)
	f2, _ := s.Feeds.Create(u.ID, a.ID, "Two", "https://two.dev/feed", "", "", 900)
	other, _ := s.Authors.Create(u.ID, "Other", "", "")
	fo, _ := s.Feeds.Create(u.ID, other.ID, "Other", "https://other.dev/feed", "", "", 900)

	upsert := func(feedID int64, guid, title string, cats ...string) int64 {
		t.Helper()
		if _, err := s.Items.Upsert(feedID, Item{
			GUID: guid, Title: title, Link: "https://x/" + guid,
			Categories: cats, FetchedAt: db.Now(),
		}); err != nil {
			t.Fatal(err)
		}
		id, err := s.Items.ByFeedIdentity(feedID, guid)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	upsert(f1.ID, "go", "Go post", "r/golang", "tutorial")
	rustID := upsert(f1.ID, "rust", "Rust post", "r/rust")
	upsert(f2.ID, "note", "Note post", "r/rust")
	upsert(fo.ID, "other", "Other post", "r/rust")

	titles := func(items []ItemWithFeed) []string {
		out := make([]string, 0, len(items))
		for _, it := range items {
			out = append(out, it.Title)
		}
		return out
	}
	list := func() []string {
		items, err := s.Items.List(u.ID, ItemFilter{AuthorID: a.ID, Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		return titles(items)
	}
	contains := func(list []string, want string) bool {
		for _, v := range list {
			if v == want {
				return true
			}
		}
		return false
	}

	if got := list(); len(got) != 3 {
		t.Fatalf("unfiltered author items = %v, want 3", got)
	}

	apply := func(rules ...FilterRule) {
		t.Helper()
		if err := s.AuthorFilters.Replace(u.ID, a.ID, rules); err != nil {
			t.Fatal(err)
		}
	}

	// Block a tag: matching items vanish everywhere, including global counts
	// and lists; another author's item with the same tag is untouched.
	apply(FilterRule{Kind: FilterKindTag, Action: FilterActionHide, Combine: FilterCombineAny, Values: []string{"r/rust"}})
	if got := list(); len(got) != 1 || got[0] != "Go post" {
		t.Fatalf("blocked tag list = %v, want [Go post]", got)
	}
	if n, _ := s.Items.CountUnreadAuthor(u.ID, a.ID); n != 1 {
		t.Fatalf("author unread = %d, want 1", n)
	}
	if n, _ := s.Items.CountUnread(u.ID, 0); n != 2 {
		t.Fatalf("global unread = %d, want 2 (Go + Other)", n)
	}
	all, err := s.Items.List(u.ID, ItemFilter{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if got := titles(all); !contains(got, "Go post") || contains(got, "Rust post") {
		t.Fatalf("global list = %v, want Go post but not Rust post", got)
	}
	if err := s.Items.SetFavorite(u.ID, rustID, true); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Items.CountFavoritesAuthor(u.ID, a.ID); n != 0 {
		t.Fatalf("hidden favorite count = %d, want 0", n)
	}

	// Allow rules are ANDed: both tags must be present.
	apply(FilterRule{Kind: FilterKindTag, Action: FilterActionShow, Combine: FilterCombineAll, Values: []string{"r/golang", "tutorial"}})
	if got := list(); len(got) != 1 || got[0] != "Go post" {
		t.Fatalf("allow all-tags list = %v, want [Go post]", got)
	}

	// A feed rule uses membership, so it hides both items of that feed.
	apply(FilterRule{Kind: FilterKindFeed, Action: FilterActionHide, Combine: FilterCombineAny, Values: []string{itoa(f1.ID)}})
	if got := list(); len(got) != 1 || got[0] != "Note post" {
		t.Fatalf("blocked feed list = %v, want [Note post]", got)
	}

	// Clearing the rules reveals everything again.
	apply()
	if got := list(); len(got) != 3 {
		t.Fatalf("cleared rules list = %v, want 3", got)
	}

	// Preview evaluates draft groups without persisting them.
	allow := []TagGroup{{Tags: []string{"r/rust"}, All: true}}
	preview, err := s.Items.PreviewAuthorFilters(u.ID, a.ID, allow, nil, 5)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Total != 3 || preview.AllowedCount != 2 || preview.BlockedCount != 1 {
		t.Fatalf("preview counts = %+v, want total 3 allowed 2 blocked 1", preview)
	}
	if len(preview.Allowed) != 2 || len(preview.Blocked) != 1 || preview.Blocked[0].Title != "Go post" {
		t.Fatalf("preview samples = allowed %v blocked %v", titles(preview.Allowed), titles(preview.Blocked))
	}
}

func TestAuthorFilterFeedDeleteCleanup(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, _ := s.Authors.Create(u.ID, "Author", "", "")
	f, _ := s.Feeds.Create(u.ID, a.ID, "One", "https://one.dev/feed", "", "", 900)

	if err := s.AuthorFilters.Replace(u.ID, a.ID, []FilterRule{{
		Kind: FilterKindFeed, Action: FilterActionHide, Combine: FilterCombineAny,
		Values: []string{itoa(f.ID)},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Feeds.Delete(u.ID, f.ID); err != nil {
		t.Fatal(err)
	}
	rules, err := s.AuthorFilters.List(u.ID, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 0 {
		t.Fatalf("feed delete should remove the emptied rule, got %+v", rules)
	}
}

func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}

// TestAuthorFilterViewMatchesGroups guards the one deliberate duplication: the
// visible_items view and the dynamic group builder must agree for every rule
// shape. Both are exercised with the same fixtures.
func TestAuthorFilterViewMatchesGroups(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, _ := s.Authors.Create(u.ID, "Author", "", "")
	f1, _ := s.Feeds.Create(u.ID, a.ID, "One", "https://one.dev/feed", "", "", 900)
	f2, _ := s.Feeds.Create(u.ID, a.ID, "Two", "https://two.dev/feed", "", "", 900)
	upsert := func(feedID int64, guid string, cats ...string) {
		t.Helper()
		if _, err := s.Items.Upsert(feedID, Item{
			GUID: guid, Title: guid, Link: "https://x/" + guid,
			Categories: cats, FetchedAt: db.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	upsert(f1.ID, "go", "r/golang", "tutorial")
	upsert(f1.ID, "rust", "r/rust")
	upsert(f2.ID, "note", "r/rust", "note")
	upsert(f2.ID, "misc")

	cases := [][]FilterRule{
		nil,
		{{Kind: FilterKindTag, Action: FilterActionHide, Combine: FilterCombineAny, Values: []string{"r/rust"}}},
		{{Kind: FilterKindTag, Action: FilterActionShow, Combine: FilterCombineAll, Values: []string{"r/rust", "note"}}},
		{{Kind: FilterKindFeed, Action: FilterActionHide, Combine: FilterCombineAny, Values: []string{itoa(f1.ID)}}},
		{
			{Kind: FilterKindTag, Action: FilterActionHide, Combine: FilterCombineAny, Values: []string{"tutorial"}},
			{Kind: FilterKindTag, Action: FilterActionShow, Combine: FilterCombineAny, Values: []string{"r/rust", "r/golang"}},
		},
	}
	names := func(items []ItemWithFeed) []string {
		out := make([]string, 0, len(items))
		for _, it := range items {
			out = append(out, it.Title)
		}
		return out
	}
	equal := func(a, b []string) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}
	for i, rules := range cases {
		if err := s.AuthorFilters.Replace(u.ID, a.ID, rules); err != nil {
			t.Fatal(err)
		}
		view, err := s.Items.List(u.ID, ItemFilter{AuthorID: a.ID, Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		tg, fg, _ := FilterGroups(rules)
		dynamic, _, err := s.Items.listPageTaggedFrom(u.ID, ItemFilter{
			AuthorID: a.ID, TagGroups: tg, FeedGroups: fg,
		}, 50, "items")
		if err != nil {
			t.Fatal(err)
		}
		if !equal(names(view), names(dynamic)) {
			t.Fatalf("case %d: view %v != groups %v", i, names(view), names(dynamic))
		}
	}
}

// TestAuthorScopeAppliesRulesToMembership covers the cross-feed case: an item
// owned by one author's feed can also be a member of another author's feed. On
// the second author's page the second author's rules apply, not the owner's.
func TestAuthorScopeAppliesRulesToMembership(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	owner, _ := s.Authors.Create(u.ID, "Owner", "", "")
	ownerFeed, _ := s.Feeds.Create(u.ID, owner.ID, "Owner feed", "https://owner.example/feed", "", "", 900)
	page, _ := s.Authors.Create(u.ID, "Page", "", "")
	pageFeed, _ := s.Feeds.Create(u.ID, page.ID, "Page feed", "https://page.example/feed", "", "", 900)

	post := Item{
		GUID: "cross", SharedKey: "site:cross", Title: "Cross post",
		Link: "https://x/cross", Categories: []string{"r/shared", "u/blocked"}, FetchedAt: db.Now(),
	}
	if _, err := s.Items.Upsert(ownerFeed.ID, post); err != nil {
		t.Fatalf("owner ingest: %v", err)
	}
	if _, err := s.Items.Upsert(pageFeed.ID, post); err != nil {
		t.Fatalf("page ingest: %v", err)
	}

	// The page author blocks the tag. The owner has no rules.
	if err := s.AuthorFilters.Replace(u.ID, page.ID, []FilterRule{{
		Kind: FilterKindTag, Action: FilterActionHide, Combine: FilterCombineAny, Values: []string{"u/blocked"},
	}}); err != nil {
		t.Fatal(err)
	}

	pageScope := ItemFilter{AuthorID: page.ID}
	if items, _, err := s.Items.ListPage(u.ID, pageScope); err != nil || len(items) != 0 {
		t.Fatalf("page scope = %v, %v; want none", items, err)
	}
	// The ?feed= view of the page is the same membership scope.
	feedScope := ItemFilter{AuthorID: page.ID, FeedID: pageFeed.ID}
	if items, _, err := s.Items.ListPage(u.ID, feedScope); err != nil || len(items) != 0 {
		t.Fatalf("page feed scope = %v, %v; want none", items, err)
	}
	// The owner's page shows it: the item's owner author allows it.
	if items, _, err := s.Items.ListPage(u.ID, ItemFilter{AuthorID: owner.ID}); err != nil || len(items) != 1 {
		t.Fatalf("owner scope = %v, %v; want the post", items, err)
	}
	if n, _ := s.Items.CountFiltered(u.ID, pageScope); n != 0 {
		t.Fatalf("page count = %d, want 0", n)
	}
	if stats, err := s.Items.StatsFilter(u.ID, pageScope); err != nil || stats.Total != 0 {
		t.Fatalf("page stats = %+v, %v; want 0", stats, err)
	}
	if cats, err := s.Items.ListCategoriesFilter(u.ID, pageScope); err != nil || len(cats) != 0 {
		t.Fatalf("page categories = %+v, %v; want none", cats, err)
	}
	// Global reads keep the owner rule (the owner allows it).
	if items, err := s.Items.List(u.ID, ItemFilter{Limit: 10}); err != nil || len(items) != 1 {
		t.Fatalf("global list = %v, %v; want the post", items, err)
	}
	// Mark-all on the page must not touch it.
	if _, err := s.Items.MarkAllReadFiltered(u.ID, pageScope); err != nil {
		t.Fatal(err)
	}
	id, _ := s.Items.ByFeedIdentity(ownerFeed.ID, "cross")
	it, _ := s.Items.ByID(u.ID, id)
	if it.Read {
		t.Fatalf("blocked cross-feed post should stay unread")
	}
}

// TestUnreadCountsByFeedScope covers the grouped per-feed badge query behind an
// author page. It must match CountFiltered for each feed: the author's rules
// apply to items reached through the author's feeds, and a view adds items from
// feeds the author does not own.
func TestUnreadCountsByFeedScope(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, _ := s.Authors.Create(u.ID, "Author", "", "")
	f1, _ := s.Feeds.Create(u.ID, a.ID, "One", "https://one.dev/feed", "", "", 900)
	f2, _ := s.Feeds.Create(u.ID, a.ID, "Two", "https://two.dev/feed", "", "", 900)
	other, _ := s.Authors.Create(u.ID, "Other", "", "")
	fo, _ := s.Feeds.Create(u.ID, other.ID, "Other", "https://other.dev/feed", "", "", 900)

	upsert := func(feedID int64, guid string, cats ...string) {
		t.Helper()
		if _, err := s.Items.Upsert(feedID, Item{
			GUID: guid, Title: guid, Link: "https://x/" + guid,
			Categories: cats, FetchedAt: db.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	upsert(f1.ID, "keep", "r/golang")
	upsert(f1.ID, "blocked", "r/rust")
	upsert(f2.ID, "blocked-two", "r/rust")
	upsert(fo.ID, "cross", "r/other")

	if err := s.AuthorFilters.Replace(u.ID, a.ID, []FilterRule{{
		Kind: FilterKindTag, Action: FilterActionHide, Combine: FilterCombineAny, Values: []string{"r/rust"},
	}}); err != nil {
		t.Fatal(err)
	}

	views := []ViewFilter{{TagGroups: []TagGroup{{Tags: []string{"r/other"}}}}}
	counts, err := s.Items.UnreadCountsByFeedScope(u.ID, a.ID, views)
	if err != nil {
		t.Fatalf("UnreadCountsByFeedScope: %v", err)
	}
	for _, f := range []Feed{f1, f2, fo} {
		want, err := s.Items.CountFiltered(u.ID, ItemFilter{
			AuthorID: a.ID, Views: views, FeedID: f.ID, UnreadOnly: true,
		})
		if err != nil {
			t.Fatalf("CountFiltered feed %d: %v", f.ID, err)
		}
		if counts[f.ID] != want {
			t.Fatalf("feed %d unread = %d, want %d (grouped must match CountFiltered)", f.ID, counts[f.ID], want)
		}
	}
	if counts[f1.ID] != 1 || counts[f2.ID] != 0 || counts[fo.ID] != 1 {
		t.Fatalf("feed counts = %v, want one on f1 and one on the view-only feed", counts)
	}
}

// TestCountTabsFilter checks that the combined unread/read/favorite aggregate
// matches the three separate counts it replaces, including author rules.
func TestCountTabsFilter(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, _ := s.Authors.Create(u.ID, "Author", "", "")
	f, _ := s.Feeds.Create(u.ID, a.ID, "One", "https://one.dev/feed", "", "", 900)

	items := make([]int64, 0, 3)
	for _, guid := range []string{"one", "two", "three"} {
		if _, err := s.Items.Upsert(f.ID, Item{
			GUID: guid, Title: guid, Link: "https://x/" + guid, FetchedAt: db.Now(),
		}); err != nil {
			t.Fatal(err)
		}
		id, err := s.Items.ByFeedIdentity(f.ID, guid)
		if err != nil {
			t.Fatal(err)
		}
		items = append(items, id)
	}
	if err := s.Items.SetRead(u.ID, items[0], true); err != nil {
		t.Fatal(err)
	}
	if err := s.Items.SetFavorite(u.ID, items[0], true); err != nil {
		t.Fatal(err)
	}

	check := func() {
		t.Helper()
		scope := ItemFilter{AuthorID: a.ID}
		got, err := s.Items.CountTabsFilter(u.ID, scope)
		if err != nil {
			t.Fatalf("CountTabsFilter: %v", err)
		}
		unread, _ := s.Items.CountFiltered(u.ID, ItemFilter{AuthorID: a.ID, UnreadOnly: true})
		read, _ := s.Items.CountFiltered(u.ID, ItemFilter{AuthorID: a.ID, ReadOnly: true})
		favs, _ := s.Items.CountFiltered(u.ID, ItemFilter{AuthorID: a.ID, FavoritesOnly: true})
		if got.Unread != unread || got.Read != read || got.Favorites != favs {
			t.Fatalf("tabs = %+v, want unread=%d read=%d favorites=%d", got, unread, read, favs)
		}
	}
	check()
	if err := s.AuthorFilters.Replace(u.ID, a.ID, []FilterRule{{
		Kind: FilterKindTag, Action: FilterActionHide, Combine: FilterCombineAny, Values: []string{"missing"},
	}}); err != nil {
		t.Fatal(err)
	}
	check()
}
