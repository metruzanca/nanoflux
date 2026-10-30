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
	"github.com/metruzanca/nanoflux/internal/imagecache"
	"github.com/metruzanca/nanoflux/internal/safedial"
	"github.com/metruzanca/nanoflux/internal/store"
)

const (
	fetchTimeout = 30 * time.Second

	// rateLimitFloor is the minimum spacing the poller enforces between two
	// requests to a host that has rate-limited it. Hosts report their own window
	// (Retry-After / x-ratelimit-reset); this floor covers a missing or tiny
	// hint. The tightest known hosts allow only ~1 anonymous request per minute.
	rateLimitFloor = 60 * time.Second

	// minWake is the smallest next-wake delay, so a just-cleared backoff can't
	// make the loop spin.
	minWake = 30 * time.Second

	// defaultHostSpacing is the spacing the poller enforces between two
	// requests to the same registrable host even when the host has never
	// rate-limited it. It stops a domain with many due feeds from bursting all
	// of them in one cycle, which is what tends to trigger the first 429. 60s
	// matches the tightest anonymous per-IP window we know of. A learned
	// rate-limit window overrides it when larger. Configured via
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
	// minPoll returns the smallest interval (seconds) a feed URL may use: the
	// host's configured floor raised by any plugin floor. Nil keeps the
	// historical adaptive floor. It is never allowed below minPollInterval.
	minPoll func(feedURL string) int

	// hostWindow records, per registrable host, the minimum spacing the host
	// has asked for after a rate limit (learned from Retry-After /
	// x-ratelimit-reset). hostNextHit is the earliest time the host may be hit
	// again. Both are in-memory (reset on restart): a host that never
	// rate-limits is never paced. This replaces a coarse "cool the host for the
	// whole cycle", which starved the host's other feeds.
	hostMu      sync.Mutex
	hostWindow  map[string]time.Duration
	hostNextHit map[string]time.Time

	// queues holds each registrable host's FIFO of due feed IDs. It survives
	// across cycles: a feed is appended when it becomes due and keeps its place
	// until polled, so a feed that becomes due again cannot jump ahead of the
	// backlog. Seeded most-overdue-first when a host's queue is empty. In-memory
	// and rebuilt on restart. cycleMu serializes pollDue so the queue map is
	// only touched by one cycle at a time.
	queues  map[string]*hostQueue
	cycleMu sync.Mutex
}

// hostQueue is one registrable host's FIFO of due feed IDs. IDs are immutable
// feed identities; the fresh feed snapshot is looked up in the cycle's due map.
type hostQueue struct {
	ids []int64
	set map[int64]struct{}
}

func newHostQueue() *hostQueue {
	return &hostQueue{set: map[int64]struct{}{}}
}

func (q *hostQueue) add(id int64) {
	if _, ok := q.set[id]; ok {
		return
	}
	q.set[id] = struct{}{}
	q.ids = append(q.ids, id)
}

func (q *hostQueue) pop() (int64, bool) {
	if len(q.ids) == 0 {
		return 0, false
	}
	id := q.ids[0]
	q.ids = q.ids[1:]
	delete(q.set, id)
	return id, true
}

func (q *hostQueue) len() int { return len(q.ids) }

// queueHost is the map key for a feed's host queue. A feed whose host cannot
// be derived gets its own bucket keyed by URL, so it never blocks another feed.
func queueHost(f store.Feed) string {
	if host := store.RegistrableDomain(f.FeedURL); host != "" {
		return host
	}
	return "\x00" + f.FeedURL
}

// hostPlan is one host's ready queue for a cycle: the FIFO, the fresh feed
// snapshots it refers to, and the head's due deadline for scheduling order.
type hostPlan struct {
	host  string
	queue *hostQueue
	due   map[int64]store.Feed
	head  time.Time
}

func New(st *store.Store, interval time.Duration, workers int) *Poller {
	if workers <= 0 {
		workers = 4
	}
	return &Poller{
		store:       st,
		client:      safedial.Client(fetchTimeout),
		interval:    interval,
		workers:     workers,
		hostSpacing: defaultHostSpacing,
		hostWindow:  map[string]time.Duration{},
		hostNextHit: map[string]time.Time{},
		queues:      map[string]*hostQueue{},
	}
}

