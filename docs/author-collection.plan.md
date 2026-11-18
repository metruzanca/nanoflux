# Feeds are a library, authors are lenses

Status: design agreed, decisions below must be confirmed before implementation.
Supersedes the ownership parts of `author-collection.plan.md`; that plan's
collection-by-author membership is absorbed here.

## The problem

`feeds.author_id` carries two unrelated responsibilities:

1. Polling identity: one URL, one poll state, one plugin owner, one cache folder.
2. Presentation and filtering identity: whose page the items appear on, whose
   always-on rules apply, which auto-read window applies, how momentum groups
   items, what the source line says.

`saved_views` exists as the escape hatch for "a second filter lens over feeds I
do not own". The read path then reconciles owner rules, page-author rules and
view-author rules through `visible_items`, `authorScopeClause`,
`viewFilterClause`, `authorRulesVisibleSQL`, `visibleItemExists` and
`collectionScopeClause`. That reconciliation is the abstraction leaking.

## Target model

Feeds are the library. Authors are lenses. Collections group lenses.

```
feeds               user_id, url, poll state, plugin, rank, cache_images
                    (no author_id)
authors             user_id, name, profile, filter rules
author_feeds        author_id, feed_id             -- explicit selection
collections         user_id, name, is_auto
collection_authors  collection_id, author_id
items               unchanged: owner feed (items.feed_id) + item_feeds
```

An author's item scope is: items reachable through its selected feeds, then that
author's filter rules. No ownership join, no view branches, no special handling
for cross-feed posts.

This deletes:

- `saved_views`, `collection_views`, `ViewFilter`, and every view query path.
- `authorScopeClause` OR branches (becomes a membership predicate).
- `collectionScopeClause` feed/view union (becomes a union of author scopes).
- `visible_items` as the global correctness backbone.
- `authorRulesVisibleSQL`, `visibleItemExists`, and the raw-items vs view-scope
  split in most call sites.
- `SetFeedAuthor`, the feed "move to another author" dialog, and the ownership
  checks in feed create/edit/delete.

The subreddit case: one `r/cats` feed; author `u/alice` selects it with a
`u/alice` tag rule; author `u/bob` selects it with a `u/bob` rule; optionally an
author `r/cats` selects it unfiltered. One poll, one item row, shared read state.

## Decisions (recommended defaults)

Record the final answer next to each before starting.

1. **One lens per author.** An author has exactly one feed selection and one
   filter recipe. This is what actually removes views. Multi-lens authors are
   migrated into separate authors grouped by a collection.
   - Alternative: an author profile may hold several lens branches. That keeps a
     `saved_views`-shaped sub-entity and does not remove the abstraction.
2. **Author filters are page-local.** They shape that author's lists only.
   Global library lists (`/unread`, `/search`, stats) show the library without
   author rules. Feed-level default rules are deferred; if "block this tag
   everywhere" is later needed, it belongs on the feed or the user, not an
   author.
   - Alternative: keep owner-derived global visibility, which keeps most of
     today's complexity and adds a join table.
3. **Auto-read moves off authors.** `items.read` is shared per item, so two
   authors including the same feed with different windows have no unambiguous
   answer. Move the window to the feed (`feeds.auto_read_after_days`) with the
   user window as fallback, and drop `authors.auto_read_after_days`.
   - Alternative: effective window is the minimum positive window across
     authors that include the item. Deterministic but surprising.
4. **Momentum groups by feed.** `feeds.author_id` disappears, so "small authors
   first" has no owner to group by. Group the backlog by feed and adjust the
   hint. Only offer momentum where a feed backlog is meaningful.
5. **Collections group authors only.** Never mix feeds and authors in one
   collection, or the union complexity returns. The library and `/feeds` cover
   raw feeds; an auto collection can list authors not in a user collection.
6. **Attribution becomes scope-derived.** There is no owner author. On an author
   page, the row's source is that author/lens; on global lists, the feed plus
   any plugin decoration. An aggregator feed no longer falsely attributes every
   post to one author.

## Schema and migration

Split into small migrations so each step can ship green. Numbering starts after
the latest (`schemaV67` recreates `visible_items`).

### V68: author_feeds

```sql
CREATE TABLE author_feeds (
    author_id INTEGER NOT NULL REFERENCES authors(id) ON DELETE CASCADE,
    feed_id   INTEGER NOT NULL REFERENCES feeds(id) ON DELETE CASCADE,
    PRIMARY KEY (author_id, feed_id)
);
CREATE INDEX idx_author_feeds_feed ON author_feeds(feed_id);
INSERT OR IGNORE INTO author_feeds (author_id, feed_id)
SELECT author_id, id FROM feeds WHERE author_id IS NOT NULL;
```

