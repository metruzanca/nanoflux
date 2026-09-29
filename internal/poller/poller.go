// Package poller runs the background feed fetch loop and single-feed
// refreshes.
package poller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/charmbracelet/log"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/feedparse"
	"github.com/metruzanca/nanoflux/internal/filtermatch"
	"github.com/metruzanca/nanoflux/internal/imagecache"
	"github.com/metruzanca/nanoflux/internal/store"
)

const (
	fetchTimeout = 30 * time.Second

	// rateLimitFloor is the minimum spacing the poller enforces between two
	// requests to a host that has rate-limited it. Hosts report their own window
	// (Retry-After / x-ratelimit-reset); this floor covers a missing or tiny
	// hint. Reddit's anonymous .rss limit is ~1 request per minute.
	rateLimitFloor = 60 * time.Second

	// minWake is the smallest next-wake delay, so a just-cleared backoff can't
	// make the loop spin.
	minWake = 30 * time.Second

	// defaultHostSpacing is the spacing the poller enforces between two
	// requests to the same registrable host even when the host has never
	// rate-limited it. It stops a domain with many due feeds from bursting all
	// of them in one cycle, which is what tends to trigger the first 429. 60s
	// matches reddit's anonymous per-IP window, the tightest host we know of. A
	// learned rate-limit window overrides it when larger. Configured via
	// NF_POLL_HOST_SPACING; 0 disables the default spacing.
	defaultHostSpacing = 60 * time.Second
)

// hostCooler is the rate-limit cooldown the poller consults, satisfied by
// *plugin.Cooldown. Kept as a local interface so the poller does not import the
// plugin package; when set, a limit learned by either the poller or a plugin
// paces both.
type hostCooler interface {
	Cooling(rawURL string) bool
	Cool(rawURL string, t time.Time)
	Until(rawURL string) (time.Time, bool)
}

// itemEnricher resolves plugin-enriched bodies for freshly stored items, grouped
// by the plugin that matches each item's link. It is satisfied by
// *plugin.Dispatcher; kept as a local interface so the poller does not import
// the plugin package. Enrichment is best-effort and never fails a poll.
type itemEnricher interface {
	Enrich(ctx context.Context, items []store.Item) (map[int64]string, error)
}

// itemImageCacher stores a feed's images host-side, for feeds whose plugin
// serves short-lived signed image URLs. Satisfied by *imagecache.Cacher; kept as
// a local interface so the poller can be tested without object storage.
type itemImageCacher interface {
	Forced(feedURL string) bool
	Folder(feedURL string) string
	Cache(ctx context.Context, req imagecache.Request) imagecache.Result
}

// Poller fetches due feeds on an interval. Per-feed schedules come from each
// feed's poll_interval_sec; the ticker just wakes the loop.
type Poller struct {
	store       *store.Store
	client      *http.Client
	interval    time.Duration
	workers     int
	hostSpacing time.Duration
	cool        hostCooler
	enricher    itemEnricher
	imgCache    itemImageCacher

	// hostWindow records, per registrable host, the minimum spacing the host
	// has asked for after a rate limit (learned from Retry-After /
	// x-ratelimit-reset). hostNextHit is the earliest time the host may be hit
	// again. Both are in-memory (reset on restart): a host that never
	// rate-limits is never paced. This replaces a coarse "cool the host for the
	// whole cycle", which starved the host's other feeds.
	hostMu      sync.Mutex
	hostWindow  map[string]time.Duration
	hostNextHit map[string]time.Time
}

func New(st *store.Store, interval time.Duration, workers int) *Poller {
	if workers <= 0 {
		workers = 4
	}
	return &Poller{
		store:       st,
		client:      &http.Client{Timeout: fetchTimeout},
		interval:    interval,
		workers:     workers,
		hostSpacing: defaultHostSpacing,
		hostWindow:  map[string]time.Duration{},
		hostNextHit: map[string]time.Time{},
	}
}

// SetHostSpacing sets the default minimum spacing between two requests to the
// same host, applied even to hosts that have never rate-limited the poller. A
// learned rate-limit window still overrides it when larger. Zero disables it.
func (p *Poller) SetHostSpacing(d time.Duration) { p.hostSpacing = d }

// SetHostCooler shares a rate-limit cooldown with the poller, so a limit seen
// by a plugin (or by the poller) paces both. A nil cooler is ignored.
func (p *Poller) SetHostCooler(c hostCooler) {
	if c != nil {
		p.cool = c
	}
}

