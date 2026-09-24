// Package maintenance runs low-frequency background housekeeping that is not
// tied to feed fetching. Today that is the auto-read sweep: per user, unread
// items older than the user's chosen window are marked read.
package maintenance

import (
	"context"
	"time"

	"github.com/charmbracelet/log"

	"github.com/metruzanca/nanoflux/internal/store"
)

// DefaultInterval is how often Run sweeps. The setting is day-granular, so a
// daily pass is enough; a save also sweeps the user immediately.
const DefaultInterval = 24 * time.Hour

// Sweeper marks stale unread items read on a fixed interval.
type Sweeper struct {
	store    *store.Store
	interval time.Duration
}

// New builds a Sweeper. A non-positive interval falls back to DefaultInterval.
func New(st *store.Store, interval time.Duration) *Sweeper {
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Sweeper{store: st, interval: interval}
}

// Run sweeps once, then every interval until ctx is done.
func (s *Sweeper) Run(ctx context.Context) {
	log.Info("auto-read sweeper starting", "interval", s.interval)
	s.Sweep()

	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Info("auto-read sweeper stopped")
			return
		case <-t.C:
			s.Sweep()
		}
	}
}

// Sweep applies every opted-in user's auto-read window. A single user's
// failure is logged and does not stop the rest.
func (s *Sweeper) Sweep() {
	users, err := s.store.Users.List()
	if err != nil {
		log.Error("auto-read sweep: list users", "err", err)
		return
	}
	for _, u := range users {
		if u.AutoReadAfterDays <= 0 {
			continue
		}
		n, err := s.store.Items.MarkOlderThanRead(u.ID, u.AutoReadAfterDays)
		if err != nil {
			log.Error("auto-read sweep", "user_id", u.ID, "err", err)
			continue
		}
		if n > 0 {
			log.Info("auto-read sweep marked items read", "user_id", u.ID, "count", n, "days", u.AutoReadAfterDays)
		}
	}
}