Keep `feeds.author_id` for now. This is the compatibility stage: writes touch
both, reads can move to membership first.

### V69: collections group authors

```sql
CREATE TABLE collection_authors (
    collection_id INTEGER NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
    author_id     INTEGER NOT NULL REFERENCES authors(id) ON DELETE CASCADE,
    PRIMARY KEY (collection_id, author_id)
);
INSERT OR IGNORE INTO collection_authors (collection_id, author_id)
SELECT DISTINCT cf.collection_id, f.author_id
FROM collection_feeds cf JOIN feeds f ON f.id = cf.feed_id;

INSERT OR IGNORE INTO collection_authors (collection_id, author_id)
SELECT cv.collection_id, v.author_id
FROM collection_views cv JOIN saved_views v ON v.id = cv.view_id;

DROP TABLE collection_feeds;
DROP TABLE collection_views;
```

This matches the intended widening in `author-collection.plan.md`: a collection
holding one feed of an author now holds that author, so all of that author's
lenses come with them. Existing collection splits by feed are lost; the
replacement is an author-level lens or a separate collection.

### V70: views become authors

For each saved view, create a lens that preserves it exactly:

```sql
-- One author per view, unless the view's author has no feeds and no other views.
INSERT INTO authors (user_id, name, description)
SELECT v.user_id, v.title, 'imported saved view' FROM saved_views v;

INSERT OR IGNORE INTO author_feeds (author_id, feed_id)
SELECT <new author id>, f.id
FROM feeds f WHERE f.user_id = <view user> AND f.is_system = 0;

-- Copy v.filters into author_filter_rules / author_filter_values.
```

Because a view currently matches all feeds, the faithful translation is a new
author selecting every feed with the view's filters. Do **not** fold the filter
into the view's assigned author: that author's own feeds are currently
unfiltered, so folding would start hiding items. Copy collections membership
(`collection_authors`) from the view's assigned author only if the assigned
author had no feeds; otherwise the new author inherits the view's collection
members.

Then `DROP TABLE saved_views;`.

### V71: drop feeds.author_id

SQLite needs a table rebuild to drop a column that participates in indexes and
FKs. Recreate `feeds` without `author_id`, copy rows, recreate indexes. Update
`item_feeds`/`items` FKs unchanged (they point at `feeds.id`).

Also remove the system-author concept: keep `feeds.is_system`, drop
`authors.is_system` and the `EnsureSystemAuthor` path. `EnsureSystemFeed` stops
needing an author. Saved-page items keep surfacing only in lists, favorites and
bookmarks exactly as today.

### V72: visible_items becomes identity

Once every read path is scope-aware, replace the view with:

```sql
DROP VIEW visible_items;
CREATE VIEW visible_items AS SELECT * FROM items;
```

Keep the name temporarily for call sites, then delete it in a follow-up. Or
drop it outright in the same migration once no query selects from it. Feed-level
default rules, if added later, get their own predicate at the chokepoint rather
than resurrecting owner-based visibility.

### Filter rules

`author_filter_rules` stays as the per-lens rule set. `kind='author'` (just
added for owner-based matching) is redefined to match `author_feeds` of the
referenced author instead of `feeds.author_id`. Author delete removes that id
from other authors' `kind='author'` rows; feed delete removes it from
`kind='feed'` rows. Keep the existing non-collision rule.

## Store and query rewrite

This is the bulk of the work.

### `internal/store/queries/feeds.sql`

- Remove `author_id` from every column list and from `CreateFeed`,
  `UpdateFeed`, `CreateSystemFeed`.
- Delete `ListFeedsByAuthor`, `ListFeedsByAuthorWithUnread`, `SetFeedAuthor`.
- Add:
  - `ListFeedsForAuthor` joined through `author_feeds`.
  - `ListAuthorsForFeed` (feed library page) and a count.
  - `ListFeedsWithAuthorCount`.
- `ListFeedsWithUnread`: unread is items in the feed; no owner rules.

### `internal/store/queries/items.sql`

- Every `LEFT JOIN authors a ON a.id = f.author_id` goes. `ListItems` no longer
  has an author join.
- `authorID` matching becomes:

  ```sql
  EXISTS (SELECT 1 FROM item_feeds mf JOIN author_feeds af ON af.feed_id = mf.feed_id
          WHERE mf.item_id = i.id AND af.author_id = ?)
  ```

