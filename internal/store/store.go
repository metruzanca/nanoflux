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
	Settings    *SettingStore
	Authors     *AuthorStore
	Feeds       *FeedStore
	Items       *ItemStore
	Collections *CollectionStore
	SourceIcons *SourceIconStore
	Filters     *FilterStore
	Shares      *ShareStore
	Lists       *ListStore
	AuthorLinks *AuthorLinkStore
	ViewPrefs   *ViewPrefStore
}

func New(sqldb *sql.DB) *Store {
	q := sqlcgen.New(sqldb)
	policy := URLPolicy(identityPolicy{})
	return &Store{
		db:          sqldb,
		q:           q,
		Users:       &UserStore{q: q, db: sqldb},
		Sessions:    &SessionStore{q: q},
		Settings:    &SettingStore{q: q},
		Authors:     &AuthorStore{q: q},
		Feeds:       &FeedStore{q: q, db: sqldb, policy: policy},
		Items:       &ItemStore{q: q, db: sqldb, policy: policy},
		Collections: &CollectionStore{q: q},
		SourceIcons: &SourceIconStore{q: q},
		Filters:     &FilterStore{q: q},
		Shares:      &ShareStore{q: q},
		Lists:       &ListStore{q: q, policy: policy},
		AuthorLinks: &AuthorLinkStore{q: q},
		ViewPrefs:   &ViewPrefStore{q: q},
	}
}

// SetURLPolicy installs the host's per-URL site rules (from the plugin layer),
// so feed create/edit and the startup canonicalize pass defer site-specific URL
// handling to the owning plugin. A nil policy restores the pass-through default.
func (s *Store) SetURLPolicy(p URLPolicy) {
	if p == nil {
		p = identityPolicy{}
	}
	s.Feeds.policy = p
	s.Items.policy = p
	s.Lists.policy = p
}

// SetItemDecorator installs the view-time item decorator (from the plugin
// layer), so lists can render plugin-owned source attribution and card kinds.
// A nil decorator clears it.
func (s *Store) SetItemDecorator(d ItemDecorator) {
	s.Items.decorator = d
	s.Lists.decorator = d
}

// DB exposes the underlying handle for poller and CLI use.
func (s *Store) DB() *sql.DB { return s.db }
