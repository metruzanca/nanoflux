package store

import (
	"strings"
	"testing"

	"github.com/metruzanca/nanoflux/internal/db"
)

// TestNormalizeData covers the one-time pass over rows written before tags,
// usernames and author-link URLs were canonicalized.
func TestNormalizeData(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, _ := s.Authors.Create(u.ID, "Metru", "", "")
	f, _ := s.Feeds.Create(u.ID, a.ID, "Blog", "https://example.test/rss.xml", "", "", 900)

	// An item stored the old way, with mixed-case tags and a case duplicate.
	res, err := s.db.Exec(`
		INSERT INTO items (feed_id, user_id, guid, dedup_key, title, link, categories, fetched_at)
		VALUES (?, ?, 'g1', 'g1', 'Post', 'https://example.test/1', ?, ?)`,
		f.ID, u.ID, "R/Cats\nu/Sam\nr/cats", db.Now())
	if err != nil {
		t.Fatalf("insert item: %v", err)
	}
	itemID, _ := res.LastInsertId()
	for _, c := range []string{"R/Cats", "u/Sam", "r/cats"} {
		if _, err := s.db.Exec(`INSERT INTO item_categories (item_id, category) VALUES (?, ?)`, itemID, c); err != nil {
			t.Fatalf("insert item category: %v", err)
		}
	}

	// A tag-kind author rule with an uppercase value.
	res, err = s.db.Exec(`
		INSERT INTO author_filter_rules (author_id, action, kind, combine)
		VALUES (?, 'show', 'tag', 'all')`, a.ID)
	if err != nil {
		t.Fatalf("insert author rule: %v", err)
	}
	ruleID, _ := res.LastInsertId()
	if _, err := s.db.Exec(`INSERT INTO author_filter_values (rule_id, value) VALUES (?, 'U/SAM')`, ruleID); err != nil {
		t.Fatalf("insert author filter value: %v", err)
	}

	// Display labels and an author link written with stray whitespace and an
	// uppercase host. A non-default LAN port must survive.
	if _, err := s.db.Exec(`UPDATE authors SET name = '  Metru   Dev ' WHERE id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE feeds SET title = 'My   Blog' WHERE id = ?`, f.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`
		INSERT INTO author_links (user_id, author_id, label, url)
		VALUES (?, ?, '  Twitch  ', 'HTTP://NixOS:8090/Path')`, u.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	// An unlabeled link stores a NULL label, which the pass must scan safely.
	if _, err := s.db.Exec(`
		INSERT INTO author_links (user_id, author_id, label, url)
		VALUES (?, ?, NULL, 'https://EXAMPLE.com/')`, u.ID, a.ID); err != nil {
		t.Fatal(err)
	}

	// An account stored before usernames were lowercased.
	if _, err := s.db.Exec(`INSERT INTO users (username, password_hash) VALUES ('Bob', 'x')`); err != nil {
		t.Fatalf("insert user: %v", err)
	}

	rep, err := s.NormalizeData()
	if err != nil {
		t.Fatalf("NormalizeData: %v", err)
	}
	if rep.Total() == 0 {
		t.Fatal("expected a non-empty report")
	}

	var cats string
	if err := s.db.QueryRow(`SELECT categories FROM items WHERE id = ?`, itemID).Scan(&cats); err != nil {
		t.Fatal(err)
	}
	if cats != "r/cats\nu/sam" {
		t.Fatalf("items.categories = %q, want canonical lowercase and deduped", cats)
	}

	var tagRows int
	if err := s.db.QueryRow(`
		SELECT COUNT(*) FROM item_categories WHERE item_id = ? AND category IN ('r/cats', 'u/sam')`,
		itemID).Scan(&tagRows); err != nil {
		t.Fatal(err)
	}
	if tagRows != 2 {
		t.Fatalf("item_categories has %d canonical rows, want 2", tagRows)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM item_categories WHERE item_id = ?`, itemID).Scan(&tagRows); err != nil {
		t.Fatal(err)
	}
	if tagRows != 2 {
		t.Fatalf("item_categories has %d rows, want 2 after dedupe", tagRows)
	}

	var filterValue string
	if err := s.db.QueryRow(`SELECT value FROM author_filter_values WHERE rule_id = ?`, ruleID).Scan(&filterValue); err != nil {
		t.Fatal(err)
	}
	if filterValue != "u/sam" {
		t.Fatalf("author filter value = %q, want u/sam", filterValue)
	}

	// The lowercased account resolves through the normal case-insensitive path.
	gotUser, err := s.Users.ByUsername("BOB")
	if err != nil || gotUser.Username != "bob" {
		t.Fatalf("ByUsername(BOB) = %+v, %v", gotUser, err)
	}

	var authorName string
	if err := s.db.QueryRow(`SELECT name FROM authors WHERE id = ?`, a.ID).Scan(&authorName); err != nil {
		t.Fatal(err)
	}
	if authorName != "Metru Dev" {
		t.Fatalf("author name = %q, want %q", authorName, "Metru Dev")
	}

	var linkURL, linkLabel string
	if err := s.db.QueryRow(`SELECT url, label FROM author_links WHERE author_id = ? ORDER BY id LIMIT 1`, a.ID).Scan(&linkURL, &linkLabel); err != nil {
		t.Fatal(err)
	}
	if linkURL != "http://nixos:8090/Path" {
		t.Fatalf("author link url = %q, want http://nixos:8090/Path", linkURL)
	}
	if linkLabel != "Twitch" {
		t.Fatalf("author link label = %q, want Twitch", linkLabel)
	}

	// The marker makes a second run a no-op.
	again, err := s.NormalizeData()
	if err != nil {
		t.Fatalf("second NormalizeData: %v", err)
	}
	if again.Total() != 0 {
		t.Fatalf("second pass changed %d rows, want 0", again.Total())
	}
}

// TestNormalizeCategoryCaseAtWriteChokepoints guards the write-path
// canonicalization: a mixed-case tag and a whitespace variant collapse even
// without the startup pass.
func TestNormalizeCategoryCaseAtWriteChokepoints(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, _ := s.Authors.Create(u.ID, "Metru", "", "")
	f, _ := s.Feeds.Create(u.ID, a.ID, "Blog", "https://example.test/rss.xml", "", "", 900)

	if _, err := s.Items.Upsert(f.ID, Item{
		GUID: "g1", Title: "Post", Link: "https://example.test/1",
		Categories: []string{" R/Cats ", "r/cats", "U/Sam"}, FetchedAt: db.Now(),
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	itemID, _ := s.Items.ByFeedIdentity(f.ID, "g1")
	it, err := s.Items.ByID(u.ID, itemID)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(it.Categories, ","); got != "r/cats,u/sam" {
		t.Fatalf("stored categories = %q, want r/cats,u/sam", got)
	}

	cats, err := s.Items.ListCategories(u.ID, ItemFilter{FeedID: f.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(cats) != 2 || cats[0].Category != "r/cats" || cats[1].Category != "u/sam" {
		t.Fatalf("ListCategories = %+v, want r/cats and u/sam", cats)
	}

	// A filter value in any case matches the stored tag.
	items, _, err := s.Items.ListPage(u.ID, ItemFilter{
		FeedID: f.ID, TagGroups: []TagGroup{{Tags: []string{"R/CATS"}, All: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("uppercase filter matched %d items, want 1", len(items))
	}
}