- `collectionID` matching becomes the union of member authors:

  ```sql
  EXISTS (SELECT 1 FROM item_feeds mf
          JOIN author_feeds af ON af.feed_id = mf.feed_id
          JOIN collection_authors ca ON ca.author_id = af.author_id
          WHERE mf.item_id = i.id AND ca.collection_id = ?)
  ```

- Select from `items`, not `visible_items`. Author rules are applied as an
  explicit predicate only on author-scoped reads.
- `ListItemSourcesForItems` keeps feed membership and joins `author_feeds`
  instead of `f.author_id`, so a source can carry all authors that include it.

### `internal/store/item.go`

- `ItemFilter`: remove `OwnerAuthorID`, `Views`, `CollectionViews`, `ViewFilter`.
  Keep `AuthorID` and `CollectionID` with membership semantics.
- Replace `authorScopeClause` with one predicate:
  membership through `author_feeds` AND the author's rules
  (`authorRulesVisibleSQL(itemID, "?")`).
- Replace `collectionScopeClause` with an `EXISTS` over
  `collection_authors -> author_feeds`, with each member author's rules applied.
  If SQL per-author rules get unwieldy, resolve member author ids first and
  build an OR of per-author predicates in Go, like today's views array.
- Delete `viewFilterClause`, `visibleItemExists`, and the raw-base split.
- `ItemWithFeed.AuthorID/AuthorName`: no longer a join result. The handler sets
  the author context for an author-scoped list; global lists leave it zero. The
  card source line uses the scope author, else the decoration, else the feed.
- `ItemSource`: add the authors that include the feed, so "also in" stays
  meaningful.

### Other stores

- `feed.go`: `Create` takes no author. `Delete` no longer has an owner to
  re-home around (item_feeds re-homing is unchanged). Delete the move-feed
  helper.
- `author.go`: add `SetFeeds(authorID, feedIDs)` (transactional replace),
  `Feeds(authorID)`, `AuthorsForFeed(feedID)`, and
  `DeleteOrphans(userID)`/a reference-count check for the convenience delete.
- `collection.go`: membership queries move to `collection_authors`. No-collection
  sync inserts authors with no user collection, not feeds.
- `savedview.go`: delete the file and its tests.
- `stats.go`: reads `items`; group by owner feed as today. Remove rule joins.
- `normalize.go`: normalize rules unchanged. Remove saved-view normalization.
- `authorfilter.go`: `kind='author'` matches `author_feeds`.
- `saved.go`: `EnsureSystemFeed` no longer creates a system author.

### Auto-read

- Add `feeds.auto_read_after_days` (nullable, inherits `users.auto_read_after_days`).
- Migrate each author's window onto its selected feeds, taking the smallest
  non-zero window when a feed has several authors; an author with `0` and no
  other author keeps `0`.
- `MarkAutoRead` evaluates the feed window per item (items.feed_id), not an
  author join. `MarkAutoReadForFeed` replaces `MarkAutoReadForAuthor`.
- Author edit page loses the auto-read card. The feed panel gains it.

### Counts, caches, sorts

- `ListAuthorsWithFeedCount`: `feed_count` via `author_feeds`; `max_rank` and
  `fav_count` via `author_feeds -> feeds`.
- `UnreadCountsByAuthor`: count items in `author_feeds` under each author's
  rules. Keep the author-unread cache and its TTL-only behavior.
- Collection counts: union of member author scopes. Keep
  `collectionCountsCache`; replace the counts-by-feed fast path with
  counts-by-author.
- Momentum: `COUNT(*) OVER (PARTITION BY mf.feed_id)` or a resolved feed-block
  query, not `f.author_id`.
- Magic: unchanged, `feeds.rank` is still a feed property.

## HTTP and UI

### Routes

- Re-add `GET /feeds` (library index) and `GET /feeds/{id}` (feed page).
  The feed page carries: poll state, refresh, load older, mark all read, edit,
  plugin panel, delete, and "authors (N)".
- Keep `/feeds/{id}/edit` as a real page or fragment again.
- Author edit: replace the sources accordion's feed panels with a feed
  multi-select (`combo` component), plus the filter card. No add-feed form on
  the author page, since feeds are library members. Keep "new author" and
  "new feed" in the add menu.
- Delete saved-view routes, handlers, templates, the command-palette entity and
  tests: `GET /views/new`, `POST /views`, `GET /views/{id}/edit-fragment`,
  `POST /views/{id}/edit`, `POST /views/{id}/delete`.
