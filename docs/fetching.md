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

1. **A matching plugin.** Plugins (native, compiled in, or external, loaded over
   gRPC from `NF_PLUGINS_DIR`) declare which URLs they handle. If one matches,
   it fetches the feed. See [`writing-plugins.md`](writing-plugins.md) and
   [`plugin-architecture.md`](plugin-architecture.md).
2. **The generic parser.** Otherwise nanoflux fetches the URL and parses it as
   RSS / Atom / JSON-feed with `gofeed`.

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

### Per-feed interval and adaptive cadence

- A new feed starts at a default interval (900s / 15m).
- When a poll brings **new items** and adaptive polling is enabled
  (`poll_interval_auto`, on by default), nanoflux derives the interval from the
  feed's **recent posting cadence**: it averages the gaps between the newest
  ~20 item timestamps and clamps the result to **[15m, 1 day]**. A feed that
  posts hourly is polled roughly hourly; a feed that posts monthly is polled
  around once a day. This is the "automatic adjusting of delay."
- If adaptive polling is turned off, the interval stays whatever the user set.

### Quiet (stale) feeds

If a feed's newest item is **older than 7 days**, its interval is forced to the
1-day maximum (regardless of adaptive polling) so a silent feed is not polled
often. This is why `last_item_at` is tracked separately from the poll time: "no
new posts in a while" is not an error, just a signal to slow down. At 30 days the
UI additionally marks the feed as possibly abandoned.

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

### Per-host pacing and fair rotation

Feeds are grouped by **registrable host** (e.g. all `reddit.com` feeds are one
group). Two rules keep a host from being hammered:

- **One host at a time.** Feeds in a group are fetched sequentially, while
  different hosts run in parallel up to `NF_POLL_WORKERS`. This prevents
  bursting a single site.
- **Learned window.** When a host rate-limits a fetch, the poller remembers how
  long it asked to be left alone (`hostWindow`, floored at **60 seconds**) and
  records the next time the host may be hit (`hostNextHit`). Until that time,
  the host's remaining feeds are **not fetched** — but they stay **due**, so
  they are picked up the moment the window clears. A host that never rate-limits
  is never paced.

Within a group, feeds are processed **least-recently-polled first**. So if a
host can only be hit once per minute and there are twenty of its feeds, they
rotate: each feed takes its turn rather than a few winning every cycle.

### Waking at the right time

Because a paced host must be revisited promptly, the poller does not simply sleep
for the base interval. After each pass it wakes at the **earliest** of:

- the base interval (`NF_POLL_INTERVAL`), or
- the next moment a paced host becomes hittable,

floored at 30 seconds so a just-cleared backoff cannot make the loop spin.

The result: a host with, say, twenty feeds and a 1-minute window refreshes them
all across roughly twenty minutes, continuously, instead of one per day.

### The manual refresh button

The refresh button and its handler respect `next_poll_at`. While a feed is
cooling, the row shows a **"rate limited · retry in …"** badge and the button is
disabled; a click during the window is a no-op rather than another request.

### Errors are explained

