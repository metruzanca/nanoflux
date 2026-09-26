// Package poller runs the background feed fetch loop and single-feed
// refreshes.
package poller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/log"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/feedparse"
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
	// of them in one cycle, which is what tends to trigger the first 429. A
	// learned rate-limit window overrides it when larger. Configured via
	// NF_POLL_HOST_SPACING; 0 disables the default spacing.
	defaultHostSpacing = 30 * time.Second
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

// Poller fetches due feeds on an interval. Per-feed schedules come from each
// feed's poll_interval_sec; the ticker just wakes the loop.
type Poller struct {
	store       *store.Store
	client      *http.Client
	interval    time.Duration
	workers     int
	hostSpacing time.Duration
	cool        hostCooler

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
		wg    sync.WaitGroup
		sem   = make(chan struct{}, p.workers)
		mu    sync.Mutex
		total int
		wake  time.Time // earliest paced-host next-hit across groups
	)
	for _, group := range groups {
		wg.Add(1)
		sem <- struct{}{}
		go func(group []store.Feed) {
			defer wg.Done()
			defer func() { <-sem }()
			n, gWake := p.pollGroup(ctx, group)
			mu.Lock()
			total += n
			if !gWake.IsZero() && (wake.IsZero() || gWake.Before(wake)) {
				wake = gWake
			}
			mu.Unlock()
		}(group)
	}
	wg.Wait()
	if total > 0 {
		log.Info("poller finished", "new_items", total, "feeds", len(feeds), "hosts", len(groups))
	}
	return total, wake, nil
}

// pollGroup processes one host's due feeds, oldest-polled first, pacing the host
// when it has a learned rate-limit window. It returns the new-item count and the
// earliest time this host may next be hit (zero when it was not paced).
func (p *Poller) pollGroup(ctx context.Context, group []store.Feed) (int, time.Time) {
	// Fair rotation: least-recently-polled first, so every feed takes a turn.
	sort.SliceStable(group, func(i, j int) bool {
		return group[i].LastPolledAt < group[j].LastPolledAt
	})

	host := store.RegistrableDomain(group[0].FeedURL)
	n := 0
	for i, f := range group {
		if ctx.Err() != nil {
			return n, time.Time{}
		}
		// A host cooling from a rate limit — learned by the poller or by a
		// plugin — is not hit; its feeds stay due until the window clears.
		if p.cool != nil && p.cool.Cooling(f.FeedURL) {
			if until, ok := p.cool.Until(f.FeedURL); ok {
				return n, until
			}
		}
		if host != "" && !p.hostReady(host, time.Now()) {
			// The host is inside its pacing window: leave the remaining feeds
			// due and wake when it clears.
			return n, p.hostNextHitTime(host)
		}
		got, err := p.PollOne(ctx, f)
		n += got
		if err != nil {
			log.Error("poll feed", "feed_id", f.ID, "url", f.FeedURL, "err", err)
		}
		// Space the host's next request (learned rate-limit window wins over
		// the default spacing). Only stop early when another feed is waiting:
		// the group's last feed must not sit out the window it just set, so a
		// single-feed host is never delayed.
		if host != "" && i < len(group)-1 {
			if w := p.effectiveSpacing(host); w > 0 {
				next := time.Now().Add(w)
				p.setHostNextHit(host, next)
				return n, next
			}
		}
	}
	return n, time.Time{}
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
			if host := store.RegistrableDomain(f.FeedURL); host != "" {
				p.learnWindow(host, rl.RetryAfter)
			}
			// Refuse plugin requests to the same host for the same window.
			if p.cool != nil {
				p.cool.Cool(f.FeedURL, until)
			}
			p.store.Feeds.SetPollMeta(f.ID, "", "", fetched, truncateError(err.Error()))
			return 0, err
		}
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
			Identity:    it.Identity,
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
	identity := it.Identity
	if identity == "" {
		identity = it.GUID
	}
	itemID, err := p.store.Items.ByFeedIdentity(feedID, identity)
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
