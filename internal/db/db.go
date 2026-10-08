package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// TimeFormat is the canonical timestamp layout for all stored times (UTC).
const TimeFormat = "2006-01-02 15:04:05"

// FormatTime renders a time in TimeFormat (UTC).
func FormatTime(t time.Time) string {
	return t.UTC().Format(TimeFormat)
}

// ParseTime parses a TimeFormat string as UTC.
func ParseTime(s string) (time.Time, error) {
	return time.Parse(TimeFormat, s)
}

// Open opens a sqlite database at path, enabling WAL mode and foreign keys.
// Pass ":memory:" for an in-memory database (always a single connection).
//
// File databases get their pragmas and a write-immediate transaction lock from
// the DSN, so every connection the pool opens inherits them: foreign_keys and
// busy_timeout are per-connection, and _txlock=immediate makes a write
// transaction take the write lock up front, so a check-then-act sequence is
// atomic across pooled connections instead of racing. The returned handle is
// single-connection; a caller that wants a pool raises MaxOpenConns *after*
// running migrations (which toggle foreign_keys and must stay on one conn).
func Open(path string) (*sql.DB, error) {
	memory := path == ":memory:"
	if !memory {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("create data dir: %w", err)
		}
	}

	dsn := path
	if !memory {
		dsn = "file:" + path +
			"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_txlock=immediate"
	}
	sqldb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if memory {
		// Each :memory: connection is a separate database, so it must be one.
		sqldb.SetMaxOpenConns(1)
		for _, pragma := range []string{
			"PRAGMA journal_mode=WAL",
			"PRAGMA foreign_keys=ON",
			"PRAGMA busy_timeout=5000",
		} {
			if _, err := sqldb.Exec(pragma); err != nil {
				sqldb.Close()
				return nil, fmt.Errorf("%s: %w", pragma, err)
			}
		}
	}
	if err := sqldb.Ping(); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	return sqldb, nil
}

// SetConnPool raises a freshly opened handle to a connection pool. Call it
// after Migrate: migrations run on a single connection (they toggle
// foreign_keys), and pooled connections get their pragmas from the DSN.
func SetConnPool(sqldb *sql.DB, maxConns int) {
	if maxConns < 1 {
		maxConns = 1
	}
	sqldb.SetMaxOpenConns(maxConns)
	sqldb.SetMaxIdleConns(maxConns)
}

// Now returns the current UTC time in the format used for all timestamps.
func Now() string {
	return FormatTime(time.Now())
}
