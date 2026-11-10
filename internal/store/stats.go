package store

import (
	"context"
	"sort"
	"time"

	"github.com/metruzanca/nanoflux/internal/db"
)

// The stats page is read-only aggregation over the user's own library. It is
// intentionally not cached: a personal reader's dataset is small, and the page
// may be a little slow. Every item query reads from visible_items so the
// always-on author allow/block rules apply, exactly like the lists and counts.
//
// Time-of-day bucketing (per-day series, weekday/hour heatmap, streaks) happens
// in Go rather than SQLite because SQLite's date functions do not honour the
// user's IANA timezone across DST boundaries. Item-scoped queries filter on
// items.user_id and exclude the hidden per-user system feed (saved pages).

// StatsPeriod bounds the stats page. A zero Start means all time.
type StatsPeriod struct {
	Start time.Time
}

func (p StatsPeriod) since() string {
	if p.Start.IsZero() {
		return ""
	}
	return db.FormatTime(p.Start)
}

// StatsOverview is the top-line summary for the selected period.
type StatsOverview struct {
	Total      int   // all items in scope
	Read       int   // read items
	Unread     int   // unread items
	Favorites  int   // favorited items
	Bookmarks  int   // bookmarked items
	WatchSec   int64 // total saved playback position in the period
	WatchItems int   // media items with a saved position
}

// DayCount is one calendar day's read count in the user's timezone. Day is
// "YYYY-MM-DD".
type DayCount struct {
	Day   string
	Count int
}

// Streak is the current and longest run of consecutive days with at least one
// read, in the user's timezone.
type Streak struct {
	Current int
	Longest int
}

// SourceStat aggregates one author (blog) within the period. Popularity is
// measured by reads and favorites among the items that appeared in the period,
// and the read rate normalises for posting volume.
type SourceStat struct {
	AuthorID  int64
	Name      string
	Feeds     int
	Total     int
	Read      int
	Favorites int
}

// TagStat aggregates one category/tag within the period.
type TagStat struct {
	Tag   string
	Total int
	Read  int
}

// Bucket is a labelled count (unread aging, read-lag distribution).
type Bucket struct {
	Label string
	Count int
}

// ReadLag describes how quickly the user reads items after they arrive:
// read_at minus the item time, in the user's scope.
type ReadLag struct {
	Avg     time.Duration
	Median  time.Duration
	Buckets []Bucket
}

// MediaStat aggregates saved playback positions for one author.
type MediaStat struct {
	AuthorID int64
	Name     string
	Watched  int64 // sum of position_sec
	Duration int64 // sum of duration_sec (for completion)
	Samples  int
}

const statsItemSource = `FROM visible_items i JOIN feeds f ON f.id = i.feed_id
	WHERE i.user_id = ? AND f.is_system = 0`

// statsItemTime is the item's position in time, used for the period filter on
// source/tag aggregates (the period means "posts that appeared in this window").
const statsItemTime = "COALESCE(i.published_at, i.fetched_at)"

// StatsOverview aggregates the period's totals plus saved watch time.
func (s *ItemStore) StatsOverview(userID int64, p StatsPeriod) (StatsOverview, error) {
	var o StatsOverview
	q := `SELECT COUNT(*),
		COALESCE(SUM(i.read), 0),
		COALESCE(SUM(1 - i.read), 0),
		COALESCE(SUM(i.favorite), 0),
		COALESCE(SUM(i.bookmark), 0)
		` + statsItemSource
	args := []any{userID}
	if since := p.since(); since != "" {
		q += " AND " + statsItemTime + " >= ?"
		args = append(args, since)
	}
	if err := s.db.QueryRowContext(context.Background(), q, args...).Scan(
		&o.Total, &o.Read, &o.Unread, &o.Favorites, &o.Bookmarks,
	); err != nil {
		return StatsOverview{}, err
	}

	// Watch time is its own signal: item_progress rows only exist for media the
	// user actually played, and a row survives deleting the item that carried it.
	wq := `SELECT COALESCE(SUM(position_sec), 0), COUNT(*) FROM item_progress WHERE user_id = ?`
	wargs := []any{userID}
	if since := p.since(); since != "" {
		wq += " AND updated_at >= ?"
		wargs = append(wargs, since)
	}
	if err := s.db.QueryRowContext(context.Background(), wq, wargs...).Scan(
		&o.WatchSec, &o.WatchItems,
	); err != nil {
		return StatsOverview{}, err
	}
	return o, nil
}

