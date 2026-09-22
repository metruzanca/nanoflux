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
