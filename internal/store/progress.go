package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// MaxProgressPerUser caps a user's saved playback positions. Every write trims
// the oldest rows beyond this, so a long-lived account cannot grow the table
// without bound.
const MaxProgressPerUser = 200

// ErrProgressNotFound is returned when a user has no saved position for a key.
var ErrProgressNotFound = errors.New("progress not found")

// Progress is one user's saved playback position for one media identity.
// ProgressKey is either "item:<id>" (media with no plugin identity) or
// "plugin:<name>:<key>" (a plugin-supplied identity shared across items).
type Progress struct {
	UserID      int64
	ProgressKey string
	// ItemID is the item that last wrote the position. It is informational:
	// progress survives deleting that item, so it is not a foreign key.
	ItemID      int64
	PositionSec int
	DurationSec int
	UpdatedAt   string
}

// ProgressStore persists per-user playback positions.
type ProgressStore struct{ db *sql.DB }

// Get returns a user's saved position for progressKey.
func (s *ProgressStore) Get(userID int64, progressKey string) (Progress, error) {
	if progressKey == "" {
		return Progress{}, ErrProgressNotFound
	}
	var p Progress
	err := s.db.QueryRowContext(context.Background(), `
		SELECT user_id, progress_key, item_id, position_sec, duration_sec, updated_at
		FROM item_progress
		WHERE user_id = ? AND progress_key = ?`, userID, progressKey,
	).Scan(&p.UserID, &p.ProgressKey, &p.ItemID, &p.PositionSec, &p.DurationSec, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Progress{}, ErrProgressNotFound
	}
	if err != nil {
		return Progress{}, fmt.Errorf("get item progress: %w", err)
	}
	return p, nil
}

// Set stores a user's playback position and trims the oldest rows beyond the
// per-user cap in the same transaction.
func (s *ProgressStore) Set(userID, itemID int64, progressKey string, positionSec, durationSec int) error {
	if progressKey == "" {
		return nil
	}
	if positionSec < 0 {
		positionSec = 0
	}
	if durationSec < 0 {
		durationSec = 0
	}
	if durationSec > 0 && positionSec > durationSec {
		positionSec = durationSec
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin item progress: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO item_progress (user_id, progress_key, item_id, position_sec, duration_sec, updated_at)
		VALUES (?, ?, ?, ?, ?, datetime('now'))
		ON CONFLICT(user_id, progress_key) DO UPDATE SET
			item_id      = excluded.item_id,
			position_sec = excluded.position_sec,
			duration_sec = excluded.duration_sec,
			updated_at   = datetime('now')`,
		userID, progressKey, itemID, positionSec, durationSec); err != nil {
		return fmt.Errorf("set item progress: %w", err)
	}
	if err := trimProgress(ctx, tx, userID, MaxProgressPerUser); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit item progress: %w", err)
	}
	return nil
}

// Delete removes a user's saved position for progressKey (a finished or
// explicitly reset video). Deleting an absent row is not an error.
func (s *ProgressStore) Delete(userID int64, progressKey string) error {
	if progressKey == "" {
		return nil
	}
	if _, err := s.db.ExecContext(context.Background(),
		`DELETE FROM item_progress WHERE user_id = ? AND progress_key = ?`,
		userID, progressKey); err != nil {
		return fmt.Errorf("delete item progress: %w", err)
	}
	return nil
}

// Trim removes all but the newest keep positions for a user.
func (s *ProgressStore) Trim(userID int64, keep int) error {
	if keep <= 0 {
		keep = MaxProgressPerUser
	}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("begin progress trim: %w", err)
	}
	defer tx.Rollback()
	if err := trimProgress(context.Background(), tx, userID, keep); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit progress trim: %w", err)
	}
	return nil
}

func trimProgress(ctx context.Context, tx *sql.Tx, userID int64, keep int) error {
	if keep <= 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM item_progress
		WHERE user_id = ? AND progress_key NOT IN (
			SELECT progress_key FROM item_progress
			WHERE user_id = ?
			ORDER BY updated_at DESC, progress_key DESC
			LIMIT ?
		)`, userID, userID, keep); err != nil {
		return fmt.Errorf("trim item progress: %w", err)
	}
	return nil
}

// DeleteOlderThan removes every position not updated since before, returning
// the number of rows removed. A zero cutoff disables the sweep.
func (s *ProgressStore) DeleteOlderThan(before time.Time) (int64, error) {
	if before.IsZero() {
		return 0, nil
	}
	res, err := s.db.ExecContext(context.Background(), `
		DELETE FROM item_progress
		WHERE updated_at < ?`, before.UTC().Format("2006-01-02 15:04:05"))
	if err != nil {
		return 0, fmt.Errorf("sweep item progress: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("sweep item progress rows: %w", err)
	}
	return n, nil
}