// StatsReadSeries returns one count per calendar day that has a read, in the
// user's timezone, oldest first. Callers fill empty days for the chart. Rows
// marked read before read_at was tracked (NULL) are excluded: they have no
// timestamp to place on a timeline.
func (s *ItemStore) StatsReadSeries(userID int64, p StatsPeriod, loc *time.Location) ([]DayCount, error) {
	q := `SELECT i.read_at ` + statsItemSource + `
		AND i.read = 1 AND i.read_at IS NOT NULL`
	args := []any{userID}
	if since := p.since(); since != "" {
		q += " AND i.read_at >= ?"
		args = append(args, since)
	}
	q += " ORDER BY i.read_at"

	rows, err := s.db.QueryContext(context.Background(), q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := map[string]int{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if t, err := db.ParseTime(raw); err == nil {
			counts[t.In(loc).Format("2006-01-02")]++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]DayCount, 0, len(counts))
	for day, n := range counts {
		out = append(out, DayCount{Day: day, Count: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day < out[j].Day })
	return out, nil
}

// StatsReadHeatmap returns reads by weekday (0 = Sunday) and hour of day in the
// user's timezone.
func (s *ItemStore) StatsReadHeatmap(userID int64, p StatsPeriod, loc *time.Location) ([7][24]int, error) {
	var grid [7][24]int
	q := `SELECT i.read_at ` + statsItemSource + `
		AND i.read = 1 AND i.read_at IS NOT NULL`
	args := []any{userID}
	if since := p.since(); since != "" {
		q += " AND i.read_at >= ?"
		args = append(args, since)
	}
	rows, err := s.db.QueryContext(context.Background(), q, args...)
	if err != nil {
		return grid, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return grid, err
		}
		t, err := db.ParseTime(raw)
		if err != nil {
			continue
		}
		t = t.In(loc)
		grid[int(t.Weekday())][t.Hour()]++
	}
	return grid, rows.Err()
}

// StatsStreaks computes the current and longest run of consecutive days with at
// least one read, across all time (a period filter would cut a streak in half).
func (s *ItemStore) StatsStreaks(userID int64, loc *time.Location) (Streak, error) {
	q := `SELECT i.read_at ` + statsItemSource + `
		AND i.read = 1 AND i.read_at IS NOT NULL`
	rows, err := s.db.QueryContext(context.Background(), q, userID)
	if err != nil {
		return Streak{}, err
	}
	defer rows.Close()

	days := map[string]bool{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return Streak{}, err
		}
		if t, err := db.ParseTime(raw); err == nil {
			days[t.In(loc).Format("2006-01-02")] = true
		}
	}
	if err := rows.Err(); err != nil {
		return Streak{}, err
	}
	if len(days) == 0 {
		return Streak{}, nil
	}

	ordered := make([]time.Time, 0, len(days))
	for day := range days {
		if t, err := time.ParseInLocation("2006-01-02", day, loc); err == nil {
			ordered = append(ordered, t)
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Before(ordered[j]) })

	longest, run := 1, 1
	for i := 1; i < len(ordered); i++ {
		// Compare calendar days, not elapsed duration: a DST transition makes a
		// day 23 or 25 hours long, which must still count as consecutive.
		if ordered[i-1].AddDate(0, 0, 1).Equal(ordered[i]) {
			run++
		} else {
			run = 1
		}
		if run > longest {
			longest = run
		}
	}

	// The current streak counts back from today (or yesterday, so a day that is
	// still in progress does not break it).
	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	cursor := today
	if !days[cursor.Format("2006-01-02")] {
		cursor = cursor.AddDate(0, 0, -1)
	}
	current := 0
	for days[cursor.Format("2006-01-02")] {
		current++
		cursor = cursor.AddDate(0, 0, -1)
	}
	return Streak{Current: current, Longest: longest}, nil
}

