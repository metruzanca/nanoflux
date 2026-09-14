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
	if count != 1 {
		t.Errorf("expected 1 migration, got %d", count)
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