// SetItemEnricher attaches the plugin-backed body enricher (satisfied by
// *plugin.Dispatcher), so newly stored items are offered to matching plugins.
// A nil enricher disables enrichment.
func (p *Poller) SetItemEnricher(e itemEnricher) { p.enricher = e }

// SetImageCache attaches the host-side image cache, so a feed with caching
// enabled (by its plugin or the user) has its item images downloaded and stored
// at poll time. A nil cacher disables caching.
func (p *Poller) SetImageCache(c itemImageCacher) { p.imgCache = c }

// Client returns the HTTP client the poller uses, so the plugin host can share
// the same transport and timeout policy.
func (p *Poller) Client() *http.Client { return p.client }

// Run polls due feeds, then wakes at the earliest of the base interval or the
// next moment a paced host becomes hittable, whichever is sooner (floored at
// minWake). The dynamic wake is what lets a rate-limited host's feeds rotate:
// after a 429 the poller returns when the host's window clears, for the next
// feed in line, rather than waiting out the whole base interval.
func (p *Poller) Run(ctx context.Context) {
	log.Info("poller starting", "interval", p.interval)
	for {
		_, wake, err := p.pollDue(ctx)
		if err != nil {
			log.Error("poll due", "err", err)
		}
		delay := wake.Sub(time.Now())
		// A paced host can pull the next wake in sooner than the base interval;
		// record why, so logs show the rotation rather than a bare sleep.
		if !wake.IsZero() && delay > 0 && delay <= p.interval {
			log.Debug("next wake from paced host", "delay", delay.Round(time.Second), "at", wake.Round(time.Second))
		}
		if delay <= 0 || delay > p.interval {
			delay = p.interval
		}
		if delay < minWake {
			delay = minWake
		}
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			log.Info("poller stopped")
			return
		case <-t.C:
		}
	}
}

// PollDue fetches every enabled feed that is due and returns the number of new
// items stored. It is the exported entry point for tests and manual cycles.
func (p *Poller) PollDue(ctx context.Context) (int, error) {
	n, _, err := p.pollDue(ctx)
	return n, err
}

// pollDue fetches due feeds and returns how many new items were stored plus the
// earliest time a skipped (paced) host may next be hit (zero when none).
//
// Feeds are grouped by registrable host and each host's feeds are processed
// oldest-poll-first (a fair rotation), while different hosts run in parallel up
// to the worker limit. A host that has rate-limited the poller is spaced by its
// window; its remaining feeds stay due and are picked up when the window
// clears.
func (p *Poller) pollDue(ctx context.Context) (int, time.Time, error) {
	feeds, err := p.store.Feeds.ListDue(db.Now())
	if err != nil {
		return 0, time.Time{}, err
	}
	if len(feeds) == 0 {
		return 0, time.Time{}, nil
	}

	groups := groupByHost(feeds)

	var (
		wg   sync.WaitGroup
		sem  = make(chan struct{}, p.workers)
		mu   sync.Mutex
		sum  cycleSummary
		wake time.Time // earliest paced-host next-hit across groups
	)
	for _, group := range groups {
		wg.Add(1)
		sem <- struct{}{}
		go func(group []store.Feed) {
			defer wg.Done()
			defer func() { <-sem }()
			res := p.pollGroup(ctx, group)
			mu.Lock()
			sum.merge(res)
			if !res.wake.IsZero() && (wake.IsZero() || res.wake.Before(wake)) {
				wake = res.wake
			}
			mu.Unlock()
		}(group)
	}
	wg.Wait()
	// One line per cycle, regardless of whether new items arrived, so logs alone
	// show how many feeds ran, how many were paced, and what the next wake is.
	log.Info("poll cycle",
		"feeds", len(feeds),
		"hosts", len(groups),
		"fetched", sum.fetched,
		"new_items", sum.newItems,
		"rate_limited", sum.rateLimited,
		"paced", sum.paced,
		"next_wake", wakeString(wake),
	)
	return sum.newItems, wake, nil
}

// cycleSummary aggregates one poll cycle across host groups for the cycle log.
type cycleSummary struct {
	fetched     int // feeds attempted (a rate-limited feed still made a request)
	newItems    int // new items stored
	rateLimited int // feeds that returned a rate limit
	paced       int // feeds skipped because their host was paced
}

func (s *cycleSummary) merge(o cycleResult) {
	s.fetched += o.fetched
	s.newItems += o.newItems
	s.rateLimited += o.rateLimited
	s.paced += o.paced
}