If discovery or a host-specific rule probe hits a limit, that reason is surfaced
to the user (e.g. *"the site is rate-limiting requests (HTTP 429) — wait a bit
and try again"*) instead of a misleading "no feed found". Raw fetch URLs never
leak into user-facing messages.

### Plugins

External and native plugins never see raw networking. Their `Host.Do` calls go
through the host, which applies the same User-Agent and timeout policy and
inspects every response for limits, cooling the request host. A plugin that
cannot avoid a limit returns a `RateLimit`, and the poller applies exactly the
backoff described above.

### When a plugin goes missing

A feed served by a plugin records the owning plugin's name (`feeds.plugin_name`).
At startup the plugin system reconciles stored feeds against the plugins that
actually loaded:

- A feed whose plugin is **loaded** is adopted (or kept), and if it had been
  parked for a missing plugin it is **auto-re-enabled**.
- A feed whose plugin is **not loaded** is **auto-disabled** with a reason
  (`plugin not loaded: <name>`), so it stops retrying a URL nothing can fetch.
  The row shows a `disabled · plugin not loaded: …` badge.
- Re-enabling is keyed to that exact reason, so a feed the user **paused by
  hand** is never silently resumed. A user save on the feed edit form also
  clears any automatic reason.
- Re-enable is keyed to the plugin the feed is **adopted by** on that boot, not
  the stale stored name. A plugin that was renamed or replaced (its name changed
  but it still matches the same URLs) therefore resumes its parked feeds too.

This means removing a plugin parks its feeds instead of leaving them failing
forever, and restoring the plugin brings them back automatically.

### Resetting a domain (escape hatch)

The admin page's **plugins** card lists the domains a plugin currently owns,
each with a **reset** button. Reset clears `plugin_name` for every feed on that
registrable domain and re-runs the reconcile pass, so the domain is re-owned by
whichever loaded plugin matches it now — the way to detach a domain after a
plugin is removed, renamed, or swapped for another. Feeds parked for a missing
plugin are re-enabled; a feed the user paused stays paused.

> Routing at fetch time is by URL shape, not by `feeds.plugin_name`. If the
> plugin is still loaded and matches the domain, a reset re-adopts it. The reset
> truly frees a domain to the generic parser only when no loaded plugin matches
> it (the plugin was removed, renamed, or another plugin now wins precedence).

## Failures and their effects

| Outcome | What nanoflux does |
| --- | --- |
| `304 Not Modified` | Records the poll time, clears backoff, stores nothing |
| Success with items | Stores new items, records `ETag`/`Last-Modified`, clears backoff, adjusts the interval |
| Rate limit (`429` / hinted `503`) | Sets `next_poll_at`, learns the host window, records a `last_error`; the feed is not treated as broken |
| Other HTTP error (`4xx`/`5xx`) | Records `last_error`; retried on the feed's normal interval |
| Parse error (not a feed) | Records `last_error`; retried on the normal interval |

`last_error` is shown as a **"last poll failed"** badge on the feed row and the
feed page. It is distinct from the amber **stale** badge ("no new posts in a
while"), which is not a failure.

## Pagination ("load older items")

A feed may advertise a next page via a feed-level `rel="next"` link or a
`page=`-style query. The first poll records that cursor. The feed page offers a
**"load older items"** button which walks up to 5 pages per click (bounded so an
unbounded history cannot stall the request) until the history is exhausted.
Routine polls never touch the cursor, so they cannot clobber an in-progress
backfill.

## Configuration

| Variable | Default | Effect |
| --- | --- | --- |
| `NF_POLL_INTERVAL` | `15m` | The poller's base wake interval (a ceiling; dynamic wakes can be sooner) |
| `NF_POLL_WORKERS` | `4` | Concurrent fetches across *distinct hosts*; a single host's feeds are serial |
| `NF_USER_AGENT` | `nanoflux (<repo URL>)` | Outbound User-Agent for fetching and discovery; set a contact URL for a large instance |

Per-feed interval and adaptive polling are set in the feed edit form, not by env
var.

## Where this lives in the code

| Concern | File |
| --- | --- |
| Background loop, per-host pacing, rotation, dynamic wake | `internal/poller/poller.go` |
| Adaptive interval, stale thresholds | `internal/poller/interval.go` |
| Rate-limit detection, `Retry-After` / `x-ratelimit-reset` parsing | `internal/feedparse/ratelimit.go` |
| Generic fetch: conditional GET, parse, pagination cursor | `internal/feedparse/feedparse.go` |
| Host-mediated HTTP + rate-limit cooling for plugins | `internal/plugin/host.go`, `internal/plugin/cooldown.go` |
| `next_poll_at` storage and `ListFeedsDue` | `internal/store/feed.go`, `internal/store/queries/feeds.sql` |
