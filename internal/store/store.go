package store

import (
	"database/sql"
	"errors"
)

var (
	ErrNotFound = errors.New("not found")
	ErrExists   = errors.New("already exists")
)

// Store bundles all repositories over a single sqlite connection. The CLI
// (and later the MCP server) use the same Store, so admin operations always
// share the app's data access paths.
type Store struct {
	db          *sql.DB
	Users       *UserStore
	Sessions    *SessionStore
	Authors     *AuthorStore
	Feeds       *FeedStore
	Items       *ItemStore
	Collections *CollectionStore
}

func New(sqldb *sql.DB) *Store {
	return &Store{
		db:          sqldb,
		Users:       &UserStore{db: sqldb},
		Sessions:    &SessionStore{db: sqldb},
		Authors:     &AuthorStore{db: sqldb},
		Feeds:       &FeedStore{db: sqldb},
		Items:       &ItemStore{db: sqldb},
		Collections: &CollectionStore{db: sqldb},
	}
}

// DB exposes the underlying handle for poller and CLI use.
func (s *Store) DB() *sql.DB { return s.db }

// nullStr returns nil for empty strings so optional columns stay NULL.
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
