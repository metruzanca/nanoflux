# Performance

nanoflux is expected to run on small self-hosted hardware — a Raspberry Pi
Zero W (single-core ARMv6, 512 MB) is the benchmark target. This note records
how to measure it and the known hot paths.

## Measuring latency

`scripts/bench.sh` samples page latency (and optionally host load) against a
running instance:

```sh
BENCH_URL=http://localhost:20310 BENCH_STATS=1 scripts/bench.sh
# authenticated pages:
BENCH_USER=you BENCH_PASS=... scripts/bench.sh
```

On a Pi, copy the script over and run it there (the host port is `20310`).

## Profiling CPU/heap

The server serves `net/http/pprof` when `NF_PPROF_ADDR` is set. It exposes
process internals, so bind it to loopback:

```sh
NF_PPROF_ADDR=127.0.0.1:6060
```

Capture a profile while exercising the slow path. With the container run from a
clean shell:

```sh
docker exec nanoflux-nanoflux-1 \
  wget -qO- 'http://127.0.0.1:6060/debug/pprof/profile?seconds=30' > cpu.pprof
go tool pprof -top -cum cpu.pprof
```

If the host's login shell prints a banner on non-interactive sessions (a common
`fish` setup), a `cat`/`scp` of the profile picks up those bytes and pprof
reports "unrecognized profile format". Base64-encode on the far side and take
the payload line:

```sh
ssh host 'base64 -w0 /tmp/cpu.pprof' | tail -n1 | base64 -d > cpu.pprof
```

Also useful: `/debug/pprof/heap` for memory and `/debug/pprof/goroutine?debug=2`
for blocked goroutines.

## What was slow

A single `GET /` on the Pi spent ~70% of its CPU inside SQLite. The cause was
missing indexes: `items` was only indexed on `feed_id`, but every user-scoped
query filters on `user_id`, so counts and lists full-scanned the table. The
nav-count middleware runs `CountUnreadItems` on *every* request, which is what
turned an empty page into seconds of work. `schemaV51` adds owner-scoped
indexes; `GET /` dropped from ~2-4 s to ~0.1 s.

## Author index counts

`GET /authors` used to run the per-author unread aggregate twice (once in the
nav-count middleware, once in the handler), plus a scoped `UNION ALL` count for
view-bearing authors, plus one feed-list query per saved view. On the 20 authors
x 1000 items fixture that is roughly 3 ms for the author list, 10 ms for the
grouped unread count and 17 ms for the view scope, before a Pi Zero's 20-50x
penalty.

The index now caches the per-author unread map per user for 60 seconds
(`httpapi.authorUnreadCache`), so a warm page costs one
`ListAuthorsWithFeedCount` and two small queries. Counts may lag a read toggle
by up to a minute by design; feed counts, view counts, avatars and the taste
signal stay live. The view path also loads the feed list once per request
instead of once per view.

## Tuning knobs for constrained hosts

- `NF_POLL_WORKERS=1` — one fetch at a time on one core.
- `NF_POLL_INTERVAL` / `NF_POLL_HOST_SPACING` — space fetches out so the poller
  does not keep the core busy.
- `NF_POLL_MIN_INTERVAL` (default `1h`) — the smallest automatic per-feed
  interval; a longer value means fewer requests per day.
- `GOMEMLIMIT` — cap the Go heap so the server cannot swap on a low-RAM box.
- `NF_DB_MAX_CONNS` — SQLite connection pool size (default 4).

See the README and `docker-compose.yml` for a full Pi compose profile.
