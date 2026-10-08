package store

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/metruzanca/nanoflux/internal/db"
)

// newPooledStore opens a file-backed store with a multi-connection pool, the
// configuration the atomicity hardening exists for.
func newPooledStore(t *testing.T) *Store {
	t.Helper()
	sqldb, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { sqldb.Close() })
	if err := db.Migrate(sqldb); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	db.SetConnPool(sqldb, 8)
	return New(sqldb)
}

// TestConcurrentEnsureList hammers the get-or-create path: every caller must
// resolve to the same list, not create duplicates.
func TestConcurrentEnsureList(t *testing.T) {
	s := newPooledStore(t)
	u, err := s.Users.Create("u", "x")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	const n = 20
	ids := make([]int64, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			l, err := s.Lists.Ensure(u.ID, "shared")
			if err != nil {
				t.Errorf("ensure: %v", err)
				return
			}
			ids[i] = l.ID
		}(i)
	}
	wg.Wait()
	for _, id := range ids {
		if id != ids[0] {
			t.Fatalf("Ensure created multiple lists: %v", ids)
		}
	}
}

// TestConcurrentEnsureAutoCollection is the collection twin of the list test.
func TestConcurrentEnsureAutoCollection(t *testing.T) {
	s := newPooledStore(t)
	u, err := s.Users.Create("u", "x")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	const n = 20
	ids := make([]int64, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, err := s.Collections.EnsureAuto(u.ID, "example.com")
			if err != nil {
				t.Errorf("ensure auto: %v", err)
				return
			}
			ids[i] = c.ID
		}(i)
	}
	wg.Wait()
	for _, id := range ids {
		if id != ids[0] {
			t.Fatalf("EnsureAuto created multiple collections: %v", ids)
		}
	}
}

// TestConcurrentShareFavorites hammers the token get-or-create: every caller
// must get the same token, not a divergent one.
func TestConcurrentShareFavorites(t *testing.T) {
	s := newPooledStore(t)
	u, err := s.Users.Create("u", "x")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	const n = 20
	toks := make([]string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tok, err := s.Users.ShareFavorites(u.ID)
			if err != nil {
				t.Errorf("share favorites: %v", err)
				return
			}
			toks[i] = tok
		}(i)
	}
	wg.Wait()
	for _, tok := range toks {
		if tok == "" || tok != toks[0] {
			t.Fatalf("ShareFavorites produced divergent tokens: %v", toks)
		}
	}
}
