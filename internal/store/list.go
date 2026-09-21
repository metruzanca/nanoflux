package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/metruzanca/nanoflux/internal/store/sqlcgen"
)

// List is a user-defined collection of items. Favorites is the special list
// (items.favorite) and is not stored here; every row is a user-created list.
type List struct {
	ID         int64
	UserID     int64
	Name       string
	ShareToken string // "" when the list is not shared
	CreatedAt  string
}

// ListWithCount adds the number of items in a list, for index pages.
type ListWithCount struct {
	List
	ItemCount int
}

type ListStore struct{ q *sqlcgen.Queries }

func toList(id, userID int64, name string, shareToken sql.NullString, createdAt string) List {
	return List{
		ID:         id,
		UserID:     userID,
		Name:       name,
		ShareToken: shareToken.String,
		CreatedAt:  createdAt,
	}
}

func (s *ListStore) Create(userID int64, name string) (List, error) {
	l, err := s.q.CreateList(context.Background(), sqlcgen.CreateListParams{
		UserID: userID,
		Name:   name,
	})
	if err != nil {
		return List{}, fmt.Errorf("create list: %w", err)
	}
	return toList(l.ID, l.UserID, l.Name, l.ShareToken, l.CreatedAt), nil
}

func (s *ListStore) ByID(userID, id int64) (List, error) {
	l, err := s.q.GetList(context.Background(), sqlcgen.GetListParams{ID: id, UserID: userID})
	if errors.Is(err, sql.ErrNoRows) {
		return List{}, ErrNotFound
	}
	if err != nil {
		return List{}, err
	}
	return toList(l.ID, l.UserID, l.Name, l.ShareToken, l.CreatedAt), nil
}

// ByToken resolves a shared list by its public token, regardless of user.
func (s *ListStore) ByToken(token string) (List, error) {
	l, err := s.q.GetListByToken(context.Background(), ns(token))
	if errors.Is(err, sql.ErrNoRows) {
		return List{}, ErrNotFound
	}
	if err != nil {
		return List{}, err
	}
	return toList(l.ID, l.UserID, l.Name, l.ShareToken, l.CreatedAt), nil
}

// List returns a user's lists with their item counts.
func (s *ListStore) List(userID int64) ([]ListWithCount, error) {
	rows, err := s.q.ListLists(context.Background(), userID)
	if err != nil {
		return nil, err
	}
	out := make([]ListWithCount, 0, len(rows))
	for _, r := range rows {
		out = append(out, ListWithCount{
			List:      toList(r.ID, r.UserID, r.Name, r.ShareToken, r.CreatedAt),
			ItemCount: int(r.ItemCount),
		})
	}
	return out, nil
}

func (s *ListStore) Delete(userID, id int64) error {
	res, err := s.q.DeleteList(context.Background(), sqlcgen.DeleteListParams{ID: id, UserID: userID})
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetShareToken stores (or, with an empty token, clears) a list's public share
// token.
func (s *ListStore) SetShareToken(userID, id int64, token string) error {
	if token == "" {
		return s.q.ClearListShareToken(context.Background(), sqlcgen.ClearListShareTokenParams{ID: id, UserID: userID})
	}
	return s.q.SetListShareToken(context.Background(), sqlcgen.SetListShareTokenParams{
		Token:  ns(token),
		ID:     id,
		UserID: userID,
	})
}

// AddItem associates an item with a list, verifying both belong to the user.
func (s *ListStore) AddItem(userID, listID, itemID int64) error {
	n, err := s.q.VerifyListItem(context.Background(), sqlcgen.VerifyListItemParams{
		ItemID: itemID,
		ListID: listID,
		UserID: userID,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	_, err = s.q.AddItemToList(context.Background(), sqlcgen.AddItemToListParams{
		ListID: listID,
		ItemID: itemID,
	})
	return err
}

// RemoveItem drops an item from a list, verifying both belong to the user.
func (s *ListStore) RemoveItem(userID, listID, itemID int64) error {
	res, err := s.q.RemoveItemFromList(context.Background(), sqlcgen.RemoveItemFromListParams{
		ListID: listID,
		ItemID: itemID,
		UserID: userID,
	})
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ItemListIDs returns the ids of a user's lists that contain itemID.
func (s *ListStore) ItemListIDs(userID, itemID int64) ([]int64, error) {
	return s.q.ListIDsForItem(context.Background(), sqlcgen.ListIDsForItemParams{
		ItemID: itemID,
		UserID: userID,
	})
}

// ItemList returns one page of a list's items, newest-added first. Pass the
// last returned id as before to page further.
func (s *ListStore) ItemList(userID, listID, before int64, limit int) ([]ItemWithFeed, bool, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.q.ListItemsInList(context.Background(), sqlcgen.ListItemsInListParams{
		ListID:       listID,
		UserID:       userID,
		BeforeItemID: before,
		Limit:        int64(limit) + 1,
	})
	if err != nil {
		return nil, false, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	out := make([]ItemWithFeed, 0, len(rows))
	for _, r := range rows {
		out = append(out, toItemWithFeed(r.ID, r.FeedID, r.Guid, r.Title, r.Link, r.Summary,
			r.ImageUrl, r.PublishedAt, r.FetchedAt, r.Read, r.Favorite, r.ReadAt,
			r.FeedTitle, r.FeedUrl, r.AuthorID, r.AuthorName))
	}
	return out, hasMore, nil
}

// ItemListPublic returns one page of a shared list's items, for the public
// (unauthenticated) list page.
func (s *ListStore) ItemListPublic(listID, before int64, limit int) ([]ItemWithFeed, bool, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.q.ListItemsInListPublic(context.Background(), sqlcgen.ListItemsInListPublicParams{
		ListID:       listID,
		BeforeItemID: before,
		Limit:        int64(limit) + 1,
	})
	if err != nil {
		return nil, false, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	out := make([]ItemWithFeed, 0, len(rows))
	for _, r := range rows {
		out = append(out, toItemWithFeed(r.ID, r.FeedID, r.Guid, r.Title, r.Link, r.Summary,
			r.ImageUrl, r.PublishedAt, r.FetchedAt, r.Read, r.Favorite, r.ReadAt,
			r.FeedTitle, r.FeedUrl, r.AuthorID, r.AuthorName))
	}
	return out, hasMore, nil
}

// SetShare posts a new random share token for a list, or returns the existing
// one. Used by the share control so sharing is idempotent.
func (s *ListStore) SetShare(userID, id int64) (string, error) {
	l, err := s.ByID(userID, id)
	if err != nil {
		return "", err
	}
	if l.ShareToken != "" {
		return l.ShareToken, nil
	}
	token, err := newToken()
	if err != nil {
		return "", err
	}
	if err := s.SetShareToken(userID, id, token); err != nil {
		return "", err
	}
	return token, nil
}
