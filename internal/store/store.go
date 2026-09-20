package store

//go:generate sqlc generate -f ../../sqlc.yaml

import (
	"database/sql"
	"errors"

	"github.com/metruzanca/nanoflux/internal/store/sqlcgen"
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
	q           *sqlcgen.Queries
	Users       *UserStore
	Sessions    *SessionStore
	Authors     *AuthorStore
	Feeds       *FeedStore
	Items       *ItemStore
	Collections *CollectionStore
	SourceIcons *SourceIconStore
}

func New(sqldb *sql.DB) *Store {
	q := sqlcgen.New(sqldb)
	return &Store{
		db:          sqldb,
		q:           q,
		Users:       &UserStore{q: q},
		Sessions:    &SessionStore{q: q},
		Authors:     &AuthorStore{q: q},
		Feeds:       &FeedStore{q: q},
		Items:       &ItemStore{q: q},
		Collections: &CollectionStore{q: q},
		SourceIcons: &SourceIconStore{q: q},
	}
}

// DB exposes the underlying handle for poller and CLI use.
func (s *Store) DB() *sql.DB { return s.db }
