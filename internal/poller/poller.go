// Package poller runs the background feed fetch loop and single-feed
// refreshes.
package poller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/log"

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
		p.store.Feeds.SetPollMeta(f.ID, f.ETag, f.LastModified, db.Now(), "")
		return 0, nil
	}
	fetched := db.Now()
	if err != nil {
		// Record the attempt and the failure so the owner can see the feed is
		// broken; a broken feed isn't retried every tick.
		p.store.Feeds.SetPollMeta(f.ID, "", "", fetched, truncateError(err.Error()))
		return 0, err
	}

	rules, _ := p.store.Filters.ListByFeed(f.UserID, f.ID)
	newItems, err := p.ingest(f, res, rules, fetched)
	if err != nil {
		return newItems, err
	}
	if err := p.store.Feeds.SetPollMeta(f.ID, res.ETag, res.LastModified, fetched, ""); err != nil {
		return newItems, err
	}
	// Record whether the feed advertises a next page so the feed page can offer
	// "load older items". Detection is cheap (parsing only) — no extra fetches.
	// This only happens on the first poll (a newly added feed): routine polls
	// leave the cursor untouched so they can't clobber a user's in-progress
	// "load older items" walk by resetting it to page two.
	if f.LastPolledAt == "" {
		if err := p.store.Feeds.SetNextPageURL(f.ID, res.NextPageURL); err != nil {
			return newItems, err
		}
	}
	return newItems, nil
}

// maxBackfillPages caps how many pages a single "load older items" click
// fetches, so a feed with an unbounded history can't stall the request. The
// button stays available for further clicks.
const maxBackfillPages = 5

// PollOlder fetches the feed's next page(s) — its stored next-page URL — and
// stores the items, advancing the cursor. It returns the number of new items
// and whether the feed's history is now exhausted (the "load older items"
// button can disappear). On error the cursor is left untouched so the user can
// retry.
func (p *Poller) PollOlder(ctx context.Context, f store.Feed) (newItems int, exhausted bool, err error) {
	url := f.NextPageURL
	if url == "" {
		return 0, true, nil
	}
	rules, _ := p.store.Filters.ListByFeed(f.UserID, f.ID)
	seen := map[string]bool{url: true}
	for page := 0; page < maxBackfillPages; page++ {
		res, err := feedparse.Fetch(ctx, url, p.client, "", "")
		if err != nil {
			return newItems, false, fmt.Errorf("fetch %s: %w", url, err)
		}
		inserted, err := p.ingest(f, res, rules, db.Now())
		if err != nil {
			return newItems, false, err
		}
		newItems += inserted
		next := res.NextPageURL
		if next == "" {
			// No more pages advertised.
			p.store.Feeds.SetNextPageURL(f.ID, "")
			return newItems, true, nil
		}
		if inserted == 0 || seen[next] {
			// A page with zero new items means every older page is already
			// stored (feeds are newest-first), and a repeated URL is a loop.
			// Either way the history is exhausted.
			p.store.Feeds.SetNextPageURL(f.ID, "")
			return newItems, true, nil
		}
		seen[next] = true
		url = next
	}
	// Hit the per-click cap: keep the cursor for another click.
	p.store.Feeds.SetNextPageURL(f.ID, url)
	return newItems, false, nil
}

// ingest stores a fetched page's items for a feed, applying the feed's filter
// rules and copying enclosures for newly inserted items. It returns how many
// items were new.
func (p *Poller) ingest(f store.Feed, res feedparse.Result, rules []store.Filter, fetched string) (int, error) {
	newItems := 0
	for _, it := range res.Items {
		action := ""
		for _, rule := range rules {
			if m, err := matchFilter(rule, it); err == nil && m {
				action = rule.Action
				break
			}
		}
		if action == "hide" {
			continue
		}
		item := store.Item{
			GUID:        it.GUID,
			Title:       it.Title,
			Link:        it.Link,
			Summary:     it.Summary,
			ImageURL:    it.ImageURL,
			PublishedAt: it.PublishedAt,
			FetchedAt:   fetched,
		}
		if action == "mark_read" {
			item.Read = true
			item.ReadAt = db.Now()
		}
		inserted, err := p.store.Items.Upsert(f.ID, item)
		if err != nil {
			return newItems, err
		}
		if inserted {
			newItems++
			if len(it.Enclosures) > 0 {
				if err := p.storeEnclosures(f.ID, it); err != nil {
					log.Error("store enclosures", "feed_id", f.ID, "guid", it.GUID, "err", err)
				}
			}
		}
	}
	return newItems, nil
}

// truncateError caps the stored failure text so a runaway error message can't
// bloat the row.
func truncateError(msg string) string {
	const maxErrLen = 200
	if len(msg) > maxErrLen {
		return msg[:maxErrLen] + "…"
	}
	return msg
}

// matchFilter reports whether a rule's pattern matches an item's selected
// field. Summary matching uses the visible text, not the raw HTML.
func matchFilter(rule store.Filter, it feedparse.Item) (bool, error) {
	var text string
	switch rule.Field {
	case "title":
		text = it.Title
	case "link":
		text = it.Link
	case "summary":
		text = feedparse.PlainText(it.Summary)
	default:
		return false, nil
	}
	if rule.IsRegex {
		re, err := regexp.Compile(rule.Pattern)
		if err != nil {
			return false, err
		}
		return re.MatchString(text), nil
	}
	return strings.Contains(strings.ToLower(text), strings.ToLower(rule.Pattern)), nil
}

// storeEnclosures copies a newly inserted item's media attachments into the
// item_enclosures table.
func (p *Poller) storeEnclosures(feedID int64, it feedparse.Item) error {
	itemID, err := p.store.Items.ByFeedGUID(feedID, it.GUID)
	if err != nil {
		return err
	}
	if itemID == 0 {
		return nil
	}
	encs := make([]store.Enclosure, 0, len(it.Enclosures))
	for _, e := range it.Enclosures {
		encs = append(encs, store.Enclosure{URL: e.URL, MIMEType: e.MIMEType, Size: e.Length})
	}
	return p.store.Items.ReplaceEnclosures(itemID, encs)
}
