package store

import (
	"database/sql"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/metruzanca/nanoflux/internal/db"
)

// TestSchemaMatchesMigrations guards the hand-maintained schema.sql against
// drift from the migration chain that real databases use.
func TestSchemaMatchesMigrations(t *testing.T) {
	fromFile, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open schema db: %v", err)
	}
	t.Cleanup(func() { fromFile.Close() })
	schema, err := os.ReadFile("schema.sql")
	if err != nil {
		t.Fatalf("read schema.sql: %v", err)
	}
	if _, err := fromFile.Exec(string(schema)); err != nil {
		t.Fatalf("apply schema.sql: %v", err)
	}

	fromMigrations, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open migration db: %v", err)
	}
	t.Cleanup(func() { fromMigrations.Close() })
	if err := db.Migrate(fromMigrations); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// schema.sql declares idx_items_cross for sqlc, but the migration chain
	// creates it from the startup merge instead.
	if _, err := New(fromMigrations).Items.MergeCrossFeedDuplicates(false); err != nil {
		t.Fatalf("create cross-feed index: %v", err)
	}

	compareSchemas(t, fromFile, fromMigrations)
}

func compareSchemas(t *testing.T, want, got *sql.DB) {
	t.Helper()
	wantTables := tableNames(t, want)
	gotTables := tableNames(t, got)
	if !equalStrings(wantTables, gotTables) {
		t.Errorf("table set differs:\nschema.sql: %v\nmigrations: %v", wantTables, gotTables)
	}
	for _, table := range wantTables {
		if a, b := tableColumns(t, want, table), tableColumns(t, got, table); !equalStrings(a, b) {
			t.Errorf("table %s columns differ:\nschema.sql: %v\nmigrations: %v", table, a, b)
		}
		if a, b := foreignKeys(t, want, table), foreignKeys(t, got, table); !equalStrings(a, b) {
			t.Errorf("table %s foreign keys differ:\nschema.sql: %v\nmigrations: %v", table, a, b)
		}
		if a, b := indexes(t, want, table), indexes(t, got, table); !equalStrings(a, b) {
			t.Errorf("table %s indexes differ:\nschema.sql: %v\nmigrations: %v", table, a, b)
		}
	}
	for _, kind := range []string{"view", "trigger"} {
		if a, b := objectSQL(t, want, kind), objectSQL(t, got, kind); !equalStrings(a, b) {
			t.Errorf("%s set differs:\nschema.sql: %v\nmigrations: %v", kind, a, b)
		}
	}
}

func tableNames(t *testing.T, sqldb *sql.DB) []string {
	t.Helper()
	rows, err := sqldb.Query(`SELECT name FROM sqlite_master
		WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name <> 'schema_migrations'
		ORDER BY name`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table: %v", err)
		}
		out = append(out, name)
	}
	return out
}

func tableColumns(t *testing.T, sqldb *sql.DB, table string) []string {
	t.Helper()
	rows, err := sqldb.Query(fmt.Sprintf("PRAGMA table_info(%q)", table))
	if err != nil {
		t.Fatalf("table_info %s: %v", table, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info %s: %v", table, err)
		}
		out = append(out, fmt.Sprintf("%s|%s|%d|%s|%d", name, typ, notNull, dflt.String, pk))
	}
	sort.Strings(out)
	return out
}

func foreignKeys(t *testing.T, sqldb *sql.DB, table string) []string {
	t.Helper()
	rows, err := sqldb.Query(fmt.Sprintf("PRAGMA foreign_key_list(%q)", table))
	if err != nil {
		t.Fatalf("foreign_key_list %s: %v", table, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id, seq int
		var refTable, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &refTable, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			t.Fatalf("scan foreign_key_list %s: %v", table, err)
		}
		out = append(out, fmt.Sprintf("%d|%d|%s|%s|%s|%s|%s|%s", id, seq, refTable, from, to, onUpdate, onDelete, match))
	}
	sort.Strings(out)
	return out
}

func indexes(t *testing.T, sqldb *sql.DB, table string) []string {
	t.Helper()
	rows, err := sqldb.Query(fmt.Sprintf("PRAGMA index_list(%q)", table))
	if err != nil {
		t.Fatalf("index_list %s: %v", table, err)
	}
	defer rows.Close()
	type index struct {
		name    string
		unique  int
		origin  string
		partial int
	}
	var list []index
	for rows.Next() {
		var seq int
		var ix index
		if err := rows.Scan(&seq, &ix.name, &ix.unique, &ix.origin, &ix.partial); err != nil {
			t.Fatalf("scan index_list %s: %v", table, err)
		}
		list = append(list, ix)
	}
	var out []string
	for _, ix := range list {
		if strings.HasPrefix(ix.name, "sqlite_autoindex_") {
			out = append(out, fmt.Sprintf("auto|%d|%d|%s", ix.unique, ix.partial, indexColumns(t, sqldb, ix.name)))
			continue
		}
		out = append(out, fmt.Sprintf("%s|%d|%d|%s", ix.name, ix.unique, ix.partial, objectOneSQL(t, sqldb, "index", ix.name)))
	}
	sort.Strings(out)
	return out
}

func indexColumns(t *testing.T, sqldb *sql.DB, index string) string {
	t.Helper()
	rows, err := sqldb.Query(fmt.Sprintf("PRAGMA index_info(%q)", index))
	if err != nil {
		t.Fatalf("index_info %s: %v", index, err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var seqno, cid int
		var name sql.NullString
		if err := rows.Scan(&seqno, &cid, &name); err != nil {
			t.Fatalf("scan index_info %s: %v", index, err)
		}
		cols = append(cols, name.String)
	}
	return strings.Join(cols, ",")
}

func objectSQL(t *testing.T, sqldb *sql.DB, kind string) []string {
	t.Helper()
	rows, err := sqldb.Query(`SELECT name, COALESCE(sql, '') FROM sqlite_master
		WHERE type = ? AND name NOT LIKE 'sqlite_%' ORDER BY name`, kind)
	if err != nil {
		t.Fatalf("list %s: %v", kind, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name, sqlText string
		if err := rows.Scan(&name, &sqlText); err != nil {
			t.Fatalf("scan %s: %v", kind, err)
		}
		out = append(out, name+"|"+normalizeSQL(sqlText))
	}
	return out
}

func objectOneSQL(t *testing.T, sqldb *sql.DB, kind, name string) string {
	t.Helper()
	var sqlText sql.NullString
	if err := sqldb.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type = ? AND name = ?`, kind, name,
	).Scan(&sqlText); err != nil {
		t.Fatalf("read %s %s: %v", kind, name, err)
	}
	return normalizeSQL(sqlText.String)
}

// normalizeSQL strips line comments, collapses whitespace, and lowercases, so a
// formatting-only difference between schema.sql and a migration is ignored.
func normalizeSQL(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte(' ')
	}
	return strings.ToLower(strings.Join(strings.Fields(b.String()), " "))
}

func equalStrings(a, b []string) bool {
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