// SetHostSpacing sets the default minimum spacing between two requests to the
// same host, applied even to hosts that have never rate-limited the poller. A
// learned rate-limit window still overrides it when larger. Zero disables it.
func (p *Poller) SetHostSpacing(d time.Duration) { p.hostSpacing = d }

// minPollInterval is the absolute smallest per-feed interval, regardless of
// what the host or a plugin configures.
const minPollInterval = 60

// SetMinPollInterval installs the per-feed poll floor: fn returns the smallest
// interval in seconds a feed URL may use, composed from the host's configured
// minimum and any plugin floor. The poller never goes below minPollInterval.
// A nil fn keeps the historical 15-minute adaptive floor.
func (p *Poller) SetMinPollInterval(fn func(feedURL string) int) { p.minPoll = fn }

// MinPollSeconds composes a configured per-feed floor with a plugin floor
// resolver into the seconds-valued function SetMinPollInterval takes. A nil
// resolver applies only the configured floor.
func MinPollSeconds(global time.Duration, pluginFloor func(string) time.Duration) func(string) int {
	base := int(global / time.Second)
	return func(rawURL string) int {
		floor := base
		if pluginFloor != nil {
			if d := int(pluginFloor(rawURL) / time.Second); d > floor {
				floor = d
			}
		}
		return floor
	}
}

// feedFloorSec returns the smallest interval (seconds) a feed may poll at. It
// applies to every feed, not only adaptive ones, so lowering the global floor
// takes effect without rewriting stored intervals.
func (p *Poller) feedFloorSec(f store.Feed) int {
	if p.minPoll == nil {
		return 0
	}
	sec := p.minPoll(f.FeedURL)
	if sec < minPollInterval {
		sec = minPollInterval
	}
	return sec
}

// adaptiveFloorSec is the floor the adaptive cadence may not go below: the
// host/plugin floor, or the historical adaptive minimum when none is set.
func (p *Poller) adaptiveFloorSec(f store.Feed) int {
	floor := p.feedFloorSec(f)
	if floor < adaptiveFloor {
		floor = adaptiveFloor
	}
	return floor
}

// EffectiveSpacing returns the spacing the poller currently enforces before the
// next request to rawURL's registrable host: its learned rate-limit window when
// that is larger than the configured default. Zero means no spacing (the host
// is never deliberately delayed). It is the read-only view used by the admin
// polling health card to estimate whether a host can keep up with its feeds.
func (p *Poller) EffectiveSpacing(rawURL string) time.Duration {
	host := store.RegistrableDomain(rawURL)
	if host == "" {
		return 0
	}
	return p.effectiveSpacing(host)
}

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

// nextWakeDelay returns how long Run should sleep before the next poll pass.
// A zero wake means no planned deadline, so it sleeps the base interval. A
// non-zero wake already at or before now means the deadline slipped while the
// cycle was running (typically another host's spacing window): it retries at
// minWake rather than sleeping the whole base interval, which would otherwise
// collapse a paced host's rotation to one feed per base tick.
func nextWakeDelay(wake, now time.Time, interval time.Duration) time.Duration {
	if wake.IsZero() {
		return interval
	}
	delay := wake.Sub(now)
	if delay <= 0 {
		delay = minWake
	}
	if delay > interval {
		delay = interval
	}
	if delay < minWake {
		delay = minWake
	}
	return delay
}

