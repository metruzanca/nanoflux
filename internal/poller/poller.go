// Package poller runs the background feed fetch loop and single-feed
// refreshes.
package poller

import (
	"context"
	"errors"
	"github.com/charmbracelet/log"
	"net/http"
	"sync"
	"time"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/feedparse"
	"github.com/metruzanca/nanoflux/internal/store"
)

const fetchTimeout = 30 * time.Second

// Poller fetches due feeds on an interval. Per-feed schedules come from each
// feed's poll_interval_sec; the ticker just wakes the loop.
type Poller struct {
	store    *store.Store
	client   *http.Client
	interval time.Duration
	workers  int
}

func New(st *store.Store, interval time.Duration, workers int) *Poller {
	if workers <= 0 {
		workers = 4
	}
	return &Poller{
		store:    st,
		client:   &http.Client{Timeout: fetchTimeout},
		interval: interval,
		workers:  workers,
	}
}

// Run polls due feeds immediately, then every interval until ctx is done.
func (p *Poller) Run(ctx context.Context) {
	log.Info("poller starting", "interval", p.interval)
	p.PollDue(ctx)
	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Info("poller stopped")
			return
		case <-t.C:
			p.PollDue(ctx)
		}
	}
}

// PollDue fetches every enabled feed that is due and returns the number of
// new items stored.
func (p *Poller) PollDue(ctx context.Context) (int, error) {
	feeds, err := p.store.Feeds.ListDue(db.Now())
	if err != nil {
		return 0, err
	}
	if len(feeds) == 0 {
		return 0, nil
	}

	var (
		wg    sync.WaitGroup
		sem   = make(chan struct{}, p.workers)
		mu    sync.Mutex
		total int
	)
	for _, f := range feeds {
		wg.Add(1)
		sem <- struct{}{}
		go func(f store.Feed) {
			defer wg.Done()
			defer func() { <-sem }()
			n, err := p.PollOne(ctx, f)
			mu.Lock()
			total += n
			mu.Unlock()
			if err != nil {
				log.Error("poll feed", "feed_id", f.ID, "url", f.FeedURL, "err", err)
			}
		}(f)
	}
	wg.Wait()
	if total > 0 {
		log.Info("poller finished", "new_items", total, "feeds", len(feeds))
	}
	return total, nil
}

// PollOne fetches a single feed, stores new items, and records poll metadata.
// It returns the number of new items.
func (p *Poller) PollOne(ctx context.Context, f store.Feed) (int, error) {
	res, err := feedparse.Fetch(ctx, f.FeedURL, p.client, f.ETag, f.LastModified)
	if errors.Is(err, feedparse.ErrNotModified) {
		p.store.Feeds.SetPollMeta(f.ID, f.ETag, f.LastModified, db.Now())
		return 0, nil
	}
	fetched := db.Now()
	if err != nil {
		// Record the attempt so a broken feed isn't retried every tick.
		p.store.Feeds.SetPollMeta(f.ID, "", "", fetched)
		return 0, err
	}

	newItems := 0
	for _, it := range res.Items {
		inserted, err := p.store.Items.Upsert(f.ID, store.Item{
			GUID:        it.GUID,
			Title:       it.Title,
			Link:        it.Link,
			Summary:     it.Summary,
			ImageURL:    it.ImageURL,
			PublishedAt: it.PublishedAt,
			FetchedAt:   fetched,
		})
		if err != nil {
			return newItems, err
		}
		if inserted {
			newItems++
		}
	}
	if err := p.store.Feeds.SetPollMeta(f.ID, res.ETag, res.LastModified, fetched); err != nil {
		return newItems, err
	}
	return newItems, nil
}
