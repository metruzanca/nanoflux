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
	// The stored tag is canonical lowercase even though the fixture supplied
	// "u/Captain", and the uppercase view rule still matches it.
	cats, err := s.Items.ListCategoriesFilter(u.ID, onlyView)
	if err != nil || len(cats) != 1 || cats[0].Category != "u/captain" {
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

// TestMatchingViews reports which view predicates each item satisfies, using
// the same clause that scopes a collection list.
func TestMatchingViews(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	author, _ := s.Authors.Create(u.ID, "Blog", "", "")
	feed, _ := s.Feeds.Create(u.ID, author.ID, "Blog", "https://blog.example/rss.xml", "", "", 900)

	for _, it := range []Item{
		{GUID: "go", Title: "Go post", Link: "https://blog.example/go", Categories: []string{"r/golang"}, FetchedAt: db.Now()},
		{GUID: "rust", Title: "Rust post", Link: "https://blog.example/rust", Categories: []string{"r/rust"}, FetchedAt: db.Now()},
		{GUID: "both", Title: "Both post", Link: "https://blog.example/both", Categories: []string{"r/golang", "r/rust"}, FetchedAt: db.Now()},
	} {
		if _, err := s.Items.Upsert(feed.ID, it); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}
	goID, _ := s.Items.ByFeedIdentity(feed.ID, "go")
	rustID, _ := s.Items.ByFeedIdentity(feed.ID, "rust")
	bothID, _ := s.Items.ByFeedIdentity(feed.ID, "both")

	views := []ViewFilter{
		{TagGroups: []TagGroup{{Tags: []string{"r/golang"}, All: true}}},
		{TagGroups: []TagGroup{{Tags: []string{"r/rust"}, All: true}}},
	}
	matched, err := s.Items.MatchingViews(u.ID, []int64{goID, rustID, bothID}, views)
	if err != nil {
		t.Fatalf("matching views: %v", err)
	}
	if got := matched[goID]; len(got) != 1 || got[0] != 0 {
		t.Fatalf("go should match view 0, got %v", got)
	}
	if got := matched[rustID]; len(got) != 1 || got[0] != 1 {
		t.Fatalf("rust should match view 1, got %v", got)
	}
	if got := matched[bothID]; len(got) != 2 || got[0] != 0 || got[1] != 1 {
		t.Fatalf("both should match both views, got %v", got)
	}
	// Another user's items never match.
	other := mustUser(t, s, "bob")
	if got, _ := s.Items.MatchingViews(other.ID, []int64{goID}, views); len(got) != 0 {
		t.Fatalf("cross-user match = %v", got)
	}
}

// TestViewFilterUsesAssignedAuthorRules covers the rule source of a saved view:
// its items follow the author the view is assigned to, not the author who owns
// the feed they come from. An unassigned view has no rules at all.
func TestViewFilterUsesAssignedAuthorRules(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")

	aggregate, _ := s.Authors.Create(u.ID, "Aggregate", "", "")
	feed, _ := s.Feeds.Create(u.ID, aggregate.ID, "Sub", "https://sub.example/rss.xml", "", "", 900)
	captain, _ := s.Authors.Create(u.ID, "Captain", "", "")

	upsert := func(guid, title, tag string) int64 {
		t.Helper()
		if _, err := s.Items.Upsert(feed.ID, Item{
			GUID: guid, Title: title, Link: "https://x/" + guid,
			Categories: []string{tag}, FetchedAt: db.Now(),
		}); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		id, err := s.Items.ByFeedIdentity(feed.ID, guid)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	aggID := upsert("agg", "Aggregate post", "u/aggregate")
	capID := upsert("cap", "Captain post", "u/captain")

	// The aggregator blocks the captain's reposts from its own feeds.
	if err := s.AuthorFilters.Replace(u.ID, aggregate.ID, []FilterRule{{
		Kind: FilterKindTag, Action: FilterActionHide, Combine: FilterCombineAny, Values: []string{"u/captain"},
	}}); err != nil {
		t.Fatal(err)
	}
	// The captain blocks the aggregator's own posts.
	if err := s.AuthorFilters.Replace(u.ID, captain.ID, []FilterRule{{
		Kind: FilterKindTag, Action: FilterActionHide, Combine: FilterCombineAny, Values: []string{"u/aggregate"},
	}}); err != nil {
		t.Fatal(err)
	}

	capView := ViewFilter{
		AuthorID:  captain.ID,
		TagGroups: []TagGroup{{Tags: []string{"u/captain"}, All: true}},
	}
	aggView := ViewFilter{
		AuthorID:  captain.ID,
		TagGroups: []TagGroup{{Tags: []string{"u/aggregate"}, All: true}},
	}
	freeView := ViewFilter{TagGroups: []TagGroup{{Tags: []string{"u/captain"}, All: true}}}

	titles := func(items []ItemWithFeed) []string {
		out := make([]string, 0, len(items))
		for _, it := range items {
			out = append(out, it.Title)
		}
		return out
	}
	list := func(f ItemFilter) []string {
		t.Helper()
		items, _, err := s.Items.ListPage(u.ID, f)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		return titles(items)
	}

	// The aggregator's page still hides the captain's post.
	if got := list(ItemFilter{AuthorID: aggregate.ID}); len(got) != 1 || got[0] != "Aggregate post" {
		t.Fatalf("aggregator scope = %v, want [Aggregate post]", got)
	}

	// The captain's view sees the post the aggregator blocks, because the view
	// follows the captain's rules (which do not block it).
	if got := list(ItemFilter{Views: []ViewFilter{capView}}); len(got) != 1 || got[0] != "Captain post" {
		t.Fatalf("captain view = %v, want [Captain post]", got)
	}
	// A view matching the aggregator's post is hidden by the captain's rule.
	if got := list(ItemFilter{Views: []ViewFilter{aggView}}); len(got) != 0 {
		t.Fatalf("captain view of blocked post = %v, want none", got)
	}
	// The captain's union is the view under the captain's rules.
	if got := list(ItemFilter{AuthorID: captain.ID, Views: []ViewFilter{capView, aggView}}); len(got) != 1 || got[0] != "Captain post" {
		t.Fatalf("captain union = %v, want [Captain post]", got)
	}
	// An unassigned view has no author rules, so it shows what the aggregator
	// hides from itself.
	if got := list(ItemFilter{Views: []ViewFilter{freeView}}); len(got) != 1 || got[0] != "Captain post" {
		t.Fatalf("unassigned view = %v, want [Captain post]", got)
	}

	// Counts, stats and tag options agree with the view's list.
	onlyCap := ItemFilter{Views: []ViewFilter{capView}}
	if n, _ := s.Items.CountFiltered(u.ID, onlyCap); n != 1 {
		t.Fatalf("captain view count = %d, want 1", n)
	}
	if stats, err := s.Items.StatsFilter(u.ID, onlyCap); err != nil || stats.Total != 1 || stats.Unread != 1 {
		t.Fatalf("captain view stats = %+v, %v", stats, err)
	}
	if cats, err := s.Items.ListCategoriesFilter(u.ID, onlyCap); err != nil || len(cats) != 1 || cats[0].Category != "u/captain" {
		t.Fatalf("captain view categories = %+v, %v", cats, err)
	}

	// A collection's member view carries the same rule source.
	col, _ := s.Collections.Create(u.ID, "Slice")
	collectionScope := ItemFilter{
		CollectionID:    col.ID,
		CollectionViews: []ViewFilter{capView},
	}
	if got := list(collectionScope); len(got) != 1 || got[0] != "Captain post" {
		t.Fatalf("collection view scope = %v, want [Captain post]", got)
	}
	if got, err := s.Items.MatchingViews(u.ID, []int64{capID}, []ViewFilter{capView}); err != nil || len(got[capID]) != 1 {
		t.Fatalf("matching views = %v, %v; want the captain view", got, err)
	}

	// Mark-all on the view marks exactly its items read.
	if _, err := s.Items.MarkAllReadFiltered(u.ID, onlyCap); err != nil {
		t.Fatalf("mark view read: %v", err)
	}
	capItem, _ := s.Items.ByID(u.ID, capID)
	aggItem, _ := s.Items.ByID(u.ID, aggID)
	if !capItem.Read {
		t.Fatalf("captain post should be read")
	}
	if aggItem.Read {
		t.Fatalf("aggregate post should stay unread")
	}
}

// TestAuthorRulesVisibleSQLMatchesView guards the duplicated rule expression:
// the Go-built per-author predicate must agree with the visible_items view for
// the owner-author case.
func TestAuthorRulesVisibleSQLMatchesView(t *testing.T) {
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
			t.Fatalf("upsert: %v", err)
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
	ids := func(q string, args ...any) []int64 {
		t.Helper()
		rows, err := s.db.Query(q, args...)
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		defer rows.Close()
		var out []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			out = append(out, id)
		}
		return out
	}
	equal := func(a, b []int64) bool {
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
	built := `SELECT i.id FROM items i JOIN feeds f ON f.id = i.feed_id
		WHERE i.user_id = ? AND (` + authorRulesVisibleSQL("i.id", "f.author_id") + `) ORDER BY i.id`
	for i, rules := range cases {
		if err := s.AuthorFilters.Replace(u.ID, a.ID, rules); err != nil {
			t.Fatal(err)
		}
		fromView := ids(`SELECT id FROM visible_items WHERE user_id = ? ORDER BY id`, u.ID)
		fromBuilder := ids(built, u.ID, a.ID, a.ID)
		if !equal(fromView, fromBuilder) {
			t.Fatalf("case %d: visible_items %v != authorRulesVisibleSQL %v", i, fromView, fromBuilder)
		}
	}
}
