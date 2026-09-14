package store

import (
	"database/sql"
	"errors"
	"fmt"
)

type Collection struct {
	ID        int64
	UserID    int64
	Name      string
	CreatedAt string
}

type CollectionStore struct{ db *sql.DB }

func (s *CollectionStore) Create(userID int64, name string) (Collection, error) {
	res, err := s.db.Exec(
		`INSERT INTO collections(user_id, name) VALUES(?, ?)`, userID, name,
	)
	if err != nil {
		return Collection{}, fmt.Errorf("create collection: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Collection{}, err
	}
	return s.ByID(userID, id)
}

func (s *CollectionStore) ByID(userID, id int64) (Collection, error) {
	c, err := scanCollection(s.db.QueryRow(
		`SELECT id, user_id, name, created_at FROM collections WHERE id = ? AND user_id = ?`,
		id, userID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return Collection{}, ErrNotFound
	}
	return c, err
}

func (s *CollectionStore) List(userID int64) ([]Collection, error) {
	rows, err := s.db.Query(
		`SELECT id, user_id, name, created_at FROM collections WHERE user_id = ? ORDER BY name`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Collection
	for rows.Next() {
		c, err := scanCollection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *CollectionStore) Delete(userID, id int64) error {
	res, err := s.db.Exec(`DELETE FROM collections WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AddFeed associates a feed with a collection, verifying both belong to the user.
func (s *CollectionStore) AddFeed(userID, collectionID, feedID int64) error {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM collections c
		 JOIN feeds f ON f.user_id = c.user_id
		 WHERE c.id = ? AND f.id = ? AND c.user_id = ?`,
		collectionID, feedID, userID,
	).Scan(&n)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	_, err = s.db.Exec(
		`INSERT INTO collection_feeds(collection_id, feed_id) VALUES(?, ?)
		 ON CONFLICT(collection_id, feed_id) DO NOTHING`,
		collectionID, feedID,
	)
	return err
}

func (s *CollectionStore) RemoveFeed(userID, collectionID, feedID int64) error {
	res, err := s.db.Exec(
		`DELETE FROM collection_feeds
		 WHERE collection_id = ? AND feed_id = ?
		   AND collection_id IN (SELECT id FROM collections WHERE user_id = ?)
		   AND feed_id IN (SELECT id FROM feeds WHERE user_id = ?)`,
		collectionID, feedID, userID, userID,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *CollectionStore) Feeds(userID, collectionID int64) ([]Feed, error) {
	rows, err := s.db.Query(
		`SELECT `+feedCols+`
		 FROM feeds f JOIN collection_feeds cf ON cf.feed_id = f.id
		 WHERE cf.collection_id = ? AND f.user_id = ? ORDER BY f.title`,
		collectionID, userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Feed
	for rows.Next() {
		f, err := scanFeed(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func scanCollection(row scanner) (Collection, error) {
	var c Collection
	err := row.Scan(&c.ID, &c.UserID, &c.Name, &c.CreatedAt)
	return c, err
}
