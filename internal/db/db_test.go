package db

import (
	"testing"
)

func TestMigrate(t *testing.T) {
	sqldb, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer sqldb.Close()

	if err := Migrate(sqldb); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	tables := []string{
		"users", "sessions", "authors", "feeds",
		"items", "collections", "collection_feeds", "schema_migrations",
	}
	for _, name := range tables {
		var n int
		if err := sqldb.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, name,
		).Scan(&n); err != nil {
			t.Fatalf("query %s: %v", name, err)
		}
		if n != 1 {
			t.Errorf("table %s not created", name)
		}
	}

	// Second run must be a no-op.
	if err := Migrate(sqldb); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
	var count int
	if err := sqldb.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != len(migrations) {
		t.Errorf("expected %d migrations, got %d", len(migrations), count)
	}
}

// TestMigrateUpgrade simulates a v1 database with data and verifies the
// rebuilds (v2, v20) preserve rows.
func TestMigrateUpgrade(t *testing.T) {
	sqldb, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer sqldb.Close()

	// Apply only migration 1 and mark it applied.
	if _, err := sqldb.Exec(schemaV1); err != nil {
		t.Fatalf("apply v1: %v", err)
	}
	if _, err := sqldb.Exec(
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := sqldb.Exec(
		`INSERT INTO schema_migrations(version, applied_at) VALUES(1, 'x')`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := sqldb.Exec(
		`INSERT INTO users(id, username, password_hash) VALUES(1, 'u', 'h');
		 INSERT INTO authors(id, user_id, name) VALUES(1, 1, 'a');
		 INSERT INTO feeds(id, user_id, author_id, title, feed_url) VALUES(1, 1, 1, 'f', 'https://f/x');
		 INSERT INTO items(feed_id, guid, title) VALUES(1, 'g', 'i');`,
	); err != nil {
		t.Fatalf("seed v1: %v", err)
	}

	if err := Migrate(sqldb); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Data survived the rebuild.
	var (
		title  string
		author int64
	)
	if err := sqldb.QueryRow(`SELECT title, author_id FROM feeds WHERE id = 1`).Scan(&title, &author); err != nil {
		t.Fatalf("read feed: %v", err)
	}
	if title != "f" || author != 1 {
		t.Fatalf("feed after upgrade: title=%q author=%d", title, author)
	}
	var itemCount int
	if err := sqldb.QueryRow(`SELECT COUNT(*) FROM items`).Scan(&itemCount); err != nil {
		t.Fatal(err)
	}
	if itemCount != 1 {
		t.Fatalf("items after upgrade: %d", itemCount)
	}

	// author_id is now required (schemaV20), not nullable.
	if _, err := sqldb.Exec(
		`INSERT INTO feeds(id, user_id, author_id, title, feed_url) VALUES(2, 1, NULL, 'f2', 'https://f/2')`,
	); err == nil {
		t.Fatalf("author_id must be NOT NULL after schemaV20")
	}
}

// TestMigrateBackfillsAuthorlessFeeds builds a v2-era database holding an
// authorless feed and verifies schemaV20 backfills an author for it and makes
// author_id NOT NULL.
func TestMigrateBackfillsAuthorlessFeeds(t *testing.T) {
	sqldb, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer sqldb.Close()

	// v1 + v2 (v2 makes author_id nullable).
	if _, err := sqldb.Exec(schemaV1); err != nil {
		t.Fatalf("apply v1: %v", err)
	}
	if _, err := sqldb.Exec(schemaV2); err != nil {
		t.Fatalf("apply v2: %v", err)
	}
	if _, err := sqldb.Exec(
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL);
		 INSERT INTO schema_migrations(version, applied_at) VALUES(1, 'x'), (2, 'x');`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := sqldb.Exec(
		`INSERT INTO users(id, username, password_hash) VALUES(1, 'u', 'h');
		 INSERT INTO authors(id, user_id, name, url) VALUES(1, 1, 'authored', 'https://a');
		 INSERT INTO feeds(id, user_id, author_id, title, feed_url, home_url) VALUES(1, 1, 1, 'authored', 'https://a/x', 'https://a');
		 INSERT INTO feeds(id, user_id, author_id, title, feed_url, home_url) VALUES(2, 1, NULL, 'orphan', 'https://o/x', 'https://o');`,
	); err != nil {
		t.Fatalf("seed v2: %v", err)
	}

	if err := Migrate(sqldb); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// The authorless feed was assigned a backfilled author, named after the feed.
	var authorID int64
	if err := sqldb.QueryRow(`SELECT author_id FROM feeds WHERE id = 2`).Scan(&authorID); err != nil {
		t.Fatalf("read orphan feed: %v", err)
	}
	if authorID == 0 {
		t.Fatal("orphan feed should have been assigned an author")
	}
	var backfilled string
	if err := sqldb.QueryRow(`SELECT name FROM authors WHERE id = ?`, authorID).Scan(&backfilled); err != nil {
		t.Fatalf("read backfilled author: %v", err)
	}
	if backfilled != "orphan" {
		t.Fatalf("backfilled author name = %q, want %q", backfilled, "orphan")
	}
	// The already-authored feed is untouched.
	var title string
	var aID int64
	if err := sqldb.QueryRow(`SELECT title, author_id FROM feeds WHERE id = 1`).Scan(&title, &aID); err != nil {
		t.Fatal(err)
	}
	if title != "authored" || aID != 1 {
		t.Fatalf("authored feed changed: title=%q author=%d", title, aID)
	}
	// New feeds must now carry an author.
	if _, err := sqldb.Exec(
		`INSERT INTO feeds(id, user_id, author_id, title, feed_url) VALUES(3, 1, NULL, 'f3', 'https://f/3')`,
	); err == nil {
		t.Fatal("author_id must be NOT NULL after schemaV20")
	}
}

func TestForeignKeys(t *testing.T) {
	sqldb, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer sqldb.Close()

	// A feed referencing a nonexistent author must fail.
	if _, err := sqldb.Exec(
		`INSERT INTO feeds(user_id, author_id, title, feed_url) VALUES(1, 999, 'x', 'http://x')`,
	); err == nil {
		t.Fatal("expected foreign key violation")
	}
}
