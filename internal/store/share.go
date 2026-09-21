package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/metruzanca/nanoflux/internal/store/sqlcgen"
)

// Share is a public link to one item. The token is random and unguessable;
// there is no endpoint that lists shares.
type Share struct {
	ID        int64
	ItemID    int64
	Token     string
	CreatedAt string
}

type ShareStore struct{ q *sqlcgen.Queries }

func toShare(s sqlcgen.SharedItem) Share {
	return Share{ID: s.ID, ItemID: s.ItemID, Token: s.Token, CreatedAt: s.CreatedAt}
}

func newToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Create shares an item, returning the existing share if it is already shared.
// The item must belong to userID.
func (s *ShareStore) Create(userID, itemID int64) (Share, error) {
	if sh, err := s.ByItem(userID, itemID); err == nil {
		return sh, nil
	}
	token, err := newToken()
	if err != nil {
		return Share{}, err
	}
	sh, err := s.q.CreateShare(context.Background(), sqlcgen.CreateShareParams{
		ItemID: itemID,
		Token:  token,
	})
	if err != nil {
		return Share{}, fmt.Errorf("create share: %w", err)
	}
	return toShare(sh), nil
}

// ByItem returns the share for one of the user's items, or ErrNotFound.
func (s *ShareStore) ByItem(userID, itemID int64) (Share, error) {
	sh, err := s.q.GetShareByItem(context.Background(), sqlcgen.GetShareByItemParams{
		UserID: userID,
		ID:     itemID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return Share{}, ErrNotFound
	}
	if err != nil {
		return Share{}, err
	}
	return toShare(sh), nil
}

// ByToken resolves a share from its public token, regardless of user.
func (s *ShareStore) ByToken(token string) (Share, error) {
	sh, err := s.q.GetShareByToken(context.Background(), token)
	if errors.Is(err, sql.ErrNoRows) {
		return Share{}, ErrNotFound
	}
	if err != nil {
		return Share{}, err
	}
	return toShare(sh), nil
}

// Delete removes the share for one of the user's items (a no-op if unshared).
func (s *ShareStore) Delete(userID, itemID int64) error {
	return s.q.DeleteShareByItem(context.Background(), sqlcgen.DeleteShareByItemParams{
		ItemID: itemID,
		UserID: userID,
	})
}