// cycleResult is what one host group contributed to the cycle summary.
type cycleResult struct {
	fetched     int
	newItems    int
	rateLimited int
	paced       int
	wake        time.Time
}

// pollGroup processes one host's due feeds, oldest-polled first, pacing the host
// when it has a learned rate-limit window. It returns this group's contribution
// to the cycle summary and the earliest time this host may next be hit (zero
// when it was not paced).
func (p *Poller) pollGroup(ctx context.Context, group []store.Feed) cycleResult {
	// Fair rotation: least-recently-polled first, so every feed takes a turn.
	sort.SliceStable(group, func(i, j int) bool {
		return group[i].LastPolledAt < group[j].LastPolledAt
	})

	host := store.RegistrableDomain(group[0].FeedURL)
	var res cycleResult
	for i, f := range group {
		if ctx.Err() != nil {
			return res
		}
		// A host cooling from a rate limit — learned by the poller or by a
		// plugin — is not hit; its feeds stay due until the window clears.
		if p.cool != nil && p.cool.Cooling(f.FeedURL) {
			if until, ok := p.cool.Until(f.FeedURL); ok {
				remaining := len(group) - i
				res.paced += remaining
				res.wake = until
				log.Debug("host cooling, skipping",
					"host", host, "until", until.Round(time.Second),
					"remaining_sec", int(time.Until(until).Round(time.Second).Seconds()),
					"skipped", remaining,
				)
				return res
			}
		}
		if host != "" && !p.hostReady(host, time.Now()) {
			// The host is inside its pacing window: leave the remaining feeds
			// due and wake when it clears.
			until := p.hostNextHitTime(host)
			remaining := len(group) - i
			res.paced += remaining
			res.wake = until
			log.Debug("host paced, skipping",
				"host", host, "until", until.Round(time.Second),
				"remaining_sec", int(time.Until(until).Round(time.Second).Seconds()),
				"skipped", remaining,
			)
			return res
		}
		got, err := p.PollOne(ctx, f)
		res.fetched++
		res.newItems += got
		if err != nil {
			// A rate limit is already logged distinctly by PollOne; only log
			// other failures here, and count limits for the cycle summary.
			var rl *feedparse.RateLimitError
			if errors.As(err, &rl) {
				res.rateLimited++
			} else {
				log.Error("poll feed", "feed_id", f.ID, "url", f.FeedURL, "err", err)
			}
		}
		// Space the host's next request (learned rate-limit window wins over
		// the default spacing). Only stop early when another feed is waiting:
		// the group's last feed must not sit out the window it just set, so a
		// single-feed host is never delayed.
		if host != "" && i < len(group)-1 {
			if w := p.effectiveSpacing(host); w > 0 {
				next := time.Now().Add(w)
				p.setHostNextHit(host, next)
				res.paced += len(group) - i - 1
				res.wake = next
				log.Debug("host spaced",
					"host", host, "spacing", w.Round(time.Second),
					"until", next.Round(time.Second),
					"skipped", len(group)-i-1,
				)
				return res
			}
		}
	}
	return res
}

// effectiveSpacing returns the spacing to enforce before a host's next request:
// its learned rate-limit window when larger, otherwise the default hostSpacing.
func (p *Poller) effectiveSpacing(host string) time.Duration {
	spacing := p.hostSpacing
	if w, ok := p.hostWindowFor(host); ok && w > spacing {
		spacing = w
	}
	return spacing
}

// groupByHost buckets feeds by their registrable domain, preserving the input
// order within and across buckets. A feed whose host cannot be derived gets its
// own singleton bucket (keyed by URL) so it never blocks another feed.
func groupByHost(feeds []store.Feed) [][]store.Feed {
	byHost := map[string][]store.Feed{}
	var order []string
	for _, f := range feeds {
		host := store.RegistrableDomain(f.FeedURL)
		if host == "" {
			host = "\x00" + f.FeedURL // unique bucket for an unparseable host
		}
		if _, ok := byHost[host]; !ok {
			order = append(order, host)
		}
		byHost[host] = append(byHost[host], f)
	}
	out := make([][]store.Feed, 0, len(order))
	for _, h := range order {
		out = append(out, byHost[h])
	}
	return out
}

// hostReady reports whether a host may be hit at t (it has no learned window, or
// its window has cleared).
func (p *Poller) hostReady(host string, t time.Time) bool {
	p.hostMu.Lock()
	defer p.hostMu.Unlock()
	next, ok := p.hostNextHit[host]
	return !ok || !t.Before(next)
}

