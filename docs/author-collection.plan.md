# Author-centred collections: implementation plan

Status: planned, not implemented.

Source design: `/home/metru/.opencode/plan/author-collection.plan.md`.

A collection becomes a group of authors rather than a group of feeds. Adding an
author brings that author's feeds and views. Collections can carry their own
always-on tag/feed filters. Auto collections stay feed-based and are relabelled
"sites" in the UI.

## Locked decisions

### All-authors state

A user collection with no member authors:

- with at least one filter row: scope is every item (`visible_items`, so the
  owner author's rules still apply), narrowed by the collection's fixed filters.
- with no filter rows: renders empty, with a hint to add an author or a filter.
  No item read happens; unread/read are `0`.

The synthetic no-collection stays author-membership based. It can be empty and
never becomes all-authors, so it cannot turn into a duplicate `/unread`.

Removing the last author from a filtered collection turns it into an
all-authors collection. Removing the last author and the filters leaves it
empty.

Auto (site) collections are unchanged: feed membership, no authors, no fixed
filters.

### Filter storage

`collections.filters TEXT NOT NULL DEFAULT '[]'` holding the canonical saved-view
`?f=` params (the same encoding as `saved_views.filters`).

- Validation reuses the saved-view filter parser, but drops time rows and feed
  ids outside the collection's resolved feed scope (all feeds for an
  all-authors collection).
- Stale feed ids degrade silently on load, so feed/author deletion needs no
  cleanup.
- The fixed chips on the collection page and the edit-page preview read the same
  parsed list.
- No `collection_filter_rules` / `collection_filter_values` tables, no new
  store, no `Store.NormalizeData` change.

## Current state

- Membership is `collection_feeds` for user and auto collections, plus
  `collection_views` for views.
- Item scope is `ItemFilter{CollectionID, CollectionViews}`
  (`internal/store/item.go`), with `collectionScopeClause` building
  `collection_feeds` OR view branches.
- Counts, tag options, home sections, the API and refresh all each re-derive the
  scope in the HTTP layer (`collectionViewFilters`,
  `collectionViewMatches`, `applyCollectionViewCounts`).
- Feeds carry collection pickers (`feedFields`), views carry collection pickers
  (`viewEditorBody`), and feed cards render collection chips.

## Target model

```go
// internal/store/item.go
type CollectionScope struct {
    ID         int64
    ByFeeds    bool          // auto site collection: collection_feeds
    ByAuthors  bool          // user / no-collection: collection_authors
    AllAuthors bool          // user collection with zero members, filters only
    Authors    []int64
    Feeds      []int64       // resolved for ByFeeds (display + refresh)
    Views      []ViewFilter  // member authors' views
    TagGroups  []TagGroup    // collection fixed filters
    FeedGroups []FeedGroup
}

type ItemFilter struct {
    // ...
    Collection *CollectionScope // nil = no collection scope
}
```

`ItemFilter.CollectionID` and `ItemFilter.CollectionViews` are removed.

`itemSource()` returns raw `items` when a scope has authors or views, so the
membership branch can apply each member author's always-on rules (same semantics
as an author page). Site collections keep `visible_items` (owner-author rules).
All-authors scopes read `visible_items` with only the fixed filters applied.

`appendItemFilterPredicates` appends the scope's `TagGroups`/`FeedGroups`, so the
list, counts, tab counts, tag options, home and API all get the fixed filters
automatically. This deliberately avoids the `filterScope` overwrite trap.

## Phase 1: Schema and migration

Files: `internal/db/migrate.go`, `internal/store/schema.sql`,
`internal/db/db_test.go`, `internal/store/schema_test.go` (auto-guard).

New `schemaV67`:

```sql
CREATE TABLE collection_authors (
    collection_id INTEGER NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
    author_id     INTEGER NOT NULL REFERENCES authors(id) ON DELETE CASCADE,
    PRIMARY KEY (collection_id, author_id)
);
CREATE INDEX idx_collection_authors_author ON collection_authors(author_id);

ALTER TABLE collections ADD COLUMN filters TEXT NOT NULL DEFAULT '[]';

INSERT OR IGNORE INTO collection_authors (collection_id, author_id)
SELECT DISTINCT cf.collection_id, f.author_id
FROM collection_feeds cf
JOIN collections c ON c.id = cf.collection_id AND c.is_auto = 0
JOIN feeds f ON f.id = cf.feed_id;

INSERT OR IGNORE INTO collection_authors (collection_id, author_id)
SELECT DISTINCT cv.collection_id, v.author_id
FROM collection_views cv
JOIN collections c ON c.id = cv.collection_id AND c.is_auto = 0
JOIN saved_views v ON v.id = cv.view_id;

DELETE FROM collection_feeds
WHERE collection_id IN (SELECT id FROM collections WHERE is_auto = 0);

DROP TABLE collection_views;
```

`collection_feeds` stays, but only for auto/site collections. Update
`db_test.go`'s table list: add `collection_authors`, remove `collection_views`.

## Phase 2: Store membership

Files: `internal/store/collection.go`,
`internal/store/queries/collections.sql` (regenerate with `mise run gen`).

