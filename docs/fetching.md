# How fetching works

This document explains how nanoflux keeps feeds up to date: when it fetches,
how it decides how often, how it stays polite to the sites it fetches from, and
what happens when a fetch fails. It is the reference for the poller
(`internal/poller`), the fetch layer (`internal/feedparse`), and the plugin host
(`internal/plugin`).

The design goal is **be a good citizen**: fetch no more often than a site wants,
back off the moment a site says to, and never let one throttled site starve the
rest of the feed list.

## The two fetch paths

Every feed is fetched through the same entry point, `feedparse.Fetch` (via
`FetchFeed`), so all callers behave identically. It routes in order:

1. **A matching plugin.** Plugins loaded over gRPC from `NF_PLUGINS_DIR`
   declare which URLs they handle. If one matches, it fetches the feed. See [`writing-plugins.md`](writing-plugins.md) and
   [`plugin-architecture.md`](plugin-architecture.md).
2. **The generic parser.** Otherwise nanoflux fetches the URL and parses it as
   RSS / Atom / JSON-feed with `gofeed`.

Both paths end with a **cross-feed key pass**: a plugin that matches the feed URL
for `CapSharedKey` is offered the parsed items and may return each item's
`SharedKey` (cross-feed identity). A plugin uses this to make the same post
stored once across two subscriptions, without owning the fetch.

Newly stored items are then offered to any **content enricher** (`CapEnrich`)
matching each item's link: a plugin may resolve a body (full text, translation,
transcript) with `Host.Do`. It is best-effort and runs only on new items, so a
failure never fails the poll and re-polling never re-fetches an article. The body
is stored as `items.content`, separate from `items.summary` (which the poll
refreshes), and the item modal renders it instead.

Plugins do not do their own networking. Their HTTP calls go back through the
host (`Host.Do`), so the host owns the User-Agent, the timeout, and rate-limit
handling for both paths (details below).

The callers of `feedparse.Fetch` are the background **poller** (the loop),
**feed discovery** (add-time probing), and **manual actions** (the refresh
button, extension adds, OPML import).

## Conditional GET (caching)

Each feed stores the `ETag` and `Last-Modified` from its last successful fetch.
On the next fetch these are sent as `If-None-Match` / `If-Modified-Since`. A
`304 Not Modified` response means nothing changed: nanoflux records the poll
time, clears any backoff, and stores no items. This is the cheapest and most
polite fetch — the server does the least work and returns no body.

## How often a feed is polled

There is no single global cadence. Each feed has its own `poll_interval_sec`,
and several things act on it.

### The base loop

The background poller (`poller.Run`) wakes on an interval (`NF_POLL_INTERVAL`,
default 15m) and fetches every feed that is *due*. It re-computes the next wake
after each pass (see "Waking at the right time"), so the base interval is a
ceiling, not a fixed tick.

The hidden per-user **saved-pages feed** (`feeds.is_system`, `schemaV34`) is
never a fetch target: it is created with `enabled = 0`, holds arbitrary pages
saved from the extension rather than feed entries, and `ListFeedsDue` excludes
it (`is_system = 0`). Nothing else in this document applies to it.

### Per-feed interval and adaptive cadence

- A new feed starts at a default interval (900s / 15m).
- When a poll brings **new items** and adaptive polling is enabled
  (`poll_interval_auto`, on by default), nanoflux derives the interval from the
  feed's **recent posting cadence**: it averages the gaps between the newest
  ~20 item timestamps and clamps the result to **[15m, 7 days]**. A feed that
  posts hourly is polled roughly hourly; a feed that posts every few days is
  polled on that cadence; one that posts monthly is polled weekly (the ceiling).
  This is the "automatic adjusting of delay."
- If adaptive polling is turned off, the interval stays whatever the user set.

### Scheduled to the author's time of day

The interval measures *how often* a feed posts, not *when*. A feed that posts
every day at 3pm but was first polled at 10am would otherwise be polled at 10am
forever, so its 3pm post is seen up to a day late.

To fix that, after every successful poll (including a `304`) an adaptive feed
whose recent posts **cluster at a time of day** gets a scheduled `next_poll_at`
just after that time, instead of waiting out its interval:

- `preferredFetchTime` takes the circular mean of the newest ~20 posts' UTC
  times of day. The circular resultant (0..1) measures concentration: a daily
  poster at a fixed hour is near 1; posts spread across the day are near 0. A
  schedule is set only with at least 6 posts and a resultant of 0.5.