- Collection edit: author multi-select instead of feed/view pickers. Collection
  page sources are author cards.

### Flows

- **Add feed**: URL -> preview -> "create an author for this feed" checked by
  default, name prefilled from the title. Unchecked adds to the library only.
- **Add author**: name, feed picker, filter rows. Can create a feed inline.
- **Author from a feed filter**: pick a feed, add a tag/author filter, name the
  author. This is the subreddit-user shortcut.
- **Delete author**: if it is the last author selecting a feed, offer to delete
  the feed too (explicit opt-in, `type=button` action). Shared feeds are never
  deleted automatically.
- **Delete feed**: removes it from every author's lens (cascade), no author
  deletion. Show "used by N authors" in the confirmation.
- **Remove a feed from an author**: membership edit, no item deletion.

### Templates and JS

- `views_feeds.templ`: feed cards become library cards; author cards lose the
  ownership framing.
- `views_items.templ`: source line uses scope author/decoration.
- `views_collections.templ`: author cards.
- `views_authors.templ` (or equivalent): feed picker in the edit page; remove
  views.
- Remove view templates, `savedviews.go`, dialog wiring and command-palette
  entries.
- Feed delete dialog: replace the remote-only opt-in with remote plus "also
  delete the feed if no other author uses it".
- Author delete dialog: name the author and offer the orphan-feed cleanup.

### OPML

Feeds are the library, so OPML export stays feed-based. Do not emit one feed per
author, which would duplicate. Decide grouping: flat, or grouped by the first
collection/author, and document it.

## Phased delivery (each phase ships and commits)

Project rules: commit each built plan, conventional commits, update
`.env.example` if any env var is added (none expected), do not mention external
plugins in internal docs.

1. **Membership stage (V68).** Add `author_feeds`, dual-write on feed create/edit,
   move author scope and counts to membership while `feeds.author_id` remains a
   compatibility column. Author scope is now selectable before ownership is
   removed. Add `/feeds` library and the add-feed create-author flow. Tests:
   author scope, counts, caches.
2. **Collections by author (V69).** Replace `collection_feeds`/`collection_views`
   with `collection_authors`. Update collection queries, counts, caches, UI,
   OPML, refresh-all (refresh member authors' feeds, deduped).
3. **Flatten views (V70).** Migrate saved views to lens authors, remove view
   routes/stores/templates/filters, remove `ViewFilter` from `ItemFilter`.
   Update the filter dialog (drop the "save as view" chip) and the command
   palette.
4. **Drop ownership (V71).** Rebuild `feeds` without `author_id`; remove
   `SetFeedAuthor`, owner joins, the system author; switch auto-read to feeds;
   switch momentum to feed blocks.
5. **Simplify visibility (V72).** Remove `visible_items` usage from read paths,
   reduce or drop the view, delete `authorRulesVisibleSQL`'s global role, and
   update stats/search/history/list/share reads.
6. **Docs and dead code.** Update `AGENTS.md` sections: author filters, feeds
   live on the author page (now a feed library page exists), saved views
   (removed), collections, auto-read, momentum, cross-feed/attribution, stats,
   image caching wording, security invariants. Delete obsolete tests and
   migrations comments that describe ownership.

## Open questions

1. One lens per author, or multi-lens profiles? (Recommended: one lens.)
2. Author filters page-local, or a global rule source? (Recommended: page-local.)
3. Auto-read on feeds, or min-window across authors? (Recommended: feeds.)
4. Momentum groups by feed, or is author-page only? (Recommended: feed.)
5. Does a collection with no authors and only filters mean "all authors"
   (inherited from `author-collection.plan.md`)?
6. OPML grouping for author-based collections: flat or first-group.

## Risks

- **Read-path blast radius.** `visible_items` is used by lists, counts, badges,
  history, list items, shares and stats. Doing phases 1 and 5 without a
  per-phase test sweep will break counts subtly. Add a test per read path for an
  author-scoped and a global read before removing the owner join.
- **Shared read state.** Cross-lens items share `items.read`. A mark-all or
  auto-read on one lens affects others. This is already true for cross-feed
  items, but the model makes it common. Document it.
- **Migration duplication.** Flattening views can produce many authors. Group
  them into a collection during migration to keep the UI usable.
- **Feed deletion.** Feeds no longer disappear with their author. Orphan cleanup
  must be explicit, never automatic, and never for a feed another author uses.