Add to `CollectionStore`:

- `Authors(userID, collectionID) ([]Author, error)`
- `AddAuthor(userID, collectionID, authorID) error`
- `RemoveAuthor(userID, collectionID, authorID) error`
- `SetAuthors(userID, collectionID, ids []int64) error`
- `AuthorCollectionIDs(userID, authorID) ([]int64, error)`
- `CollectionsByAuthor(userID) (map[int64][]Collection, error)`
- `SetAuthorCollections(userID, authorID, ids []int64) error`
- `AuthorFeeds(userID, collectionID) ([]Feed, error)` (feeds of member authors)
- `SetFilters(userID, id int64, params []string) error`
- `Filters []string` on the `Collection` struct

`SyncNoCollection` becomes author-based: every non-system author not in a
non-auto collection, via `AddAuthorsWithoutUserCollection`.

Keep `EnsureAuto`, `AssignAuto`, `UnassignAuto`, `AddFeed`, `RemoveFeed`,
`Feeds`, but mark them site-only in comments.

`ListWithCounts` returns `AuthorCount` and `FeedCount` (site), drops `ViewCount`
and the unread/read subqueries. Unread/read are computed in HTTP through the
resolved scope, which is the only way to include views and fixed filters
consistently.

Remove:

- `FeedCollectionIDs`
- `UserCollectionIDsByFeed`
- `CollectionsByAuthorFeed`
- `ListCollectionsForAuthorFeeds`
- `CollectionIDsForFeed`
- `ListUserFeedCollectionIDs`
- `AddFeedsWithoutUserCollection`
- `listFeedsInCollection` stays for site collections; site feed mutations stay

`savedview.go`: remove `ListByCollection`, `CollectionIDs`, `SetCollections`,
`CollectionsByView`.

## Phase 3: Store read scope

Files: `internal/store/item.go`, `internal/store/queries/items.sql`,
`internal/store/sqlcgen/*` (generated).

- Replace `CollectionID`/`CollectionViews` with `Collection *CollectionScope`.
- `itemSource()`: raw `items` when `AuthorID != 0`, `Views` non-empty, or
  `Collection.Authors`/`Collection.Views` non-empty.
- `ListPage` routes every read with `f.Collection != nil` through
  `listPageTagged`.
- `collectionScopeClause` becomes:
  - ByFeeds: `EXISTS (... item_feeds mf WHERE mf.item_id = i.id AND
    mf.feed_id IN (...))`.
  - ByAuthors with members: `EXISTS (SELECT 1 FROM item_feeds mf JOIN feeds sf
    ON sf.id = mf.feed_id WHERE mf.item_id = i.id AND sf.author_id IN (...)
    AND (authorRulesVisibleSQL(i.id, 'sf.author_id')))`.
  - AllAuthors: no membership branch.
  - View branches through `viewFilterClause`.
- `appendItemFilterPredicates` also appends the scope's `TagGroups` /
  `FeedGroups`.
- Remove the collection branches and `collectionID` arg from generated
  `ListItems`, `ListItemsAsc`, `ListItemsMagic`.
- Remove `CountUnreadItemsByCollection`, `CountReadItemsByCollection`,
  `ListCategoriesForCollection`.
- Remove the collection case from `ListCategories`; collection readers use
  `ListCategoriesFilter`.
- Generalize the author filter preview into `FilterPreview` plus
  `PreviewCollectionFilters(userID, scope, tagGroups, feedGroups, limit)`. It
  evaluates the draft rows against the membership scope only, reads raw items,
  and computes the blocked sample as the complement. Exclude system feed items
  for all-authors previews.

## Phase 4: HTTP data layer

Files: `internal/httpapi/web.go`, `home.go`, `api.go`, `savedviews.go`,
`opml.go`, `server.go`, `combo.go`, `filters.go`.

One new resolver used everywhere:

```go
func (s *Server) collectionScope(userID int64, c store.Collection, tz string) collectionScopeData
```

It returns the collection, member authors (with feed/view counts), member feeds
(site feeds or author feeds), member views, parsed fixed rules, `AllAuthors`,
and the `store.CollectionScope`. It runs `SyncNoCollection` for the synthetic
collection, replacing the scattered calls.

Wire it through:

- `collectionScopedItems` mirrors `authorScopedItems`: `scopedFilter(...)` with
  the scope, `CountTabsFilter`, `ListCategoriesFilter`, `dedupItems`, cursor
  paging, plus `FixedFilters` and `FeedOptions`. Drop `withoutFeedFilters` so
  feed filters work on collection pages.
- `collectionDataFor`, `collectionPage`, `collectionEdit`, `collectionItems`,
  `collectionRefresh` use the resolver. Refresh queues member authors' feeds
  only, never feeds reachable only through a view.
- `annotateCollectionViews` takes the resolved scope instead of querying
  `collection_feeds`; view sources still work for view-only items.
- `collections` index and `applyCollectionViewCounts` become
  `applyCollectionCounts`, computing unread/read for every collection (site,
  author, all-authors) with `CountTabsFilter`, then sorting most-unread first.