// hostNextHitTime returns the host's next allowed hit time, or the zero time.
func (p *Poller) hostNextHitTime(host string) time.Time {
	p.hostMu.Lock()
	defer p.hostMu.Unlock()
	return p.hostNextHit[host]
}

// hostWindowFor returns the host's learned spacing and whether it has one.
func (p *Poller) hostWindowFor(host string) (time.Duration, bool) {
	p.hostMu.Lock()
	defer p.hostMu.Unlock()
	w, ok := p.hostWindow[host]
	return w, ok
}

// setHostNextHit records the earliest time a host may next be hit, keeping the
// later of the existing value and t.
func (p *Poller) setHostNextHit(host string, t time.Time) {
	p.hostMu.Lock()
	if existing, ok := p.hostNextHit[host]; !ok || t.After(existing) {
		p.hostNextHit[host] = t
	}
	p.hostMu.Unlock()
}

// learnWindow records how long a host asked to be left alone after a rate limit,
// floored at rateLimitFloor.
func (p *Poller) learnWindow(host string, retryAfter time.Duration) {
	if retryAfter < rateLimitFloor {
		retryAfter = rateLimitFloor
	}
	p.hostMu.Lock()
	if existing, ok := p.hostWindow[host]; !ok || retryAfter > existing {
		p.hostWindow[host] = retryAfter
	}
	p.hostMu.Unlock()
}