// Run polls due feeds, then wakes at the earliest of the base interval, the
// next moment a paced host becomes hittable, or the soonest planned deadline
// (scheduled fetch or rate-limit retry), whichever is sooner (floored at
// minWake). The dynamic wake is what lets a rate-limited host's feeds rotate and
// a scheduled fetch land close to its target instead of on the next base tick.
func (p *Poller) Run(ctx context.Context) {
	log.Info("poller starting", "interval", p.interval)
	for {
		_, wake, err := p.pollDue(ctx)
		if err != nil {
			log.Error("poll due", "err", err)
		}
		now := time.Now()
		delay := nextWakeDelay(wake, now, p.interval)
		// A planned deadline or paced host can pull the next wake in sooner than
		// the base interval; record why, so logs show the reason for the sleep.
		if !wake.IsZero() && delay <= p.interval {
			if wake.After(now) {
				log.Debug("next wake from deadline", "delay", delay.Round(time.Second), "at", wake.Round(time.Second))
			} else {
				log.Debug("wake deadline passed, retrying", "delay", delay.Round(time.Second), "wake", wake.Round(time.Second))
			}
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
// Each registrable host has a persistent FIFO of due feeds. An empty queue is
// seeded most-overdue-first; while it is non-empty, newly due feeds are appended
// to the tail. Hosts are serviced in queue-head deadline order, each queue drained
// one feed per spacing window. Different hosts run in parallel up to the worker
// limit. A host that has rate-limited the poller keeps its queue intact and
// resumes when the window clears.
func (p *Poller) pollDue(ctx context.Context) (int, time.Time, error) {
	p.cycleMu.Lock()
	defer p.cycleMu.Unlock()

	now := db.Now()
	feeds, err := p.store.Feeds.ListDue(now)
	if err != nil {
		return 0, time.Time{}, err
	}
	// Seed the wake with the soonest planned deadline (a scheduled time-of-day
	// fetch or a rate-limit retry), so the loop returns close to it even when
	// nothing is due right now; Run caps it at the base interval.
	var wake time.Time
	if earliest, err := p.store.Feeds.EarliestNextPollAt(now); err == nil && earliest != "" {
		if t, err := db.ParseTime(earliest); err == nil {
			wake = t
		}
	}
	if len(feeds) == 0 {
		p.clearQueues()
		return 0, wake, nil
	}

	// A stored interval may predate the current floor (the global minimum was
	// raised, or a plugin loaded after the feed was created). Keep the feed out
	// of this cycle until its floor elapses, and wake at that deadline instead
	// of the base interval so it is not late by a whole cycle.
	due := len(feeds)
	feeds, floorWake := p.applyPollFloor(feeds, now)
	if !floorWake.IsZero() && (wake.IsZero() || floorWake.Before(wake)) {
		wake = floorWake
	}
	if len(feeds) == 0 {
		p.clearQueues()
		return 0, wake, nil
	}

	// Reconcile each host's persistent FIFO with the feeds that are due now,
	// then order hosts by how overdue their queue head is. This is the ordering
	// that drains the current backlog before feeds that become due later.
	plans := p.reconcileQueues(feeds)
	queued := 0
	for _, plan := range plans {
		queued += plan.queue.len()
	}

	var (
		wg  sync.WaitGroup
		sem = make(chan struct{}, p.workers)
		mu  sync.Mutex
		sum cycleSummary
	)
	for _, plan := range plans {
		wg.Add(1)
		sem <- struct{}{}
		go func(plan hostPlan) {
			defer wg.Done()
			defer func() { <-sem }()
			res := p.pollHost(ctx, plan)
			mu.Lock()
			sum.merge(res)
			if !res.wake.IsZero() && (wake.IsZero() || res.wake.Before(wake)) {
				wake = res.wake
			}
			mu.Unlock()
		}(plan)
	}
	wg.Wait()
	// Pick up any deadline created during the cycle (a rate-limit backoff or a
	// newly planned time-of-day fetch), so the loop wakes at it instead of the
	// base interval even when the paced feed was the last one in its queue.
	if earliest, err := p.store.Feeds.EarliestNextPollAt(db.Now()); err == nil && earliest != "" {
		if t, err := db.ParseTime(earliest); err == nil && (wake.IsZero() || t.Before(wake)) {
			wake = t
		}
	}
	// One line per cycle, regardless of whether new items arrived, so logs alone
	// show how many feeds ran, how many were paced, and what the next wake is.
	log.Info("poll cycle",
		"feeds", len(feeds),
		"floored", due-len(feeds),
		"queued", queued,
		"hosts", len(plans),
		"fetched", sum.fetched,
		"new_items", sum.newItems,
		"rate_limited", sum.rateLimited,
		"paced", sum.paced,
		"next_wake", wakeString(wake),
	)
	return sum.newItems, wake, nil
}

// clearQueues drops every host queue. It runs when no feed is due, at which
// point no queued feed can be eligible either (a queued feed is only dropped
// from a cycle by not being due).
func (p *Poller) clearQueues() {
	for host := range p.queues {
		delete(p.queues, host)
	}
}

// reconcileQueues updates each host's FIFO against the current due set and
// returns the hosts in the order they should be serviced.
//
//   - A queued feed that is no longer due (polled, disabled, deleted, floor-held,
//     rate-limited into the future, or moved to another host) is dropped.
//   - A host whose queue is empty is seeded with all of its due feeds, sorted by
//     due deadline ascending, so the most overdue is at the head.
//   - A non-empty queue keeps its order; feeds that became due since the last
//     cycle are sorted by deadline among themselves and appended to the tail.
//
// The result is a stable FIFO per host: the current backlog drains first and a
// feed that becomes due later cannot jump ahead of it.
func (p *Poller) reconcileQueues(feeds []store.Feed) []hostPlan {
	groups := groupByHost(feeds)
	byHost := make(map[string][]store.Feed, len(groups))
	for _, group := range groups {
		byHost[queueHost(group[0])] = group
	}

	// Drop queues for hosts with nothing due, and any queued ID that is no
	// longer in its host's due set.
	for host, q := range p.queues {
		group, ok := byHost[host]
		if !ok {
			delete(p.queues, host)
			continue
		}
		present := make(map[int64]struct{}, len(group))
		for _, f := range group {
			present[f.ID] = struct{}{}
		}
		kept := q.ids[:0]
		q.set = make(map[int64]struct{}, len(q.ids))
		for _, id := range q.ids {
			if _, ok := present[id]; ok {
				kept = append(kept, id)
				q.set[id] = struct{}{}
			}
		}
		q.ids = kept
	}

	plans := make([]hostPlan, 0, len(byHost))
	for host, group := range byHost {
		q := p.queues[host]
		if q == nil {
			q = newHostQueue()
			p.queues[host] = q
		}
		// New arrivals go after everything already queued. Sort the batch by
		// deadline so the most overdue of the new feeds still goes first.
		fresh := make([]store.Feed, 0, len(group))
		for _, f := range group {
			if _, ok := q.set[f.ID]; !ok {
				fresh = append(fresh, f)
			}
		}
		sort.SliceStable(fresh, func(i, j int) bool {
			return p.dueDeadline(fresh[i]).Before(p.dueDeadline(fresh[j]))
		})
		for _, f := range fresh {
			q.add(f.ID)
		}

		due := make(map[int64]store.Feed, len(group))
		for _, f := range group {
			due[f.ID] = f
		}
		plan := hostPlan{host: host, queue: q, due: due}
		if len(q.ids) > 0 {
			if f, ok := due[q.ids[0]]; ok {
				plan.head = p.dueDeadline(f)
			}
		}
		plans = append(plans, plan)
	}

	sort.SliceStable(plans, func(i, j int) bool {
		return plans[i].head.Before(plans[j].head)
	})
	return plans
}

// dueDeadline is when a feed was expected to be polled: its explicit
// next_poll_at (rate-limit retry or clock-aligned schedule) when set, otherwise
// last poll (or creation) plus the effective interval. Earlier means more
// overdue, which is the priority used to seed a host queue.
func (p *Poller) dueDeadline(f store.Feed) time.Time {
	if f.NextPollAt != "" {
		if t, err := db.ParseTime(f.NextPollAt); err == nil {
			return t
		}
	}
	ref := f.LastPolledAt
	if ref == "" {
		ref = f.CreatedAt
	}
	t, err := db.ParseTime(ref)
	if err != nil {
		return time.Time{}
	}
	interval := f.PollIntervalSec
	if floor := p.feedFloorSec(f); floor > interval {
		interval = floor
	}
	return t.Add(time.Duration(interval) * time.Second)
}

// applyPollFloor returns the feeds whose effective floor has elapsed and the
// earliest time a held-back feed may poll. A feed due by an explicit
// next_poll_at (a rate-limit retry or a clock-aligned schedule) is always kept:
// that deadline is authoritative and was already computed under the floor when
// it was set.
func (p *Poller) applyPollFloor(feeds []store.Feed, now string) ([]store.Feed, time.Time) {
	nowT, err := db.ParseTime(now)
	if err != nil {
		return feeds, time.Time{}
	}
	kept := make([]store.Feed, 0, len(feeds))
	var wake time.Time
	for _, f := range feeds {
		if f.NextPollAt != "" {
			kept = append(kept, f)
			continue
		}
		floor := p.feedFloorSec(f)
		if floor <= 0 || f.PollIntervalSec >= floor || f.LastPolledAt == "" {
			kept = append(kept, f)
			continue
		}
		last, err := db.ParseTime(f.LastPolledAt)
		if err != nil {
			kept = append(kept, f)
			continue
		}
		next := last.Add(time.Duration(floor) * time.Second)
		if !nowT.Before(next) {
			kept = append(kept, f)
			continue
		}
		if wake.IsZero() || next.Before(wake) {
			wake = next
		}
	}
	return kept, wake
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

// pollHost drains one host's FIFO until the host needs to be spaced or is
// cooling. The queue is left in place when the host is not ready, so the same
// order resumes when the window clears. A feed is removed from the queue when it
// is polled; it re-enters at the tail the next time it becomes due.
func (p *Poller) pollHost(ctx context.Context, plan hostPlan) cycleResult {
	var res cycleResult
	for plan.queue.len() > 0 {
		if ctx.Err() != nil {
			return res
		}
		id := plan.queue.ids[0]
		f, ok := plan.due[id]
		if !ok {
			// Defensive: reconciliation should have removed it.
			plan.queue.pop()
			continue
		}
		host := store.RegistrableDomain(f.FeedURL)
		// A host cooling from a rate limit, learned by the poller or by a
		// plugin, is not hit; its queue stays intact until the window clears.
		if p.cool != nil && p.cool.Cooling(f.FeedURL) {
			if until, ok := p.cool.Until(f.FeedURL); ok {
				remaining := plan.queue.len()
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
			// The host is inside its pacing window: leave the queue in place and
			// wake when it clears.
			until := p.hostNextHitTime(host)
			remaining := plan.queue.len()
			res.paced += remaining
			res.wake = until
			log.Debug("host paced, skipping",
				"host", host, "until", until.Round(time.Second),
				"remaining_sec", int(time.Until(until).Round(time.Second).Seconds()),
				"skipped", remaining,
			)
			return res
		}
		plan.queue.pop()
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
		// the default spacing). Only wait when another feed is still queued: the
		// last feed must not sit out the window it just set, so a single-feed
		// host is never delayed.
		if plan.queue.len() > 0 {
			if w := p.effectiveSpacing(host); w > 0 {
				next := time.Now().Add(w)
				p.setHostNextHit(host, next)
				res.paced += plan.queue.len()
				res.wake = next
				log.Debug("host spaced",
					"host", host, "spacing", w.Round(time.Second),
					"until", next.Round(time.Second),
					"skipped", plan.queue.len(),
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
		host := queueHost(f)
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
		fetched := db.Now()
		p.store.Feeds.SetPollMeta(f.ID, f.ETag, f.LastModified, fetched, "")
		// Recompute the schedule so a 304 (no content change) still re-anchors
		// the planned fetch time instead of drifting with the wake.
		p.clearSchedule(f.ID)
		p.updateCadence(f, fetched, 0)
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
			p.store.Feeds.SetNextPollScheduled(f.ID, false)
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
		p.clearSchedule(f.ID)
		return 0, err
	}

	newItems, err := p.ingest(ctx, f, res, fetched)
	if err != nil {
		return newItems, err
	}
	if err := p.store.Feeds.SetPollMeta(f.ID, res.ETag, res.LastModified, fetched, ""); err != nil {
		return newItems, err
	}
	// A successful poll clears any previous deadline (rate-limit backoff or a
	// planned time-of-day fetch); updateCadence sets a new schedule if the feed
	// has a preferred posting time.
	p.clearSchedule(f.ID)
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
//     to adaptiveCeil (7 days) so a quiet feed isn't polled often, regardless
//     of whether adaptive polling is enabled;
//   - otherwise, if adaptive polling is enabled and the poll brought new items,
//     derive the interval from the average gap between the feed's recent posts.
//
// last_item_at only moves forward; it is separate from the poll time, so "no
// new posts in a while" is distinct from a feed error.
//
// It also plans the next fetch for a feed whose posts cluster at a time of day,
// so a daily poster is polled just after its usual hour instead of whenever its
// interval happens to elapse (see scheduleNextPoll).
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
		// A quiet feed is polled at most once a week, regardless of whether
		// adaptive polling is enabled.
		want = adaptiveCeil
	case f.PollIntervalAuto && newItems > 0:
		if sec, ok := adaptiveInterval(times, p.adaptiveFloorSec(f), adaptiveCeil); ok {
			want = sec
		}
	}
	if want != 0 && want != f.PollIntervalSec {
		if err := p.store.Feeds.SetPollInterval(f.ID, want); err != nil {
			log.Error("set poll interval", "feed_id", f.ID, "err", err)
			return
		}
		log.Info("adjusted poll interval", "feed_id", f.ID, "from", f.PollIntervalSec, "to", want)
		f.PollIntervalSec = want
	}

	p.scheduleNextPoll(f, times, now)
}

// scheduleNextPoll plans the feed's next fetch for just after the time of day
// its recent posts cluster at, so a daily poster is caught minutes after it
// posts rather than up to a day later. It applies only to adaptive feeds that
// are not stale and have a clear time-of-day signal; any other feed is left on
// its interval (PollOne has already cleared any previous deadline). The plan is
// never later than the feed's own interval would have polled, so a schedule only
// ever brings a fetch forward.
func (p *Poller) scheduleNextPoll(f store.Feed, times []string, now string) {
	if !f.PollIntervalAuto || isStale(f.LastItemAt, now, staleAfter) {
		return
	}
	// A multi-day cadence must not be pulled forward to the next occurrence of
	// the posting hour (which is at most a day away).
	if f.PollIntervalSec > int(scheduleMaxInterval/time.Second) {
		return
	}
	sec, ok := preferredFetchTime(times)
	if !ok {
		return
	}
	nowT, err := db.ParseTime(now)
	if err != nil {
		return
	}
	target := nextFetchTime(nowT, sec, f.ID)
	// The schedule may bring a fetch forward from the interval deadline, but it
	// must never undercut the poll floor: a frequent feed with a strong
	// time-of-day signal would otherwise poll sooner than its minimum.
	floor := p.feedFloorSec(f)
	if floor > 0 {
		if earliest := nowT.Add(time.Duration(floor) * time.Second); target.Before(earliest) {
			target = earliest
		}
	}
	interval := f.PollIntervalSec
	if floor > interval {
		interval = floor
	}
	if gate := nowT.Add(time.Duration(interval) * time.Second); gate.Before(target) {
		target = gate
	}
	if err := p.store.Feeds.SetNextPollAt(f.ID, db.FormatTime(target)); err != nil {
		log.Error("schedule next poll", "feed_id", f.ID, "err", err)
		return
	}
	if err := p.store.Feeds.SetNextPollScheduled(f.ID, true); err != nil {
		log.Error("schedule next poll flag", "feed_id", f.ID, "err", err)
		return
	}
	log.Debug("scheduled next poll", "feed_id", f.ID, "at", target.Round(time.Second))
}

// clearSchedule drops any planned deadline (rate-limit backoff or time-of-day
// schedule) on a feed, leaving it on its normal interval.
func (p *Poller) clearSchedule(id int64) {
	p.store.Feeds.SetNextPollAt(id, "")
	p.store.Feeds.SetNextPollScheduled(id, false)
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
	seen := map[string]bool{url: true}
	for page := 0; page < maxBackfillPages; page++ {
		res, err := feedparse.Fetch(ctx, url, p.client, "", "")
		if err != nil {
			return newItems, false, fmt.Errorf("fetch %s: %w", url, err)
		}
		inserted, err := p.ingest(ctx, f, res, db.Now())
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
			// A page with zero new items means every older item is already
			// stored (feeds are newest-first). A repeated URL is a loop. Either
			// way the history is exhausted.
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

// ingest stores a fetched page's items for a feed and copies enclosures for
// newly inserted items. It returns how many items were new. Newly stored items
// are then offered to the body enricher (best-effort; a failure never fails the
// poll).
//
// When image caching is on for the feed (its plugin forces it, or the user
// enabled it), each item's primary image and image enclosures are downloaded
// into object storage and their keys stored, so a view renders the cached copy
// instead of a URL that may have expired. Caching is best-effort and never fails
// the poll.
func (p *Poller) ingest(ctx context.Context, f store.Feed, res feedparse.Result, fetched string) (int, error) {
	newItems := 0
	cacheImages := p.imgCache != nil && (f.CacheImages || p.imgCache.Forced(f.FeedURL))
	// Collect the page first, then store it in one transaction. The poller used
	// to open a transaction per item, which is a lot of commits on a
	// single-writer SQLite host.
	type incoming struct {
		it   feedparse.Item
		item store.Item
	}
	incomingItems := make([]incoming, 0, len(res.Items))
	for _, it := range res.Items {
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
		incomingItems = append(incomingItems, incoming{it: it, item: item})
	}

	storeItems := make([]store.Item, len(incomingItems))
	for i := range incomingItems {
		storeItems[i] = incomingItems[i].item
	}
	results, err := p.store.Items.UpsertMany(f.ID, storeItems)
	if err != nil {
		return newItems, err
	}

	var stored []store.Item
	for i, inc := range incomingItems {
		res := results[i]
		if res.Inserted {
			newItems++
		}
		// The upsert returns the stored id, so enclosures and the image cache do
		// not need a separate lookup (the old IngestItemID query).
		itemID := res.ItemID
		var enclosureKeys []string
		var enclosureSizes []int64
		if cacheImages && itemID != 0 {
			enclosureKeys, enclosureSizes = p.cacheItemImages(ctx, f, inc.it, itemID)
		}
		// Enclosures are stored whenever the item carries them, not only when it
		// is new: a feed polled before its parser learned to expose media (a
		// plugin that started returning them, say) gains its attachments on the
		// next poll.
		// ReplaceEnclosures is delete-then-insert, so this keeps them current.
		if len(inc.it.Enclosures) > 0 {
			if err := p.storeEnclosures(f, inc.it, itemID, enclosureKeys, enclosureSizes); err != nil {
				log.Error("store enclosures", "feed_id", f.ID, "guid", inc.it.GUID, "err", err)
			}
		}
		// Enrich only newly inserted items, and only in the owner feed (a
		// cross-feed member already enriched by the other feed is skipped).
		if res.Inserted && p.enricher != nil && itemID != 0 {
			it := inc.item
			it.ID = itemID
			it.UserID = f.UserID
			stored = append(stored, it)
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
// into object storage and stores their keys and byte sizes. It returns the
// enclosure cache keys and sizes (parallel to it.Enclosures, "" / 0 where
// nothing was cached). A failure on any one URL is best-effort: that URL stays
// remote. The primary image key and size are written straight to the item row.
func (p *Poller) cacheItemImages(ctx context.Context, f store.Feed, it feedparse.Item, itemID int64) ([]string, []int64) {
	if p.imgCache == nil || itemID == 0 {
		return nil, nil
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
		if err := p.store.Items.SetItemImageCacheKey(itemID, res.ImageKey, res.ImageBytes); err != nil {
			log.Error("store image cache key", "item_id", itemID, "err", err)
		}
	}
	return res.EnclosureKeys, res.EnclosureBytes
}

// storeEnclosures copies a newly inserted item's media attachments into the
// item_enclosures table. The item may be owned by another feed (seen through two
// subscriptions), so it is resolved by the user's cross-feed
// identity too. cacheKeys/cacheSizes, when non-empty, carry the object-storage
// key and byte size of each enclosure's cached image (parallel to
// it.Enclosures); ReplaceEnclosures carries an existing slot's key and size
// forward when none is supplied.
func (p *Poller) storeEnclosures(f store.Feed, it feedparse.Item, itemID int64, cacheKeys []string, cacheSizes []int64) error {
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
		if i < len(cacheSizes) {
			enc.CacheSize = cacheSizes[i]
		}
		encs = append(encs, enc)
	}
	return p.store.Items.ReplaceEnclosures(itemID, encs)
}
