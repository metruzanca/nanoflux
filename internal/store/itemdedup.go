package store

import (
	"context"
	"database/sql"
	"sort"
)

// DedupGroup is one set of items in a single feed that share the same content
// identity but were stored more than once — typically because a plugin changed
// its GUID scheme (e.g. a post URL becoming "scheme:<id>") for the same entry.
type DedupGroup struct {
	FeedID    int64
	FeedTitle string
	Link      string
	Survivor  int64   // the row kept: newest fetched_at, then highest id
	Losers    []int64 // rows merged into the survivor and deleted
	// Populated only by the cross-feed scan: the user owning the merged rows
	// and the shared cross-feed identity they collapsed on.
	UserID   int64
	CrossKey string
}

// DedupReport summarizes a deduplication scan/run.
type DedupReport struct {
	Groups      []DedupGroup
	ItemsMerged int // total loser rows removed (or that would be)
}

// dedupScanSQL finds items to merge: same feed, same link, same published time.
// That triple is the conservative content identity — a plugin GUID change
// leaves all three untouched, while two genuinely distinct posts virtually
// never share all three. Items with no link are never grouped (there is no
// safe key).
const dedupScanSQL = `
SELECT i.id, i.feed_id, f.title, i.link,
       COALESCE(i.published_at, '') AS pub, i.fetched_at
FROM items i
JOIN feeds f ON f.id = i.feed_id
WHERE i.link <> ''
  AND (i.feed_id, i.link, COALESCE(i.published_at, '')) IN (
    SELECT feed_id, link, COALESCE(published_at, '')
    FROM items
    WHERE link <> ''
    GROUP BY feed_id, link, COALESCE(published_at, '')
    HAVING COUNT(*) > 1
  )
ORDER BY i.feed_id, i.link, COALESCE(i.published_at, ''), i.fetched_at, i.id
`

// FindDedupGroups scans for duplicate items and returns them without changing
// anything. Within each group the survivor is the row most recently confirmed
// by the feed (newest fetched_at, then highest id) — the one on the plugin's
// current GUID scheme, which future polls will match.
func (s *ItemStore) FindDedupGroups() (DedupReport, error) {
	rows, err := s.db.QueryContext(context.Background(), dedupScanSQL)
	if err != nil {
		return DedupReport{}, err
	}
	defer rows.Close()

	type key struct {
		feedID int64
		link   string
		pub    string
	}
	type entry struct {
		id        int64
		fetchedAt string
	}
	order := []key{}
	groups := map[key][]entry{}
	title := map[int64]string{}

	for rows.Next() {
		var id, feedID int64
		var feedTitle, link, pub, fetchedAt string
		if err := rows.Scan(&id, &feedID, &feedTitle, &link, &pub, &fetchedAt); err != nil {
			return DedupReport{}, err
		}
		title[feedID] = feedTitle
		k := key{feedID, link, pub}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], entry{id, fetchedAt})
	}
	if err := rows.Err(); err != nil {
		return DedupReport{}, err
	}

	var report DedupReport
	for _, k := range order {
		es := groups[k]
		if len(es) < 2 {
			continue
		}
		// Survivor: latest fetched_at, then highest id.
		sort.SliceStable(es, func(i, j int) bool {
			if es[i].fetchedAt != es[j].fetchedAt {
				return es[i].fetchedAt < es[j].fetchedAt
			}
			return es[i].id < es[j].id
		})
		survivor := es[len(es)-1].id
		losers := make([]int64, 0, len(es)-1)
		for _, e := range es[:len(es)-1] {
			losers = append(losers, e.id)
		}
		report.Groups = append(report.Groups, DedupGroup{
			FeedID: k.feedID, FeedTitle: title[k.feedID], Link: k.link,
			Survivor: survivor, Losers: losers,
		})
		report.ItemsMerged += len(losers)
	}
	return report, nil
}

// DeduplicateItems merges every duplicate group found by FindDedupGroups. Each
// merge keeps the survivor's content and GUID, carries over the losers'
// read/favorite state, repoints their enclosures, list memberships and shares,
// and deletes them. It returns what was merged. Pass dryRun to report without
// writing.
func (s *ItemStore) DeduplicateItems(dryRun bool) (DedupReport, error) {
	report, err := s.FindDedupGroups()
	if err != nil || dryRun {
		return report, err
	}
	for _, g := range report.Groups {
		if err := s.mergeDedupGroup(g); err != nil {
			return report, err
		}
	}
	return report, nil
}

