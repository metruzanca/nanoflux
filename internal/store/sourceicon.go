package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/metruzanca/nanoflux/internal/store/sqlcgen"
)

// SourceIcon is an auto-fetched brand icon for a domain. The icon bytes live
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

type SourceIconStore struct{ q *sqlcgen.Queries }

// ByDomain returns the user's icon for a domain, or ErrNotFound.
func (s *SourceIconStore) ByDomain(userID int64, domain string) (SourceIcon, error) {
	ic, err := s.q.GetSourceIconByDomain(context.Background(), sqlcgen.GetSourceIconByDomainParams{
		UserID: userID,
		Domain: domain,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return SourceIcon{}, ErrNotFound
	}
	if err != nil {
		return SourceIcon{}, err
	}
	return toSourceIcon(ic.ID, ic.UserID, ic.Domain, ic.IconUrl, ic.IconKey, ic.LastFetchedAt, ic.CreatedAt), nil
}

// Create registers a domain->icon mapping. It returns ErrExists when the user
// already has an icon for the domain.
func (s *SourceIconStore) Create(userID int64, domain, iconURL string) (SourceIcon, error) {
	ic, err := s.q.CreateSourceIcon(context.Background(), sqlcgen.CreateSourceIconParams{
		UserID:  userID,
		Domain:  domain,
		IconUrl: iconURL,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return SourceIcon{}, ErrExists
		}
		return SourceIcon{}, fmt.Errorf("create source icon: %w", err)
	}
	return toSourceIcon(ic.ID, ic.UserID, ic.Domain, ic.IconUrl, ic.IconKey, ic.LastFetchedAt, ic.CreatedAt), nil
}

// SetIconKey records the object-storage key for a cached icon.
func (s *SourceIconStore) SetIconKey(userID, id int64, key, fetchedAt string) error {
	return s.q.SetSourceIconKey(context.Background(), sqlcgen.SetSourceIconKeyParams{
		IconKey:       ns(key),
		LastFetchedAt: ns(fetchedAt),
		ID:            id,
		UserID:        userID,
	})
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