// PollOne fetches a single feed, stores new items, and records poll metadata.
// It returns the number of new items.
func (p *Poller) PollOne(ctx context.Context, f store.Feed) (int, error) {
	res, err := feedparse.Fetch(ctx, f.FeedURL, p.client, f.ETag, f.LastModified)
	if errors.Is(err, feedparse.ErrNotModified) {
		p.store.Feeds.SetPollMeta(f.ID, f.ETag, f.LastModified, db.Now(), "")
		p.store.Feeds.SetNextPollAt(f.ID, "")
		return 0, nil
	}
	fetched := db.Now()
	if err != nil {
		// A rate limit is not a broken feed: back off until the host is ready
		// again (persisted per feed) and teach the poller the host's window so
		// its other feeds are spaced rather than skipped for a whole cycle. Any
		// other failure is recorded as the feed's error and retried on its
		// normal interval.
		var rl *feedparse.RateLimitError
		if errors.As(err, &rl) {
			until := time.Now().Add(rl.RetryAfter)
			p.store.Feeds.SetNextPollAt(f.ID, db.FormatTime(until))
			host := store.RegistrableDomain(f.FeedURL)
			if host != "" {
				p.learnWindow(host, rl.RetryAfter)
			}
			// Refuse plugin requests to the same host for the same window.
			if p.cool != nil {
				p.cool.Cool(f.FeedURL, until)
			}
			p.store.Feeds.SetPollMeta(f.ID, "", "", fetched, truncateError(err.Error()))
			// Rate limiting is pacing, not failure: log it distinctly (Warn,
			// not Error) with the action taken, so logs alone show that the
			// host window and shared cooldown were set.
			log.Warn("feed rate limited",
				"feed_id", f.ID,
				"host", host,
				"url", f.FeedURL,
				"status", rl.Status,
				"retry_after", rl.RetryAfter.Round(time.Second),
				"next_poll_at", db.FormatTime(until),
			)
			return 0, err
		}
		p.store.Feeds.SetPollMeta(f.ID, "", "", fetched, truncateError(err.Error()))
		return 0, err
	}

	rules, _ := p.store.Filters.ListByFeed(f.UserID, f.ID)
	newItems, err := p.ingest(ctx, f, res, rules, fetched)
	if err != nil {
		return newItems, err
	}
	if err := p.store.Feeds.SetPollMeta(f.ID, res.ETag, res.LastModified, fetched, ""); err != nil {
		return newItems, err
	}
	// A successful poll clears any rate-limit backoff.
	p.store.Feeds.SetNextPollAt(f.ID, "")
	// Record the feed's newest item time and adjust its poll interval (adaptive
	// cadence, or back a quiet feed off to once a day). Both run on every
	// successful poll so a feed that goes silent is caught even though no new
	// items arrive.
	p.updateCadence(f, fetched, newItems)
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

// updateCadence records the feed's newest item time and, based on it, adjusts
// the feed's poll interval:
//
//   - if the feed's newest item is at least staleAfter old, force the interval
//     to adaptiveCeil (1 day) so a quiet feed isn't polled often — regardless of
//     whether adaptive polling is enabled;
//   - otherwise, if adaptive polling is enabled and the poll brought new items,
//     derive the interval from the average gap between the feed's recent posts.
//
// last_item_at only moves forward; it is separate from the poll time, so "no
// new posts in a while" is distinct from a feed error.
func (p *Poller) updateCadence(f store.Feed, now string, newItems int) {
	times, err := p.store.Items.RecentTimes(f.ID, 20)
	if err != nil || len(times) == 0 {
		return
	}
	// last_item_at only moves forward and is separate from the poll time, so
	// "no new posts in a while" stays distinct from a feed error.
	if newest := times[0]; newest > f.LastItemAt {
		if err := p.store.Feeds.SetLastItemAt(f.ID, newest); err != nil {
			log.Error("set last item at", "feed_id", f.ID, "err", err)
			return
		}
		f.LastItemAt = newest
	}

	var want int
	switch {
	case isStale(f.LastItemAt, now, staleAfter):
		// A quiet feed is polled at most once a day, regardless of whether
		// adaptive polling is enabled.
		want = adaptiveCeil
	case f.PollIntervalAuto && newItems > 0:
		if sec, ok := adaptiveInterval(times, adaptiveFloor, adaptiveCeil); ok {
			want = sec
		}
	}
	if want == 0 || want == f.PollIntervalSec {
		return // nothing to change, or no cadence computable yet
	}
	if err := p.store.Feeds.SetPollInterval(f.ID, want); err != nil {
		log.Error("set poll interval", "feed_id", f.ID, "err", err)
		return
	}
	log.Info("adjusted poll interval", "feed_id", f.ID, "from", f.PollIntervalSec, "to", want)
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
		inserted, err := p.ingest(ctx, f, res, rules, db.Now())
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
// items were new. Newly stored items are then offered to the body enricher
// (best-effort; a failure never fails the poll).
//
// When image caching is on for the feed (its plugin forces it, or the user
// enabled it), each item's primary image and image enclosures are downloaded
// into object storage and their keys stored, so a view renders the cached copy
// instead of a URL that may have expired. Caching is best-effort and never fails
// the poll.
func (p *Poller) ingest(ctx context.Context, f store.Feed, res feedparse.Result, rules []store.Filter, fetched string) (int, error) {
	newItems := 0
	var stored []store.Item
	cacheImages := p.imgCache != nil && (f.CacheImages || p.imgCache.Forced(f.FeedURL))
	for _, it := range res.Items {
		action := ""
		fields := filtermatch.FieldsFromFeedItem(it)
		for _, rule := range rules {
			if m, err := filtermatch.Match(rule, fields); err == nil && m {
				action = rule.Action
				break
			}
		}
		if action == filtermatch.ActionDelete {
			continue
		}
		item := store.Item{
			GUID:        it.GUID,
			Identity:    it.Identity,
			SharedKey:   it.SharedKey,
			Title:       it.Title,
			Link:        it.Link,
			Summary:     it.Summary,
			Categories:  it.Categories,
			ImageURL:    it.ImageURL,
			DurationSec: it.DurationSec,
			PublishedAt: it.PublishedAt,
			FetchedAt:   fetched,
		}
		if action == filtermatch.ActionMarkRead {
			item.Read = true
			item.ReadAt = db.Now()
		}
		inserted, err := p.store.Items.Upsert(f.ID, item)
		if err != nil {
			return newItems, err
		}
		if inserted {
			newItems++
		}
		// Resolve the stored row once: enclosures and the image cache both need
		// its id, and it exists after Upsert whether the row is new or already
		// stored (possibly owned by another feed).
		var itemID int64
		if cacheImages || len(it.Enclosures) > 0 || inserted {
			if id, err := p.store.Items.IngestItemID(f.UserID, f.ID, dedupIdentity(it), store.CrossFeedKey(it.SharedKey)); err == nil {
				itemID = id
			}
		}
		var enclosureKeys []string
		if cacheImages && itemID != 0 {
			enclosureKeys = p.cacheItemImages(ctx, f, it, itemID)
		}
		// Enclosures are stored whenever the item carries them, not only when it
		// is new: a feed polled before its parser learned to expose media (the
		// native Bluesky plugin, say) gains its attachments on the next poll.
		// ReplaceEnclosures is delete-then-insert, so this keeps them current.
		if len(it.Enclosures) > 0 {
			if err := p.storeEnclosures(f, it, itemID, enclosureKeys); err != nil {
				log.Error("store enclosures", "feed_id", f.ID, "guid", it.GUID, "err", err)
			}
		}
		// Enrich only newly inserted items, and only in the owner feed (a
		// cross-feed member already enriched by the other feed is skipped below).
		if inserted && p.enricher != nil && itemID != 0 {
			item.ID = itemID
			item.UserID = f.UserID
			stored = append(stored, item)
		}
	}
	p.enrichStored(f.UserID, stored)
	return newItems, nil
}

// enrichStored offers newly stored items to the body enricher and writes the
// resolved content. Items already carrying content are skipped (a cross-feed
// member enriched through another feed), and a failure is logged, not returned:
// enrichment is a best-effort enhancement, never a poll failure.
func (p *Poller) enrichStored(userID int64, items []store.Item) {
	if p.enricher == nil || len(items) == 0 {
		return
	}
	var pending []store.Item
	for _, it := range items {
		if content, err := p.store.Items.Content(it.ID); err != nil || content != "" {
			continue
		}
		pending = append(pending, it)
	}
	if len(pending) == 0 {
		return
	}
	contents, err := p.enricher.Enrich(context.Background(), pending)
	if err != nil {
		log.Error("enrich items", "count", len(pending), "err", err)
		return
	}
	for _, it := range pending {
		content, ok := contents[it.ID]
		if !ok || content == "" {
			continue
		}
		if err := p.store.Items.SetContent(userID, it.ID, content); err != nil {
			log.Error("store enriched content", "item_id", it.ID, "err", err)
		}
	}
}

// dedupIdentity is an item's stable per-feed dedup key: its Identity, else GUID.
func dedupIdentity(it feedparse.Item) string {
	if it.Identity != "" {
		return it.Identity
	}
	return it.GUID
}

// wakeString renders the earliest paced-host next-hit for a log line, or "" so
// a cycle with no paced host is unambiguous.
func wakeString(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
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

// cacheItemImages downloads an item's primary image and image-typed enclosures
// into object storage and stores their keys. It returns the enclosure cache keys
// (parallel to it.Enclosures, "" where nothing was cached). A failure on any one
// URL is best-effort: that URL stays remote. The primary image key is written
// straight to the item row.
func (p *Poller) cacheItemImages(ctx context.Context, f store.Feed, it feedparse.Item, itemID int64) []string {
	if p.imgCache == nil || itemID == 0 {
		return nil
	}
	encs := make([]imagecache.Enclosure, 0, len(it.Enclosures))
	for _, e := range it.Enclosures {
		encs = append(encs, imagecache.Enclosure{URL: e.URL, MIMEType: e.MIMEType, Kind: e.Kind})
	}
	res := p.imgCache.Cache(ctx, imagecache.Request{
		Folder:     p.imgCache.Folder(f.FeedURL),
		ItemID:     itemID,
		ImageURL:   it.ImageURL,
		Enclosures: encs,
	})
	if res.ImageKey != "" {
		if err := p.store.Items.SetItemImageCacheKey(itemID, res.ImageKey); err != nil {
			log.Error("store image cache key", "item_id", itemID, "err", err)
		}
	}
	return res.EnclosureKeys
}

// storeEnclosures copies a newly inserted item's media attachments into the
// item_enclosures table. The item may be owned by another feed (a reddit post
// seen through two subscriptions), so it is resolved by the user's cross-feed
// identity too. cacheKeys, when non-empty, carries the object-storage key of each
// enclosure's cached image (parallel to it.Enclosures); ReplaceEnclosures carries
// an existing slot's key forward when none is supplied.
func (p *Poller) storeEnclosures(f store.Feed, it feedparse.Item, itemID int64, cacheKeys []string) error {
	if itemID == 0 {
		identity := it.Identity
		if identity == "" {
			identity = it.GUID
		}
		id, err := p.store.Items.IngestItemID(f.UserID, f.ID, identity, store.CrossFeedKey(it.SharedKey))
		if err != nil {
			return err
		}
		itemID = id
	}
	if itemID == 0 {
		return nil
	}
	encs := make([]store.Enclosure, 0, len(it.Enclosures))
	for i, e := range it.Enclosures {
		enc := store.Enclosure{
			URL: e.URL, MIMEType: e.MIMEType, Size: e.Length,
			Kind: e.Kind, Poster: e.Poster, Title: e.Title,
		}
		if i < len(cacheKeys) {
			enc.CacheKey = cacheKeys[i]
		}
		encs = append(encs, enc)
	}
	return p.store.Items.ReplaceEnclosures(itemID, encs)
}