// mergeDedupGroup merges one group's losers into its survivor in a transaction.
func (s *ItemStore) mergeDedupGroup(g DedupGroup) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ctx := context.Background()

	for _, loser := range g.Losers {
		// Carry read/favorite/bookmark state onto the survivor (never un-read,
		// un-favorite or un-bookmark something).
		if err := exec(ctx, tx, `
			UPDATE items
			SET read = MAX(read, (SELECT read FROM items WHERE id = ?)),
			    favorite = MAX(favorite, (SELECT favorite FROM items WHERE id = ?)),
			    bookmark = MAX(bookmark, (SELECT bookmark FROM items WHERE id = ?)),
			    read_at = COALESCE(read_at, (SELECT read_at FROM items WHERE id = ?))
			WHERE id = ?`, loser, loser, loser, loser, g.Survivor); err != nil {
			return err
		}
		// Fold the loser's feed memberships into the survivor, then repoint
		// every other reference to the loser.
		if err := exec(ctx, tx, `
			INSERT OR IGNORE INTO item_feeds (item_id, feed_id, created_at)
			SELECT ?, feed_id, created_at FROM item_feeds WHERE item_id = ?`, g.Survivor, loser); err != nil {
			return err
		}
		if err := exec(ctx, tx, `UPDATE item_enclosures SET item_id = ? WHERE item_id = ?`, g.Survivor, loser); err != nil {
			return err
		}
		// list_items is keyed (list_id, item_id): insert-or-ignore then drop.
		if err := exec(ctx, tx, `
			INSERT OR IGNORE INTO list_items (list_id, item_id, created_at)
			SELECT list_id, ?, created_at FROM list_items WHERE item_id = ?`, g.Survivor, loser); err != nil {
			return err
		}
		if err := exec(ctx, tx, `DELETE FROM list_items WHERE item_id = ?`, loser); err != nil {
			return err
		}
		// shared_items.item_id is UNIQUE: keep the survivor's token if it has
		// one, otherwise adopt the loser's.
		if err := exec(ctx, tx, `
			INSERT OR IGNORE INTO shared_items (item_id, token, created_at)
			SELECT ?, token, created_at FROM shared_items WHERE item_id = ?`, g.Survivor, loser); err != nil {
			return err
		}
		if err := exec(ctx, tx, `DELETE FROM shared_items WHERE item_id = ?`, loser); err != nil {
			return err
		}
		if err := exec(ctx, tx, `DELETE FROM items WHERE id = ?`, loser); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func exec(ctx context.Context, tx *sql.Tx, query string, args ...any) error {
	_, err := tx.ExecContext(ctx, query, args...)
	return err
}

// crossKeyBackfillSQL adopts a shared cross-feed identity on every reddit post
// stored before plugins supplied SharedKey. This is a one-time legacy backfill:
// a reddit post's identity is derived from its GUID (t3_<id>), a pure SQL prefix
// test. The reddit plugin now sets SharedKey at ingest, so this only matters for
// rows written by an older build.
const crossKeyBackfillSQL = `
UPDATE items
SET cross_key = 'reddit:' || guid
WHERE cross_key = ''
  AND guid GLOB 't3_*'
  AND length(guid) > 3
  AND substr(guid, 4) NOT GLOB '*[^0-9a-zA-Z]*'`

// crossDuplicatesSQL finds rows that share a per-user cross-feed identity but
// are still stored separately (a subreddit feed and a user feed both holding the
// same reddit post), plus the surviving row's id: the newest fetched_at, then
// the highest id. It ignores the owner feed and returns every member row.
const crossDuplicatesSQL = `
SELECT i.id, i.user_id, i.feed_id, i.cross_key, i.fetched_at
FROM items i
WHERE i.cross_key <> ''
  AND (i.user_id, i.cross_key) IN (
    SELECT user_id, cross_key FROM items
    WHERE cross_key <> ''
    GROUP BY user_id, cross_key
    HAVING COUNT(*) > 1
  )
ORDER BY i.user_id, i.cross_key, i.fetched_at, i.id`

// MergeCrossFeedDuplicates brings already-stored rows onto the cross-feed model:
// it derives cross_key for every reddit item written before plugins supplied
// SharedKey, merges rows that share a per-user cross-feed identity into one row
// (carrying read/favorite state, memberships, enclosures, lists and shares), and
// only then creates the partial unique index that keeps them merged. Idempotent
// and cheap to call on every startup: with nothing to merge it is one UPDATE and
// one scan.
func (s *ItemStore) MergeCrossFeedDuplicates(dryRun bool) (DedupReport, error) {
	ctx := context.Background()
	if _, err := s.db.ExecContext(ctx, crossKeyBackfillSQL); err != nil {
		return DedupReport{}, err
	}

	rows, err := s.db.QueryContext(ctx, crossDuplicatesSQL)
	if err != nil {
		return DedupReport{}, err
	}
	defer rows.Close()

	type key struct {
		userID   int64
		crossKey string
	}
	type entry struct {
		id        int64
		feedID    int64
		fetchedAt string
	}
	order := []key{}
	groups := map[key][]entry{}
	for rows.Next() {
		var id, userID, feedID int64
		var crossKey, fetchedAt string
		if err := rows.Scan(&id, &userID, &feedID, &crossKey, &fetchedAt); err != nil {
			return DedupReport{}, err
		}
		k := key{userID, crossKey}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], entry{id, feedID, fetchedAt})
	}
	if err := rows.Err(); err != nil {
		return DedupReport{}, err
	}

	var report DedupReport
	for _, k := range order {
		es := groups[k]
		if len(es) < 2 {
			continue
		}
		// Survivor: latest fetched_at, then highest id.
		sort.SliceStable(es, func(i, j int) bool {
			if es[i].fetchedAt != es[j].fetchedAt {
				return es[i].fetchedAt < es[j].fetchedAt
			}
			return es[i].id < es[j].id
		})
		survivor := es[len(es)-1].id
		losers := make([]int64, 0, len(es)-1)
		for _, e := range es[:len(es)-1] {
			losers = append(losers, e.id)
		}
		report.Groups = append(report.Groups, DedupGroup{
			FeedID: k.userID, Link: k.crossKey, UserID: k.userID, CrossKey: k.crossKey,
			Survivor: survivor, Losers: losers,
		})
		report.ItemsMerged += len(losers)
	}

	if dryRun {
		return report, nil
	}
	for _, g := range report.Groups {
		if err := s.mergeDedupGroup(g); err != nil {
			return report, err
		}
	}

	if _, err := s.db.ExecContext(ctx,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_items_cross ON items(user_id, cross_key) WHERE cross_key <> ''`); err != nil {
		return report, err
	}
	return report, nil
}
