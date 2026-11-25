# Feeds are a library, authors select them

Status: 2026-11-25. Implemented in schemaV71. This replaces the earlier
owner-pointer model (`feeds.author_id`).

## Model

```
users               account, timezone, retention defaults
authors             profile, rules, is_aggregator, auto-read/delete windows
feeds               library row: url, poll state, plugin, rank, cache_images
author_feeds        author_id, feed_id, created_at   (many-to-many)
saved_views         author_id, filters                (author lens over all feeds)
collections         -> collection_authors -> authors
items               owner feed + item_feeds, one shared read flag per item
item_claims         item_id, author_id                (derived, rebuildable)
```

An author's page scope is the union of:

1. items reached through its `author_feeds` membership, with the author's
   allow/block rules applied, and
2. items matching any of its `saved_views`, with the same author rules applied.

A collection is the union of its member authors' scopes.

The global read scope (`visible_items`) is the union of every author's
**member-feed** slice plus items on feeds no author selected. Saved views stay
author-page lenses and do not widen the global scope.

## Claims and aggregators

`item_claims` is derived data: the items a **claiming** author owns.

- `authors.is_aggregator = 0` (the default for new authors) means the author
  claims its items.
- `is_aggregator = 1` means the author does not claim, and its page hides items
  claimed by other authors.
- A claim is built from the author's member feeds (with rules) plus its views.
- Rebuilt on ingest, view save, rule save, membership change, and the
  aggregator toggle. `nanoflux claims rebuild` repairs it.

Backfill in schemaV71: every existing author starts as an aggregator, then any
author with at least one view becomes a claimer. Filter rules are deliberately
ignored because aggregators are the authors that carried those rules.

## Read state and retention

Read state is shared per item: reading a post anywhere marks it read everywhere.

- **Auto-read** sweeps each author's displayed slice with that author's
  effective window (override, else the account window). An item on a member feed
  is never swept by the account-wide residual pass, so an author with auto-read
  off is protected.
- **Auto-delete** uses the maximum window across the item's member authors and
  claiming authors. A window of `0` blocks deletion. Items on feeds with no
  members use the account window. Lists, favorites, bookmarks and saved pages
  are never deleted.

## Attribution

- Author page: the page author.
- Collection or global list: a claiming author whose claim includes the item,
  else the earliest member author (by `author_feeds.created_at`).
- `/feeds/{id}`: the feed. The feed page reads raw items, so filters do not hide
  entries from the library view.
- Unassigned feed: the feed.
- Plugin decoration still wins when present.

## Add flow

A canonical URL already in the library is the same feed. Adding it for another
author attaches membership instead of creating a duplicate. This applies to the
web add form, the extension, the JSON API, OPML and plugin provisioning. OPML
reuses an existing author by name and attaches existing feeds.

## UI

- Author edit: the **feeds** card selects library feeds; the **Aggregator feed**
  checkbox toggles `is_aggregator`.
- The author `sources` accordion lists member feeds and views.
- The feed editor's change-author action adds another author; it never removes
  the existing membership.
- `/feeds` is the library, `/feeds/{id}` its raw item list.

## Invariants

- `author_feeds` never includes the system feed or system author.
- Deleting an author removes only its memberships; feeds and items survive.
- `visible_items` is `SELECT i.*`, so any new `items` column requires recreating
  the view in the same migration.
- `visible_items` and `authorRulesVisibleSQL` must stay in sync.
- The author and collection count caches remain TTL-only; do not add
  invalidation hooks for read toggles or rule edits.
- Claims are derived; never treat `item_claims` as the source of truth.

## Not done yet

- A one-click "promote this poster" action from an item.
- Folding `saved_views` into a unified author-source table. The read path is
  already source-shaped (member feeds plus views), so this is a data/UI
  migration, not a read-path rewrite.
- Applying an aggregator's unclaimed-only rule inside collections. It is an
  author-page display rule today.