- `homeSectionData` and `apiItems` resolve the scope and pass it; `apiItems`
  also verifies collection ownership.
- `collectionViewMatches` becomes "views assigned to member authors".

New routes:

- `POST /collections/{id}/add-author`
- `POST /collections/{id}/remove-author/{author_id}`
- `POST /collections/{id}/authors` (set from the edit page)
- `POST /collections/{id}/filters`
- `POST /collections/{id}/filters/preview`
- `POST /authors/{id}/collections`

Remove the handlers and routes:

- `collectionAddFeed`, `collectionRemoveFeed`
- `collectionAddView`, `collectionRemoveView`

Remove feed/view collection inputs:

- `feedForm.CollectionIDs`, `feedsData.Collections`
- `collectionIDsForFeed`
- the collection sync block in `applyFeedForm`
- `savedViewForm.CollectionIDs`, `savedViewForm.CollectionOptions`
- `savedViewCollectionIDs`
- collection memberships in `savedViewRows`

Author page: `Collections` comes from `CollectionsByAuthor`, not feed/view
memberships. Author edit gains a `collections` card with `CollectionOptions` /
`CollectionIDs` and its own apply endpoint.

OPML export groups each feed by its **author's** collections (site auto
collections excluded). OPML import adds the created author to the outline's
collection instead of the feed.

## Phase 5: Templates

Files: `views_collections.templ`, `views_items.templ`, `views_authors.templ`,
`views_feeds.templ`, `views_saved.templ`, `combo.go`.

`views_collections.templ`:

- Index: user cards show "N authors · M unread"; site cards keep "N feeds · M
  unread"; headings become "sites"; no-collection copy becomes "authors you
  have not put in a collection".
- Collection page: header has "+ add author" (dialog with an author
  multi-select, members excluded), no add feed/add view. The sources tab renders
  author cards (name, avatar, link to the author page, "X feeds · Y views",
  remove button) for user collections; site collections keep the read-only feed
  cards. All-authors collections get an explanatory line.
- Fixed filter chips render above the item list, read-only, alongside the
  transient funnel.
- Edit page: name form, authors multi-select with apply, filter card with live
  preview (copy the author filter card pattern), delete. Site collections keep
  the read-only feeds view.

`views_items.templ`:

- Replace `FeedCount+ViewCount` with a single `SourceCount`.
- Render author cards or feed cards for the sources tab.
- Keep `comboChips` working for site feeds.

`views_authors.templ`: new collections card; author page chips keep their look
but come from membership.

`views_feeds.templ`: remove the collections `comboMulti` and the feed-card
collection chips.

`views_saved.templ`: remove the collection picker and the view-card collection
chips.

`combo.go`: remove `removeFeedURL` / `removeViewURL`; add `removeAuthorURL`;
keep `nonAutoCollections` for the author picker.

## Phase 6: Cleanup, docs, tests

Remove unused helpers and queries, plus `firstGroupedFeedValue` if nothing else
uses it.

Docs:

- `AGENTS.md`: update the saved-view/collection sections, feed panel membership,
  refresh all, OPML grouping, and add a Collections section covering author
  scope, fixed filters, all-authors and "sites".
- `docs/vendored-web-components.md`: author chips instead of feed chips.
- `README.md` line 89.

Tests to rewrite or extend:

- `internal/store/store_test.go`: `TestListWithCounts`.
- `internal/db/db_test.go`: table list.
- `internal/httpapi/web_test.go`: collection suites, field flow, index stats,
  no-collection, add-author grouping, edit/delete.
- `internal/httpapi/savedviews_test.go`: view arriving through its author, view
  source attribution.
- `internal/httpapi/home_test.go`: pinned collection includes member views.
- `internal/httpapi/collection_refresh_test.go`: author-fed refresh.
- `internal/httpapi/autocol_test.go`: sites unchanged, feed edit has no
  collections control, OPML grouping.
- `internal/httpapi/authorfilter_test.go`: drop `SetCollections` setups.

New coverage:

- Author membership set from both sides.
- A view travels with its author.
- Collection filters narrow author items (for example `allow type:text`).
- All-authors with filters, and empty with none.
- An author with views but no feeds.
- Site collections unchanged.
- No-collection author grouping.
- OPML grouping by author collection.
- Refresh excludes view-only feeds.

## Risks and consequences

- Collections widen by design: a collection holding one feed of an author now
  holds all of that author's feeds and views.
- Per-feed splits are lost; the replacement is a collection feed filter or a
  second collection.
- Deleting an author can leave a collection empty. With filters it becomes
  all-authors; without filters it stays empty.
- Collection index counts become N scope counts. Consistent with the current
  view-aware path, but more work than one grouped query.
- View-only feeds are not refreshed by refresh all. That is the intended scope
  rule.
- A saved collection feed filter naming a feed the collection no longer reaches
  is dropped at load rather than repaired.

## Verification

1. `mise run gen` (templ + sqlc).
2. `go build ./...`.
3. `go test ./...`.
4. Manual check on a scratch database: create a collection, add two authors,
   confirm sources, unread counts, fixed filters, all-authors removal, refresh
   all, and OPML export.
