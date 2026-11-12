package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/metruzanca/nanoflux/internal/store/sqlcgen"
	"github.com/metruzanca/nanoflux/internal/tagnorm"
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

type ListStore struct {
	q         *sqlcgen.Queries
	db        *sql.DB
	policy    URLPolicy
	decorator ItemDecorator
}

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
		Name:   tagnorm.Label(name),
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

// ByName returns the user's list matching name (case-insensitive), or
// ErrNotFound.
func (s *ListStore) ByName(userID int64, name string) (List, error) {
	l, err := s.q.GetListByName(context.Background(), sqlcgen.GetListByNameParams{UserID: userID, Name: tagnorm.Label(name)})
	if errors.Is(err, sql.ErrNoRows) {
		return List{}, ErrNotFound
	}
	if err != nil {
		return List{}, err
	}
	return toList(l.ID, l.UserID, l.Name, l.ShareToken, l.CreatedAt), nil
}

// Ensure returns the user's list named name, creating it when absent. Used to
// materialize a list a user names while saving a page.
func (s *ListStore) Ensure(userID int64, name string) (List, error) {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return List{}, err
	}
	defer tx.Rollback()
	q := s.q.WithTx(tx)
	name = tagnorm.Label(name)
	if l, err := q.GetListByName(ctx, sqlcgen.GetListByNameParams{UserID: userID, Name: name}); err == nil {
		if err := tx.Commit(); err != nil {
			return List{}, err
		}
		return toList(l.ID, l.UserID, l.Name, l.ShareToken, l.CreatedAt), nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return List{}, err
	}
	l, err := q.CreateList(ctx, sqlcgen.CreateListParams{UserID: userID, Name: name})
	if err != nil {
		return List{}, fmt.Errorf("create list: %w", err)
	}
	if err := tx.Commit(); err != nil {
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

// Rename updates a list's name.
func (s *ListStore) Rename(userID, id int64, name string) error {
	res, err := s.q.RenameList(context.Background(), sqlcgen.RenameListParams{Name: tagnorm.Label(name), ID: id, UserID: userID})
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

// ListItemFilter narrows a list's items to a read/unread view and optional
// tag/feed/time predicates. The zero value matches every item in the list.
type ListItemFilter struct {
	ReadOnly   bool
	UnreadOnly bool
	TagGroups  []TagGroup
	FeedGroups []FeedGroup
	TimeGroups []TimeGroup
}

// hasFilters reports whether the filter needs the hand-written membership
// query (the generated list queries have fixed placeholders).
func (f ListItemFilter) hasFilters() bool {
	return len(f.TagGroups) > 0 || len(f.FeedGroups) > 0 || len(f.TimeGroups) > 0
}

// appendListPredicates adds the list member filter's read state and tag/feed/
// time predicates. The query must use i for items. Unlike the generic item
// filters it does not exclude system-feed items: lists may already contain
// saved pages.
func appendListPredicates(b *strings.Builder, args *[]any, f ListItemFilter) {
	if f.UnreadOnly {
		b.WriteString(" AND i.read = 0")
	}
	if f.ReadOnly {
		b.WriteString(" AND i.read = 1")
	}
	appendFilterGroups(b, args, "i.id", f.TagGroups, f.FeedGroups)
	appendTimeGroups(b, args, "COALESCE(i.published_at, i.fetched_at)", f.TimeGroups)
}

// ItemList returns one page of a list's items, newest-added first (oldest-added
// first when ascending). Pass the last returned id as cursor to page further.
func (s *ListStore) ItemList(userID, listID, cursor int64, limit int, ascending bool, f ListItemFilter) ([]ItemWithFeed, bool, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if f.hasFilters() {
		return s.itemListFiltered(userID, listID, cursor, limit, ascending, f)
	}
	if ascending {
		rows, err := s.q.ListItemsInListAsc(context.Background(), sqlcgen.ListItemsInListAscParams{
			ListID:      listID,
			UserID:      userID,
			AfterItemID: cursor,
			Unread:      boolInt(f.UnreadOnly),
			Read:        boolInt(f.ReadOnly),
			Limit:       int64(limit) + 1,
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
			out = append(out, toItemWithFeed(r.ID, r.FeedID, r.Guid, r.Title, r.Link, r.Summary, r.Categories,
				r.ImageUrl, r.ImageCacheKey, r.DurationSec, r.PublishedAt, r.FetchedAt, r.Read, r.Favorite, r.Bookmark, r.ReadAt,
				r.FeedTitle, r.FeedUrl, r.FeedHomeUrl, r.FeedIsSystem, r.AuthorID, r.AuthorName))
		}
		if err := attachSources(s.q, userID, s.policy, s.decorator, out); err != nil {
			return nil, false, err
		}
		return out, hasMore, nil
	}
	rows, err := s.q.ListItemsInList(context.Background(), sqlcgen.ListItemsInListParams{
		ListID:       listID,
		UserID:       userID,
		BeforeItemID: cursor,
		Unread:       boolInt(f.UnreadOnly),
		Read:         boolInt(f.ReadOnly),
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
		out = append(out, toItemWithFeed(r.ID, r.FeedID, r.Guid, r.Title, r.Link, r.Summary, r.Categories,
			r.ImageUrl, r.ImageCacheKey, r.DurationSec, r.PublishedAt, r.FetchedAt, r.Read, r.Favorite, r.Bookmark, r.ReadAt,
			r.FeedTitle, r.FeedUrl, r.FeedHomeUrl, r.FeedIsSystem, r.AuthorID, r.AuthorName))
	}
	if err := attachSources(s.q, userID, s.policy, s.decorator, out); err != nil {
		return nil, false, err
	}
	return out, hasMore, nil
}

// itemListFiltered reads one membership-ordered page with tag/feed/time
// predicates. Ordering and the cursor stay on list_items.created_at, so
// filtering never reorders a list by item time.
func (s *ListStore) itemListFiltered(userID, listID, cursor int64, limit int, ascending bool, f ListItemFilter) ([]ItemWithFeed, bool, error) {
	var b strings.Builder
	b.WriteString(itemListColumns("visible_items"))
	b.WriteString(" JOIN list_items li ON li.item_id = i.id WHERE li.list_id = ? AND i.user_id = ?")
	args := []any{listID, userID}
	if ascending {
		if cursor != 0 {
			b.WriteString(` AND (li.created_at, i.id) > (SELECT li2.created_at, li2.item_id FROM list_items li2 WHERE li2.list_id = ? AND li2.item_id = ?)`)
			args = append(args, listID, cursor)
		}
	} else if cursor != 0 {
		b.WriteString(` AND (li.created_at, i.id) < (SELECT li2.created_at, li2.item_id FROM list_items li2 WHERE li2.list_id = ? AND li2.item_id = ?)`)
		args = append(args, listID, cursor)
	}
	appendListPredicates(&b, &args, f)
	if ascending {
		b.WriteString(" ORDER BY li.created_at ASC, i.id ASC LIMIT ?")
	} else {
		b.WriteString(" ORDER BY li.created_at DESC, i.id DESC LIMIT ?")
	}
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(context.Background(), b.String(), args...)
	if err != nil {
		return nil, false, err
	}
	out, hasMore, err := scanItemList(rows, limit)
	if err != nil {
		return nil, false, err
	}
	if err := attachSources(s.q, userID, s.policy, s.decorator, out); err != nil {
		return nil, false, err
	}
	return out, hasMore, nil
}

// countItems counts a list's items for one read/unread tab with the same
// predicates the list query applies.
func (s *ListStore) countItems(userID, listID int64, f ListItemFilter) (int, error) {
	var b strings.Builder
	b.WriteString(`SELECT COUNT(*) FROM list_items li JOIN visible_items i ON i.id = li.item_id WHERE li.list_id = ? AND i.user_id = ?`)
	args := []any{listID, userID}
	appendListPredicates(&b, &args, f)
	var n int
	if err := s.db.QueryRowContext(context.Background(), b.String(), args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// CountUnread returns the number of unread items in a list.
func (s *ListStore) CountUnread(userID, listID int64) (int, error) {
	return s.CountUnreadFiltered(userID, listID, ListItemFilter{})
}

// CountUnreadFiltered counts unread items matching f's tag/feed/time rows.
func (s *ListStore) CountUnreadFiltered(userID, listID int64, f ListItemFilter) (int, error) {
	f.UnreadOnly, f.ReadOnly = true, false
	return s.countItems(userID, listID, f)
}

// CountRead returns the number of read items in a list.
func (s *ListStore) CountRead(userID, listID int64) (int, error) {
	return s.CountReadFiltered(userID, listID, ListItemFilter{})
}

// CountReadFiltered counts read items matching f's tag/feed/time rows.
func (s *ListStore) CountReadFiltered(userID, listID int64, f ListItemFilter) (int, error) {
	f.ReadOnly, f.UnreadOnly = true, false
	return s.countItems(userID, listID, f)
}

// Categories returns the distinct tags of a list's items, most-used first, for
// its filter modal.
func (s *ListStore) Categories(userID, listID int64) ([]CategoryCount, error) {
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT ic.category, COUNT(DISTINCT ic.item_id)
		FROM item_categories ic
		WHERE ic.item_id IN (
			SELECT i.id FROM list_items li JOIN visible_items i ON i.id = li.item_id
			WHERE li.list_id = ? AND i.user_id = ?
		)
		GROUP BY ic.category
		ORDER BY COUNT(DISTINCT ic.item_id) DESC, ic.category`, listID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CategoryCount
	for rows.Next() {
		var c CategoryCount
		if err := rows.Scan(&c.Category, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
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
		out = append(out, toItemWithFeed(r.ID, r.FeedID, r.Guid, r.Title, r.Link, r.Summary, r.Categories,
			r.ImageUrl, r.ImageCacheKey, r.DurationSec, r.PublishedAt, r.FetchedAt, r.Read, r.Favorite, r.Bookmark, r.ReadAt,
			r.FeedTitle, r.FeedUrl, r.FeedHomeUrl, r.FeedIsSystem, r.AuthorID, r.AuthorName))
	}
	if err := attachSources(s.q, 0, s.policy, s.decorator, out); err != nil {
		return nil, false, err
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
