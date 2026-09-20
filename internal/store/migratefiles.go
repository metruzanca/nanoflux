package store

import (
	"context"
	"database/sql"
	"strconv"

	"github.com/metruzanca/nanoflux/internal/filestore"
)

// MigrateLegacyFiles moves blobs that were stored directly in the database
// (pre-object-storage) out to the filestore, recording the object keys. It
// runs once at startup; rows with no blob are skipped.
func (s *Store) MigrateLegacyFiles(ctx context.Context, fs filestore.Store) error {
	if err := s.migrateLegacyAvatars(ctx, fs); err != nil {
		return err
	}
	return s.migrateLegacyIcons(ctx, fs)
}

func (s *Store) migrateLegacyAvatars(ctx context.Context, fs filestore.Store) error {
	rows, err := s.db.Query(
		`SELECT id, avatar_content_type, avatar_data FROM users WHERE avatar_data IS NOT NULL`,
	)
	if err != nil {
		return err
	}
	defer rows.Close()

	var blobs []struct {
		id  int64
		ct  sql.NullString
		dat []byte
	}
	for rows.Next() {
		var b struct {
			id  int64
			ct  sql.NullString
			dat []byte
		}
		if err := rows.Scan(&b.id, &b.ct, &b.dat); err != nil {
			return err
		}
		blobs = append(blobs, b)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, b := range blobs {
		key := "avatars/" + strconv.FormatInt(b.id, 10)
		if err := fs.Put(ctx, key, b.ct.String, b.dat); err != nil {
			return err
		}
		if _, err := s.db.Exec(
			`UPDATE users SET avatar_key = ?, avatar_data = NULL, avatar_content_type = NULL WHERE id = ?`,
			key, b.id,
		); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) migrateLegacyIcons(ctx context.Context, fs filestore.Store) error {
	rows, err := s.db.Query(
		`SELECT id, user_id, domain, content_type, icon_data FROM source_icons WHERE icon_data IS NOT NULL`,
	)
	if err != nil {
		return err
	}
	defer rows.Close()

	type blob struct {
		id     int64
		userID int64
		domain string
		ct     sql.NullString
		dat    []byte
	}
	var blobs []blob
	for rows.Next() {
		var b blob
		if err := rows.Scan(&b.id, &b.userID, &b.domain, &b.ct, &b.dat); err != nil {
			return err
		}
		blobs = append(blobs, b)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, b := range blobs {
		key := "icons/" + strconv.FormatInt(b.userID, 10) + "/" + b.domain
		if err := fs.Put(ctx, key, b.ct.String, b.dat); err != nil {
			return err
		}
		if _, err := s.db.Exec(
			`UPDATE source_icons SET icon_key = ?, icon_data = NULL, content_type = NULL WHERE id = ?`,
			key, b.id,
		); err != nil {
			return err
		}
	}
	return nil
}
