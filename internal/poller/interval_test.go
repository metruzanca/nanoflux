package poller

import (
	"testing"
	"time"

	"github.com/metruzanca/nanoflux/internal/db"
)

func dbT(t time.Time) string { return db.FormatTime(t) }

func TestAdaptiveInterval(t *testing.T) {
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	hour := 3600

	cases := []struct {
		name  string
		times []string
		want  int
		ok    bool
	}{
		{"even 2h spacing", []string{dbT(base), dbT(base.Add(-2 * time.Hour)), dbT(base.Add(-4 * time.Hour))}, 2 * hour, true},
		{"unsorted input", []string{dbT(base.Add(-4 * time.Hour)), dbT(base), dbT(base.Add(-2 * time.Hour))}, 2 * hour, true},
		{"mixed gaps average", []string{dbT(base), dbT(base.Add(-1 * time.Hour)), dbT(base.Add(-3 * time.Hour))}, 5400, true},
		{"single timestamp", []string{dbT(base)}, 0, false},
		{"empty", nil, 0, false},
		{"all same time", []string{dbT(base), dbT(base), dbT(base)}, 0, false},
		{"unparseable dropped", []string{dbT(base), "not-a-time", dbT(base.Add(-2 * time.Hour))}, 2 * hour, true},
		{"clamps below floor", []string{dbT(base), dbT(base.Add(-30 * time.Second))}, adaptiveFloor, true},
		{"clamps above ceil", []string{dbT(base), dbT(base.Add(-40 * 24 * time.Hour))}, adaptiveCeil, true},
	}
	for _, c := range cases {
		got, ok := adaptiveInterval(c.times, adaptiveFloor, adaptiveCeil)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: adaptiveInterval = %d, %v; want %d, %v", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestIsStale(t *testing.T) {
	now := db.Now()
	d := func(days int) string {
		return db.FormatTime(time.Now().Add(-time.Duration(days) * 24 * time.Hour))
	}
	cases := []struct {
		last string
		days int
		want bool
	}{
		{"", 0, false},        // no items yet
		{d(1), 7, false},      // active
		{d(6), 7, false},      // just under the 7-day threshold
		{d(7), 7, true},       // at the threshold
		{d(30), 30, true},     // abandoned threshold too
		{d(0), 0, false},      // published today
		{"garbage", 0, false}, // unparseable
	}
	for _, c := range cases {
		if got := isStale(c.last, now, staleAfter); got != c.want {
			t.Errorf("isStale(%q) = %v, want %v", c.last, got, c.want)
		}
	}
}

func TestPreferredFetchTime(t *testing.T) {
	// A daily post at 15:00 UTC (with a little jitter) concentrates at 15:00.
	at := func(h, m int) string {
		return dbT(time.Date(2026, 9, 1, h, m, 0, 0, time.UTC))
	}
	clustered := []string{at(15, 0), at(15, 4), at(14, 58), at(15, 2), at(15, 1), at(15, 6)}
	sec, ok := preferredFetchTime(clustered)
	if !ok {
		t.Fatalf("clustered daily posts should yield a preferred time")
	}
	if want := 15*3600 + 3*60; sec < want-120 || sec > want+120 {
		t.Errorf("preferredFetchTime = %d, want ~%d (15:03)", sec, want)
	}

	// Posts spread around the clock have no preferred time.
	spread := []string{at(0, 0), at(4, 0), at(8, 0), at(12, 0), at(16, 0), at(20, 0)}
	if _, ok := preferredFetchTime(spread); ok {
		t.Errorf("spread posts should not yield a preferred time")
	}

	// Too few samples to trust.
	if _, ok := preferredFetchTime(clustered[:3]); ok {
		t.Errorf("fewer than %d samples should not yield a preferred time", scheduleMinSamples)
	}
}

func TestNextFetchTime(t *testing.T) {
	// 15:00 preferred, buffer 5m, jitter 0 for feed id 0 -> 15:05 today.
	after := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	got := nextFetchTime(after, 15*3600, 0)
	want := time.Date(2026, 9, 1, 15, 5, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("nextFetchTime = %s, want %s", got, want)
	}

	// If the preferred time already passed today, roll to tomorrow.
	after = time.Date(2026, 9, 1, 16, 0, 0, 0, time.UTC)
	got = nextFetchTime(after, 15*3600, 0)
	want = time.Date(2026, 9, 2, 15, 5, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("nextFetchTime (next day) = %s, want %s", got, want)
	}

	// Jitter is deterministic per feed and stays strictly after `after`.
	a := nextFetchTime(after, 15*3600, 7)
	b := nextFetchTime(after, 15*3600, 7)
	if !a.Equal(b) {
		t.Errorf("jitter should be deterministic for a feed id")
	}
	if !a.After(after) {
		t.Errorf("target %s must be after %s", a, after)
	}
}
