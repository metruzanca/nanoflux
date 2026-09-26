package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/store/sqlcgen"
)

// systemAuthorName and systemFeedTitle label the hidden per-user entities that
// hold saved pages. They are never shown as feeds or authors; their items
// surface only in lists, favorites and search.
const (
	systemAuthorName = "saved"
	systemFeedTitle  = "saved pages"
	systemFeedURL    = "nanoflux:saved"
)

// EnsureSystemAuthor returns the user's hidden system author, creating it on
// first use. Idempotent.
func (s *Store) EnsureSystemAuthor(userID int64) (Author, error) {
	a, err := s.q.GetSystemAuthor(context.Background(), userID)
	if err == nil {
		return toAuthor(a), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Author{}, err
	}
	a, err = s.q.CreateSystemAuthor(context.Background(), sqlcgen.CreateSystemAuthorParams{
		UserID:      userID,
		Name:        systemAuthorName,
		Description: ns("system author for saved pages"),
	})
	if err != nil {
		return Author{}, fmt.Errorf("create system author: %w", err)
	}
	return toAuthor(a), nil
}

// EnsureSystemFeed returns the user's hidden feed that holds saved pages,
// creating it (and its author) on first use. Idempotent.
func (s *Store) EnsureSystemFeed(userID int64) (Feed, error) {
	f, err := s.q.GetSystemFeed(context.Background(), userID)
	if err == nil {
		return toFeed(f), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Feed{}, err
	}
	a, err := s.EnsureSystemAuthor(userID)
	if err != nil {
		return Feed{}, err
	}
	f, err = s.q.CreateSystemFeed(context.Background(), sqlcgen.CreateSystemFeedParams{
		UserID:      userID,
		AuthorID:    a.ID,
		Title:       systemFeedTitle,
		FeedUrl:     systemFeedURL,
		Description: ns("holds pages saved from the browser extension"),
	})
	if err != nil {
		return Feed{}, fmt.Errorf("create system feed: %w", err)
	}
	return toFeed(f), nil
}

// SavePage stores an arbitrary web page as an item under the user's hidden
// system feed and adds it to listID (0 skips list membership). Re-saving the
// same page URL (guid) is idempotent: the existing item is reused and returned.
// It reports whether the item was newly inserted.
func (s *Store) SavePage(userID, listID int64, guid, title, link, summary, imageURL string) (itemID int64, inserted bool, err error) {
	feed, err := s.EnsureSystemFeed(userID)
	if err != nil {
		return 0, false, err
	}
	// Re-saving an already-saved page is a no-op (beyond list membership): the
	// stored item keeps its content rather than being blanked by a re-save whose
	// metadata fetch failed.
	itemID, err = s.Items.ByFeedGUID(feed.ID, guid)
	if err != nil {
		return 0, false, err
	}
	if itemID == 0 {
		if _, err := s.Items.Upsert(feed.ID, Item{
			GUID:      guid,
			Title:     title,
			Link:      link,
			Summary:   summary,
			ImageURL:  imageURL,
			FetchedAt: db.Now(),
		}); err != nil {
			return 0, false, err
		}
		itemID, err = s.Items.ByFeedGUID(feed.ID, guid)
		if err != nil {
			return 0, false, err
		}
		if itemID == 0 {
			return 0, false, ErrNotFound
		}
		inserted = true
	}
	if listID != 0 {
		if err := s.Lists.AddItem(userID, listID, itemID); err != nil {
			return itemID, inserted, err
		}
	}
	return itemID, inserted, nil
}