- The target is that time plus a 5-minute buffer (so the item is already in the
  feed) plus a per-feed jitter of up to 10 minutes (`feed id mod 10m`), so feeds
  that share a posting hour don't all fetch at once. Per-host spacing and fair
  rotation still spread a host's feeds (see below).
- The target is never later than `last_polled + poll_interval_sec`, so a schedule
  only ever brings a fetch **forward**; frequent feeds keep their cadence and a
  mis-timed daily feed realigns in one hop.
- Scheduling applies only to intervals of **at most one day**. On a multi-day
  cadence the next occurrence of the posting hour is always nearby, which would
  pull a weekly fetch forward to tomorrow and defeat the longer interval. Those
  feeds poll on their interval instead.
- Stale feeds and feeds with adaptive polling off are never scheduled. A poll
  that brings no new items (the author was late, or a `304`) keeps the plan: the
  next fetch is recomputed from the actual poll time, so the alignment does not
  drift.

Because `next_poll_at` also holds a rate-limit backoff, `feeds.next_poll_scheduled`
(schemaV54) marks which it is: a scheduled deadline shows no "rate limited"
badge and a manual refresh may override it. `EarliestNextPollAt` lets the poller
wake close to a scheduled target rather than on the next base tick.

### Quiet (stale) feeds

If a feed's newest item is **older than 7 days**, its interval is forced to the
7-day maximum (regardless of adaptive polling) so a silent feed is not polled
often. This is why `last_item_at` is tracked separately from the poll time: "no
new posts in a while" is not an error, just a signal to slow down. The feed
card's quiet/abandoned badge scales with the feed's interval too: quiet after
`max(7 days, 2 x interval)` and possibly abandoned after
`max(30 days, 4 x interval)`, so a weekly feed is not labelled abandoned between
its normal posts.

> Note: `last_item_at` only ever moves forward, and an empty value (no items
> yet) is never treated as stale.

### User overrides

The feed edit form can set a fixed interval and toggle adaptive polling. The
manual **refresh** button bypasses the schedule entirely (subject to the
rate-limit guard below).

## Rate limits and politeness

Sites sometimes refuse a fetch with `429 Too Many Requests` (or `503` with a
retry hint). nanoflux treats this as **pacing, not failure**. None of this is
site-specific: it triggers on the response and the host, so any site that
behaves this way is handled the same.

Pacing decisions are logged so the behavior can be confirmed from logs alone:
a rate limit emits `feed rate limited` (Warn) with the host, status,
`retry_after` and `next_poll_at`; each poll pass emits a `poll cycle` (Info)
line with `fetched`, `new_items`, `rate_limited`, `paced` and `next_wake`; and
per-host skip/spacing decisions emit `host cooling, skipping`, `host paced,
skipping`, `host spaced` and `next wake from paced host` at Debug
(`NF_LOG_LEVEL=debug`). Plugin-host limits log `plugin rate limited` and
`plugin request refused: host cooling`, which is the shared cooldown in action.

### Feed URLs are canonicalized by the owning plugin

Some sites have a redirect-free URL shape the reader should store (the `www`
host, a canonical user path, a posts-only feed form). `Store.SetURLPolicy`
installs the plugin layer's per-URL rules, so feed create, feed edit and a
startup pass ask the owning plugin for the canonical shape via
`pluginapi.URLPolicy.CanonicalizeFeedURL`. A site with no plugin that owns its
URL is left untouched.

A site's anonymous feed may sit behind a ~1 request/IP/minute limit, and each
redirect hop spends one of those requests, so the plugin rewrites every URL to
the shape the site answers without a redirect (a short user path to its canonical
form, a bare user feed to its posts-only feed). Because a plugin owns these URLs,
discovery never
probes them either: the plugin derives the feed from the page URL with no
request (`Candidate.Derived`).

### Detecting a limit and how long to wait

`feedparse` recognizes `429`, or `503` carrying a retry hint, and derives the
wait from the standard headers, in order:

1. `Retry-After` (seconds or an HTTP date),
2. `x-ratelimit-reset` (seconds until the window resets).

If neither is present, it uses a default of **5 minutes**, and any value is
clamped to **at most 1 hour** so a bogus header cannot park a feed indefinitely.

### Per-feed backoff: `next_poll_at`

When a fetch is rate-limited, the feed's `next_poll_at` is set to
`now + wait`. `ListFeedsDue` treats that deadline as authoritative on its own:
a feed whose `next_poll_at` is in the future is **not due**, and — importantly —
a feed whose `next_poll_at` has passed **is due even if its normal interval has
not elapsed**. This prevents a short "retry in 60s" from being masked by a long
adaptive interval. A successful poll (or a `304`) clears `next_poll_at`.

