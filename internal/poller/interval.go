package poller

import (
	"sort"
	"time"

	"github.com/metruzanca/nanoflux/internal/db"
)

// Adaptive polling bounds.
const (
	// adaptiveFloor is the smallest computed poll interval (15 min), matching
	// the poller ticker's default granularity.
	adaptiveFloor = 900
	// adaptiveCeil is the largest computed poll interval (1 day).
	adaptiveCeil = 86400
)

// Stale-feed thresholds (how old a feed's newest item may be before it is
// considered quiet).
const (
	staleAfter     = 7 * 24 * time.Hour
	abandonedAfter = 30 * 24 * time.Hour
)

// adaptiveInterval derives a feed's poll interval (seconds) from the spacing
// between its recent post timestamps (newest first, as stored). It sorts the
// timestamps, drops duplicates, and averages the positive gaps between
// consecutive posts, clamped to [floor, ceil]. ok is false when there are not
// enough valid timestamps to compute a cadence.
func adaptiveInterval(times []string, floor, ceil int) (sec int, ok bool) {
	var parsed []time.Time
	for _, s := range times {
		t, err := db.ParseTime(s)
		if err != nil {
			continue
		}
		parsed = append(parsed, t)
	}
	if len(parsed) < 2 {
		return 0, false
	}
	sort.Slice(parsed, func(i, j int) bool { return parsed[i].Before(parsed[j]) })

	var sum time.Duration
	var n int
	prev := parsed[0]
	for _, t := range parsed[1:] {
		d := t.Sub(prev)
		prev = t
		if d <= 0 {
			continue // duplicate or out-of-order published time
		}
		sum += d
		n++
	}
	if n == 0 {
		return 0, false
	}
	avg := time.Duration(sum / time.Duration(n)).Seconds()
	sec = int(avg + 0.5) // round
	if sec < floor {
		sec = floor
	}
	if sec > ceil {
		sec = ceil
	}
	return sec, true
}

// isStale reports whether a feed's newest item (lastItemAt) is older than
// threshold from now. An empty lastItemAt (no items yet) is never stale.
func isStale(lastItemAt, now string, threshold time.Duration) bool {
	if lastItemAt == "" {
		return false
	}
	t, err := db.ParseTime(lastItemAt)
	if err != nil {
		return false
	}
	n, err := db.ParseTime(now)
	if err != nil {
		return false
	}
	return n.Sub(t) >= threshold
}
