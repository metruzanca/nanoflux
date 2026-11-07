package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// LinkIcon is the cached Simple Icons mapping for an author-link domain. A
// found icon carries the Simple Icons slug and the glyph's path data; a
// confirmed miss has Found=false and empty fields. The cache is global because
// a brand glyph is the same for every user.
type LinkIcon struct {
	Domain    string
	Slug      string
	Paths     []string
	Found     bool
	CheckedAt string
}

// LinkIconStore persists the global domain -> Simple Icons cache.
type LinkIconStore struct{ db *sql.DB }

// Get returns the cached mapping for a domain, or ErrNotFound when the domain
// has never been looked up.
func (s *LinkIconStore) Get(domain string) (LinkIcon, error) {
	row := s.db.QueryRowContext(context.Background(), `
		SELECT domain, slug, paths, found, checked_at
		FROM link_icons WHERE domain = ?`, domain)
	ic, err := scanLinkIcon(row)
	if errors.Is(err, sql.ErrNoRows) {
		return LinkIcon{}, ErrNotFound
	}
	if err != nil {
		return LinkIcon{}, fmt.Errorf("get link icon: %w", err)
	}
	return ic, nil
}

// GetMany returns the cached mappings for the given domains, keyed by domain.
// Domains that have never been looked up are absent from the result.
func (s *LinkIconStore) GetMany(domains []string) (map[string]LinkIcon, error) {
	out := make(map[string]LinkIcon, len(domains))
	if len(domains) == 0 {
		return out, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(domains)), ",")
	args := make([]any, len(domains))
	for i, d := range domains {
		args[i] = d
	}
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT domain, slug, paths, found, checked_at
		FROM link_icons WHERE domain IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("get link icons: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		ic, err := scanLinkIcon(rows)
		if err != nil {
			return nil, fmt.Errorf("scan link icon: %w", err)
		}
		out[ic.Domain] = ic
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get link icons: %w", err)
	}
	return out, nil
}

// List returns every cached mapping, ordered by domain.
func (s *LinkIconStore) List() ([]LinkIcon, error) {
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT domain, slug, paths, found, checked_at
		FROM link_icons ORDER BY domain`)
	if err != nil {
		return nil, fmt.Errorf("list link icons: %w", err)
	}
	defer rows.Close()
	var out []LinkIcon
	for rows.Next() {
		ic, err := scanLinkIcon(rows)
		if err != nil {
			return nil, fmt.Errorf("scan link icon: %w", err)
		}
		out = append(out, ic)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list link icons: %w", err)
	}
	return out, nil
}

// UpsertFound records a successful lookup. Paths are stored newline-joined.
func (s *LinkIconStore) UpsertFound(domain, slug string, paths []string) error {
	return s.upsert(domain, slug, strings.Join(paths, "\n"), true)
}

// UpsertMissing records a confirmed miss, so the domain is not looked up again
// until an admin rechecks it.
func (s *LinkIconStore) UpsertMissing(domain string) error {
	return s.upsert(domain, "", "", false)
}

func (s *LinkIconStore) upsert(domain, slug, paths string, found bool) error {
	_, err := s.db.ExecContext(context.Background(), `
		INSERT INTO link_icons (domain, slug, paths, found, checked_at)
		VALUES (?, ?, ?, ?, datetime('now'))
		ON CONFLICT(domain) DO UPDATE SET
			slug = excluded.slug,
			paths = excluded.paths,
			found = excluded.found,
			checked_at = excluded.checked_at`,
		domain, slug, paths, found)
	if err != nil {
		return fmt.Errorf("upsert link icon: %w", err)
	}
	return nil
}

// Delete forgets a domain's mapping so the next lookup resolves it again.
func (s *LinkIconStore) Delete(domain string) error {
	if _, err := s.db.ExecContext(context.Background(),
		`DELETE FROM link_icons WHERE domain = ?`, domain); err != nil {
		return fmt.Errorf("delete link icon: %w", err)
	}
	return nil
}

func scanLinkIcon(sc interface{ Scan(...any) error }) (LinkIcon, error) {
	var (
		ic    LinkIcon
		paths string
		found int
	)
	if err := sc.Scan(&ic.Domain, &ic.Slug, &paths, &found, &ic.CheckedAt); err != nil {
		return LinkIcon{}, err
	}
	ic.Found = found != 0
	if paths != "" {
		ic.Paths = strings.Split(paths, "\n")
	}
	return ic, nil
}