The time-of-day scheduler above stores its planned fetch in the same column, and
`feeds.next_poll_scheduled` says which one it is: a scheduled deadline is not a
backoff (no retry badge, and a manual refresh may override it), whereas a
rate-limit deadline disables the refresh button.

### Per-host pacing and fair rotation

Feeds are grouped by **registrable host** (all feeds on one domain are one
group). Three rules keep a host from being hammered:

- **One host at a time.** Feeds in a group are fetched sequentially, while
  different hosts run in parallel up to `NF_POLL_WORKERS`. This prevents
  bursting a single site.
- **Default spacing.** Even a host that has never rate-limited the poller is
  spaced: after fetching one of its feeds, the next same-host fetch waits
  `NF_POLL_HOST_SPACING` (default **60 seconds**, matching the tightest
  anonymous per-IP window we know of). This stops a domain with many
  due feeds from firing them all in one cycle, which is what tends to trigger
  the first `429`. A single-feed host is never delayed, and a host's last
  waiting feed is fetched promptly once the window clears. Set the variable to
  `0s` to disable; a learned rate-limit window always overrides the default.
- **Learned window.** When a host rate-limits a fetch, the poller remembers how
  long it asked to be left alone (`hostWindow`, floored at **60 seconds**) and
  records the next time the host may be hit (`hostNextHit`). Until that time,
  the host's remaining feeds are **not fetched** — but they stay **due**, so
  they are picked up the moment the window clears.

The `NF_POLL_HOST_SPACING` floor is the poller's **30-second wake floor**: a
smaller configured value cannot make the loop revisit a host sooner.

Within a group, feeds are processed **least-recently-polled first**. So if a
host can only be hit once per minute and there are twenty of its feeds, they
rotate: each feed takes its turn rather than a few winning every cycle.

### Waking at the right time

Because a paced host must be revisited promptly, the poller does not simply sleep
for the base interval. After each pass it wakes at the **earliest** of:

- the base interval (`NF_POLL_INTERVAL`), or
- the next moment a paced host becomes hittable, or
- the soonest planned `next_poll_at` (a scheduled time-of-day fetch or a
  rate-limit retry),

floored at 30 seconds so a just-cleared backoff cannot make the loop spin. If a
deadline is already in the past by the time a pass finishes (another host's
spacing window landed a second earlier), the poller retries at that 30-second
floor rather than sleeping the whole base interval.

The result: a host with, say, twenty feeds and a 1-minute window refreshes them
all across roughly twenty minutes, continuously, instead of one per day.

### Admin polling health

The admin page's **polling** card shows whether the poller can keep up. A feed is
**behind** when its last poll is older than its own `poll_interval_sec` (a
never-polled feed is measured from its creation time), so rate-limited feeds
still count while they wait. Per host it shows:

- **demand/day**: the sum of every enabled feed's required polls per day
  (`86400 / poll_interval_sec`).
- **capacity/day**: what the host's effective spacing allows
  (`86400 / spacing`), including a learned rate-limit window.
- **clear**: the estimated time to drain the host's current backlog at that
  spacing.

When demand exceeds capacity, the host is marked **overloaded**. That is the
permanent-lateness signal: the backlog cannot drain until feeds are removed from
that host or their intervals are lengthened. A large backlog with demand below
capacity is only a transient catch-up, for example after downtime. Active 429
cooldowns are not included in the estimate.

### The manual refresh button

The refresh button and its handler respect a **rate-limit** `next_poll_at`. While
a feed is cooling, the row shows a **"rate limited · retry in …"** badge and the
button is disabled; a click during the window is a no-op rather than another
request. A *scheduled* deadline (`next_poll_scheduled`) is not a backoff, so the
button stays enabled and a manual refresh overrides the plan.

### Errors are explained

