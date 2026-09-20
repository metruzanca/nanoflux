package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// SourceIcon is a user-configured brand icon for a domain. The icon bytes live
// in object storage; IconKey points at them.
type SourceIcon struct {
	ID            int64
	UserID        int64
	Domain        string
	IconURL       string
	IconKey       string
	LastFetchedAt string
	CreatedAt     string
}

type SourceIconStore struct{ db *sql.DB }

// List returns the user's custom source icons.
func (s *SourceIconStore) List(userID int64) ([]SourceIcon, error) {
	rows, err := s.db.Query(
		`SELECT id, user_id, domain, icon_url, icon_key, last_fetched_at, created_at
		 FROM source_icons WHERE user_id = ? ORDER BY domain`, userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SourceIcon
	for rows.Next() {
		ic, err := scanSourceIcon(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ic)
	}
	return out, rows.Err()
}

// ByDomain returns the user's icon for a domain, or ErrNotFound.
func (s *SourceIconStore) ByDomain(userID int64, domain string) (SourceIcon, error) {
	ic, err := scanSourceIcon(s.db.QueryRow(
		`SELECT id, user_id, domain, icon_url, icon_key, last_fetched_at, created_at
		 FROM source_icons WHERE user_id = ? AND domain = ?`, userID, domain,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return SourceIcon{}, ErrNotFound
	}
	return ic, err
}

// ByID returns a user's icon by id, scoped to the user.
func (s *SourceIconStore) ByID(userID, id int64) (SourceIcon, error) {
	ic, err := scanSourceIcon(s.db.QueryRow(
		`SELECT id, user_id, domain, icon_url, icon_key, last_fetched_at, created_at
		 FROM source_icons WHERE id = ? AND user_id = ?`, id, userID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return SourceIcon{}, ErrNotFound
	}
	return ic, err
}

// Create registers a domain->icon mapping. It returns ErrExists when the user
// already has an icon for the domain.
func (s *SourceIconStore) Create(userID int64, domain, iconURL string) (SourceIcon, error) {
	res, err := s.db.Exec(
		`INSERT INTO source_icons(user_id, domain, icon_url) VALUES(?, ?, ?)`,
		userID, domain, iconURL,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return SourceIcon{}, ErrExists
		}
		return SourceIcon{}, fmt.Errorf("create source icon: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return SourceIcon{}, err
	}
	return s.ByID(userID, id)
}

// SetIconKey records the object-storage key for a cached icon.
func (s *SourceIconStore) SetIconKey(userID, id int64, key, fetchedAt string) error {
	_, err := s.db.Exec(
		`UPDATE source_icons SET icon_key = ?, last_fetched_at = ?
		 WHERE id = ? AND user_id = ?`,
		nullStr(key), fetchedAt, id, userID,
	)
	return err
}

// Delete removes a user's icon mapping.
func (s *SourceIconStore) Delete(userID, id int64) error {
	_, err := s.db.Exec(
		`DELETE FROM source_icons WHERE id = ? AND user_id = ?`, id, userID,
	)
	return err
}

func scanSourceIcon(row scanner) (SourceIcon, error) {
	var ic SourceIcon
	var key, lastFetched sql.NullString
	err := row.Scan(
		&ic.ID, &ic.UserID, &ic.Domain, &ic.IconURL, &key, &lastFetched, &ic.CreatedAt,
	)
	ic.IconKey = key.String
	ic.LastFetchedAt = lastFetched.String
	return ic, err
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