// StatsTopSources aggregates reads and favorites per author in the period.
// Counts are by owner feed, so a cross-feed post is attributed once, to the
// feed that published it.
func (s *ItemStore) StatsTopSources(userID int64, p StatsPeriod, limit int) ([]SourceStat, error) {
	if limit <= 0 {
		limit = 15
	}
	q := `SELECT f.author_id, a.name, COUNT(DISTINCT f.id),
		COUNT(*), COALESCE(SUM(i.read), 0), COALESCE(SUM(i.favorite), 0)
		FROM visible_items i
		JOIN feeds f ON f.id = i.feed_id
		JOIN authors a ON a.id = f.author_id
		WHERE i.user_id = ? AND f.is_system = 0`
	args := []any{userID}
	if since := p.since(); since != "" {
		q += " AND " + statsItemTime + " >= ?"
		args = append(args, since)
	}
	q += ` GROUP BY f.author_id ORDER BY COUNT(*) DESC, SUM(i.read) DESC, a.name LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(context.Background(), q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SourceStat
	for rows.Next() {
		var r SourceStat
		if err := rows.Scan(&r.AuthorID, &r.Name, &r.Feeds, &r.Total, &r.Read, &r.Favorites); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// StatsTags aggregates read rate per category/tag in the period.
func (s *ItemStore) StatsTags(userID int64, p StatsPeriod, limit int) ([]TagStat, error) {
	if limit <= 0 {
		limit = 20
	}
	q := `SELECT ic.category, COUNT(DISTINCT ic.item_id), COALESCE(SUM(i.read), 0)
		FROM visible_items i
		JOIN feeds f ON f.id = i.feed_id
		JOIN item_categories ic ON ic.item_id = i.id
		WHERE i.user_id = ? AND f.is_system = 0`
	args := []any{userID}
	if since := p.since(); since != "" {
		q += " AND " + statsItemTime + " >= ?"
		args = append(args, since)
	}
	q += ` GROUP BY ic.category ORDER BY COUNT(DISTINCT ic.item_id) DESC, ic.category LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(context.Background(), q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TagStat
	for rows.Next() {
		var r TagStat
		if err := rows.Scan(&r.Tag, &r.Total, &r.Read); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// StatsUnreadAging buckets the current unread backlog by age. It is deliberately
// not period-filtered: the backlog is a present-tense figure.
func (s *ItemStore) StatsUnreadAging(userID int64) ([]Bucket, error) {
	q := `SELECT CASE
			WHEN julianday('now') - julianday(` + statsItemTime + `) < 1 THEN 'under 1 day'
			WHEN julianday('now') - julianday(` + statsItemTime + `) < 7 THEN '1-7 days'
			WHEN julianday('now') - julianday(` + statsItemTime + `) < 30 THEN '7-30 days'
			ELSE 'over 30 days'
		END AS bucket, COUNT(*)
		` + statsItemSource + ` AND i.read = 0
		GROUP BY bucket`
	rows, err := s.db.QueryContext(context.Background(), q, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	order := []string{"under 1 day", "1-7 days", "7-30 days", "over 30 days"}
	counts := map[string]int{}
	for rows.Next() {
		var label string
		var n int
		if err := rows.Scan(&label, &n); err != nil {
			return nil, err
		}
		counts[label] = n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Bucket, 0, len(order))
	for _, label := range order {
		out = append(out, Bucket{Label: label, Count: counts[label]})
	}
	return out, nil
}

// StatsReadLag measures read_at minus the item's published (or fetched) time in
// the period. It scans the period's read rows and computes the average, median
// and distribution in Go, which keeps the statistics exact and DST-free.
func (s *ItemStore) StatsReadLag(userID int64, p StatsPeriod) (ReadLag, error) {
	q := `SELECT i.read_at, ` + statsItemTime + `
		` + statsItemSource + `
		AND i.read = 1 AND i.read_at IS NOT NULL`
	args := []any{userID}
	if since := p.since(); since != "" {
		q += " AND i.read_at >= ?"
		args = append(args, since)
	}
	rows, err := s.db.QueryContext(context.Background(), q, args...)
	if err != nil {
		return ReadLag{}, err
	}
	defer rows.Close()

	var lags []time.Duration
	for rows.Next() {
		var readRaw, itemRaw string
		if err := rows.Scan(&readRaw, &itemRaw); err != nil {
			return ReadLag{}, err
		}
		readAt, err := db.ParseTime(readRaw)
		if err != nil {
			continue
		}
		itemAt, err := db.ParseTime(itemRaw)
		if err != nil {
			continue
		}
		lag := readAt.Sub(itemAt)
		if lag < 0 {
			lag = 0
		}
		lags = append(lags, lag)
	}
	if err := rows.Err(); err != nil {
		return ReadLag{}, err
	}
	if len(lags) == 0 {
		return ReadLag{Buckets: []Bucket{
			{Label: "under 1 hour"}, {Label: "1-24 hours"}, {Label: "1-7 days"},
			{Label: "7-30 days"}, {Label: "over 30 days"},
		}}, nil
	}

	sort.Slice(lags, func(i, j int) bool { return lags[i] < lags[j] })
	var total time.Duration
	for _, d := range lags {
		total += d
	}
	median := lags[len(lags)/2]
	if len(lags)%2 == 0 {
		median = (lags[len(lags)/2-1] + lags[len(lags)/2]) / 2
	}

	buckets := []Bucket{
		{Label: "under 1 hour"},
		{Label: "1-24 hours"},
		{Label: "1-7 days"},
		{Label: "7-30 days"},
		{Label: "over 30 days"},
	}
	for _, d := range lags {
		switch {
		case d < time.Hour:
			buckets[0].Count++
		case d < 24*time.Hour:
			buckets[1].Count++
		case d < 7*24*time.Hour:
			buckets[2].Count++
		case d < 30*24*time.Hour:
			buckets[3].Count++
		default:
			buckets[4].Count++
		}
	}
	return ReadLag{Avg: total / time.Duration(len(lags)), Median: median, Buckets: buckets}, nil
}

// StatsMedia aggregates saved playback positions per author in the period. It
// joins visible_items so a blocked author's media is not surfaced, mirroring
// every other stats aggregate.
func (s *ItemStore) StatsMedia(userID int64, p StatsPeriod, limit int) ([]MediaStat, error) {
	if limit <= 0 {
		limit = 15
	}
	q := `SELECT COALESCE(a.id, 0), COALESCE(a.name, 'unknown'),
		COALESCE(SUM(p.position_sec), 0),
		COALESCE(SUM(CASE WHEN p.duration_sec > 0 THEN p.duration_sec ELSE 0 END), 0),
		COUNT(*)
		FROM item_progress p
		JOIN visible_items i ON i.id = p.item_id AND i.user_id = p.user_id
		JOIN feeds f ON f.id = i.feed_id AND f.is_system = 0
		LEFT JOIN authors a ON a.id = f.author_id
		WHERE p.user_id = ?`
	args := []any{userID}
	if since := p.since(); since != "" {
		q += " AND p.updated_at >= ?"
		args = append(args, since)
	}
	q += ` GROUP BY COALESCE(a.id, 0) ORDER BY SUM(p.position_sec) DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(context.Background(), q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MediaStat
	for rows.Next() {
		var r MediaStat
		if err := rows.Scan(&r.AuthorID, &r.Name, &r.Watched, &r.Duration, &r.Samples); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