If a discovery probe hits a limit, that reason is surfaced
to the user (e.g. *"the site is rate-limiting requests (HTTP 429) — wait a bit
and try again"*) instead of a misleading "no feed found". Raw fetch URLs never
leak into user-facing messages.

### Plugins

Plugins never see raw networking. Their `Host.Do` calls go
through the host, which applies the same User-Agent and timeout policy and
inspects every response for limits, cooling the request host. A plugin that
cannot avoid a limit returns a `RateLimit`, and the poller applies exactly the
backoff described above.

The plugin host and the poller share **one** cooldown instance (wired in
`cmd/server/main.go`): a limit seen by either paces both, so a feed fetch and a
plugin's auxiliary request cannot trip the same host twice in a row. The default
spacing above is poller-only — it never blocks a plugin request.

### Ownership is derived, not locked

A feed served by a plugin records the owning plugin's name
(`feeds.plugin_name`). That name is bookkeeping and display only: fetch routing
always matches the feed URL against the loaded plugins. At startup the plugin
system re-derives every stored feed's owner against the plugins that actually
loaded:

- A feed whose URL matches a loaded plugin is adopted (or kept).
- A feed whose URL matches no loaded plugin has its owner cleared and falls back
  to the **generic parser**; it is not disabled. This is why there is no reset
  button: removing, renaming, or swapping a plugin is handled on the next
  launch.
- A feed a previous build parked for a missing plugin
  (`plugin not loaded: <name>`) is re-enabled once. A feed the user **paused by
  hand** is never resumed.

The admin plugins card shows, per loaded plugin, the domains and distinct feed
URLs it currently matches. That is the debug view for ownership, including a
plugin that matches only part of a shared domain (a URL path or query shape).

## Failures and their effects

| Outcome | What nanoflux does |
| --- | --- |
| `304 Not Modified` | Records the poll time, clears backoff, stores nothing |
| Success with items | Stores new items, records `ETag`/`Last-Modified`, clears backoff, adjusts the interval |
| Rate limit (`429` / hinted `503`) | Sets `next_poll_at`, learns the host window, records a `last_error`; the feed is not treated as broken |
| Other HTTP error (`4xx`/`5xx`) | Records `last_error`; retried on the feed's normal interval |
| Parse error (not a feed) | Records `last_error`; retried on the normal interval |

`last_error` is shown as a **"last poll failed"** badge on the author page's
feed card. It is distinct from the amber **stale** badge ("no new posts in a
while"), which is not a failure.

## Item categories

Feeds carry context that surfaces without any site-specific code. Categories are
normalized at parse time from:

- each RSS/Atom `<category>` value (gofeed prefers the atom `label`),
- each item author name (a leading slash is dropped).

One feed therefore gives each item its section as a category and its poster as
the author; another gives the destination section. Categories are
stored newline-joined in `items.categories` (schemaV36) and refreshed on re-poll,
so an item already stored gains them without a re-fetch. They power the scoped
show/hide tag filter (and are available to plugins via
`pluginapi.Item.Categories`); the former ingest filter-rule system was removed
for a clean reimplementation.

## Pagination ("load older items")

A feed may advertise a next page via a feed-level `rel="next"` link or a
`page=`-style query. The first poll records that cursor. The feed's card on its
author page offers a **"load older items"** button which walks up to 5 pages per
click (bounded so an unbounded history cannot stall the request) until the
history is exhausted, refreshing the author's item list as it goes. Routine
polls never touch the cursor, so they cannot clobber an in-progress backfill.

## Configuration

| Variable | Default | Effect |
| --- | --- | --- |
| `NF_POLL_INTERVAL` | `15m` | The poller's base wake interval (a ceiling; dynamic wakes can be sooner) |
| `NF_POLL_WORKERS` | `4` | Concurrent fetches across *distinct hosts*; a single host's feeds are serial |
| `NF_POLL_HOST_SPACING` | `60s` | Minimum spacing between two fetches to the same host, even before it rate-limits. `0s` disables; floored at 30s by the wake floor |
| `NF_USER_AGENT` | `nanoflux (<repo URL>)` | Outbound User-Agent for fetching and discovery; set a contact URL for a large instance |

Per-feed interval and adaptive polling are set in the feed edit form, not by env
var.

## Where this lives in the code

| Concern | File |
| --- | --- |
| Background loop, per-host pacing, rotation, dynamic wake, `scheduleNextPoll` | `internal/poller/poller.go` |
| Adaptive interval, time-of-day detection (`preferredFetchTime`), stale thresholds | `internal/poller/interval.go` |
| Rate-limit detection, `Retry-After` / `x-ratelimit-reset` parsing | `internal/feedparse/ratelimit.go` |
| Generic fetch: conditional GET, parse, pagination cursor | `internal/feedparse/feedparse.go` |
| Host-mediated HTTP + rate-limit cooling for plugins | `internal/plugin/host.go`, `internal/plugin/cooldown.go` |
| Ingest cross-feed key pass and dispatch to plugins | `internal/plugin/dispatch.go`, `internal/feedparse/feedparse.go` |
| Per-URL site rules (canonical shape, feed token) from plugins | `internal/plugin/urlpolicy.go`, `internal/store/domain.go` |
| `next_poll_at` / `next_poll_scheduled` storage, `ListFeedsDue`, `EarliestNextPollAt` | `internal/store/feed.go`, `internal/store/queries/feeds.sql` |
