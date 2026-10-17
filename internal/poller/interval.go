package poller

import (
	"math"
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

// Time-of-day scheduling: when a feed's recent posts cluster at a particular
// hour, its next poll is planned for just after that hour (plus a per-feed
// jitter so many feeds sharing a posting hour don't converge).
const (
	// scheduleBuffer is how long after the author's typical posting time a
	// scheduled fetch waits, so a newly published item is already in the feed.
	scheduleBuffer = 5 * time.Minute
	// scheduleJitter spreads feeds that share a posting hour: each feed's fetch
	// is offset by (feed id mod scheduleJitter), so a host's feeds don't all land
	// on the same second.
	scheduleJitter = 10 * time.Minute
	// scheduleMinSamples is the fewest recent posts needed to trust a time-of-day
	// signal.
	scheduleMinSamples = 6
	// scheduleMinStrength is the smallest circular resultant (0..1) at which the
	// posts are considered concentrated at one time of day. A daily poster at a
	// fixed hour is near 1; posts spread across the day are near 0.
	scheduleMinStrength = 0.5
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

// preferredFetchTime derives the time of day (UTC seconds past midnight) at
// which a feed's recent posts cluster, using the circular mean of their times.
// The resultant length measures concentration; ok is false when there are too
// few posts or they are spread across the day, in which case no time-of-day
// schedule should be applied. Working in UTC is enough: it captures the author's
// absolute posting rhythm without needing the user's timezone.
func preferredFetchTime(times []string) (secOfDay int, ok bool) {
	const daySecs = 86400
	var sumCos, sumSin float64
	n := 0
	for _, s := range times {
		t, err := db.ParseTime(s)
		if err != nil {
			continue
		}
		u := t.UTC()
		sec := u.Hour()*3600 + u.Minute()*60 + u.Second()
		ang := 2 * math.Pi * float64(sec) / daySecs
		sumCos += math.Cos(ang)
		sumSin += math.Sin(ang)
		n++
	}
	if n < scheduleMinSamples {
		return 0, false
	}
	if strength := math.Hypot(sumCos, sumSin) / float64(n); strength < scheduleMinStrength {
		return 0, false
	}
	ang := math.Atan2(sumSin, sumCos)
	if ang < 0 {
		ang += 2 * math.Pi
	}
	return int(ang/(2*math.Pi)*daySecs+0.5) % daySecs, true
}

// nextFetchTime returns the next occurrence of the feed's preferred time of day,
// plus the buffer and a per-feed jitter, strictly after `after`. The jitter
// keeps feeds that share a posting hour from all landing on the same second.
func nextFetchTime(after time.Time, secOfDay int, feedID int64) time.Time {
	jitter := int(feedID % int64(scheduleJitter/time.Second))
	offset := secOfDay + int(scheduleBuffer/time.Second) + jitter
	a := after.UTC()
	base := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, time.UTC).
		Add(time.Duration(offset) * time.Second)
	for !base.After(a) {
		base = base.Add(24 * time.Hour)
	}
	return base
}
