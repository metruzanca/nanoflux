# Feeds are a library, authors are lenses

Status: 2026-11-24. Steps 1 and 2 are implemented. Step 3 is next. The old
"flatten views into authors" step is cancelled.

## Progress

- [x] **Step 1: collections group authors (schemaV68).** Commit `23c11cc`.
  Collections store `collection_authors`. A collection's scope is the union of
  its members' author pages (owned feeds plus their views). Auto collections and
  the synthetic no-collection are gone. `/domains` groups the feed library by
  registrable domain instead. No collection-level filters yet.
- [x] **Step 2: unassigned feeds and the `/feeds` library page (schemaV69).**
  Commit `b588d57`. `feeds.author_id` is still an owner pointer, `0` means
  unassigned, and the author foreign key is gone. `/feeds` lists every feed,
  `/feeds/{id}` shows its items, `/feeds/{id}/edit` is a standalone editor, and
  an author delete unassigns its feeds instead of deleting them.
- [ ] **Step 3: `author_feeds` compatibility membership (schemaV71).** Next.
  See the plan below. V70 is already used by read-item retention.
- [x] **Step 4: flatten saved views into authors.** Cancelled. The live data
  shows views are an author's filter lenses, not redundant sources. See below.
- [ ] Step 5: drop the compatibility owner, move auto-read/delete ownership,
  momentum by feed. Deferred until global filter semantics are decided.
- [ ] Step 6: simplify `visible_items`. Deferred to a separate read-path
  cleanup after step 5's semantics are settled.

## Live usage review (2026-11-24)

Reviewed the current data from a schemaV69 backup. This is the only active
account, so its usage is the product's usage.

Counts:

- 109 saved views, 379 authors, 370 feeds, 34 collections, 36,744 items.
- 88 authors have views: 64 with zero feeds, 20 with one feed, 4 with more than
  one feed.
- 105 of 109 views are a single `tag:show:u/<user>` filter. The other 4 are
  compound tag recipes.
- 4 authors hold multiple views: one holds 19 and three hold 2. All four have
  zero feeds.
- Views are real streams: of the 105 single-tag views, 2 match zero items, 23
  match 1-9, 77 match 10-99, 3 match 100+, for 2,703 matched rows total.
- Collections are large author groups: the largest hold 131, 104, 101, 70 and 43
  authors. One of the largest has 39 view-bearing members and 107 with feeds.

What this means:

- A view is usually an author's **filter lens**, and for 64 authors it is the
  only thing the author has. The author profile is the identity; the view is how
  a poster or person is followed across the library.
- Flattening views into one author each would split multi-lens authors (the
  19-view author into 19 pages), lose author groupings, and turn every lens into
  an author that must pin a static feed list. Views currently match every feed
  dynamically; a pinned list would silently miss feeds added later.
- Worse, collections group authors and expand each member's page. If every
  flattened lens selected all feeds, a collection holding those authors would
  pull the entire library. One large collection alone has 39 view-bearing
  members, and a multi-lens author sits in a small collection. This would break
  collections.
- The real model in use is: **feeds are a library, authors are profiles with
  optional selected feeds and optional filter lenses, collections group
  authors.** That is still a large simplification over feed ownership.

Decision: keep `saved_views` as an author's lenses. Do not flatten. A rename to
"lenses" is optional and can happen later without a data migration.

## Revised target model

```
feeds               user_id, url, poll state, plugin, rank, cache_images
                    author_id = compatibility owner (0 = none)
authors             user_id, name, profile, filter rules
author_feeds        author_id, feed_id             -- selected feeds
saved_views         author_id, title, filters      -- author lenses
collections         user_id, name
collection_authors  collection_id, author_id
items               unchanged: owner feed + item_feeds
```

An author's item scope is the union of:

1. items reachable through its `author_feeds` membership, and
2. items matching any of its views.

Collections are a union of their member authors' scopes.

In step 3 the compatibility owner (`feeds.author_id`) stays authoritative for
global behavior: `visible_items`, auto-read, auto-delete, momentum and global
attribution. Author pages use membership plus views. This keeps the step
additive and reversible.

## Decisions

1. **Authors may hold multiple lenses.** Answered by live usage. Do not flatten
   views. `saved_views` stays as an author-owned lens, with the current
   `tag`/`feed`/`author`/time filter rows.
2. **Keep the owner column during step 3.** `feeds.author_id` remains the
   compatibility owner for global visibility, auto-read/delete, momentum and
   attribution. Step 5 removes it only after global filter semantics are
   decided.
3. **Author filters are page-local in the end state.** Pending for step 5.
   Global lists would ignore author rules, with feed-level or user-level rules
   as a later feature. Not part of step 3.
4. **Auto-read and auto-delete move off authors in step 5.** Both currently key
   off the owner author (`LEFT JOIN authors` so unassigned feeds inherit the
   account window). Shared feeds need a feed or user window. Pending.
5. **Momentum groups by feed in step 5.** Pending.
6. **Collections group authors only.** Implemented. Never mix feeds and authors
   in one collection.
7. **Attribution is scope-derived in the end state.** Step 3 keeps owner
   attribution on global lists and shows the scope author on an author page.

## Step 3 plan: author_feeds compatibility membership

### Migration V71

```sql
CREATE TABLE author_feeds (
    author_id INTEGER NOT NULL REFERENCES authors(id) ON DELETE CASCADE,
    feed_id   INTEGER NOT NULL REFERENCES feeds(id) ON DELETE CASCADE,
    PRIMARY KEY (author_id, feed_id)
);
CREATE INDEX idx_author_feeds_feed ON author_feeds(feed_id);
INSERT OR IGNORE INTO author_feeds (author_id, feed_id)
SELECT author_id, id FROM feeds WHERE author_id <> 0;
```

Do not touch `feeds.author_id` in this migration.

### Write rules

- Feed create with an author: insert membership, set the owner to that author.
- Feed create unassigned: no membership, owner stays 0.
- Feed move to another author: replace the old membership with the new one and
  set the owner.
- Add an author to a feed: insert membership. If the feed's owner is 0, promote
  that author to owner so global rules, auto-read/delete and attribution keep a
  sensible owner.
- Remove an author from a feed: delete membership. If the removed author was the
  owner, promote the lowest remaining member author to owner, else set 0. A feed
  with no members becomes unassigned.
- Author delete: delete memberships. If the deleted author owned feeds, hand
  each feed to its lowest remaining member, else set 0. The feeds and their
  items always survive.
- Feed delete: memberships cascade; no author deletion.

### Read rules

- `authorScopeClause` uses membership (`author_feeds`) plus the author's views.
- `authorRulesVisibleSQL`'s `feed` kind matches through `author_feeds` for the
  rule's author, not `feeds.author_id`.
- `ListFeedsByAuthor` and `ListFeedsByAuthorWithUnread` join `author_feeds`.
- `ListAuthorsWithFeedCount` counts membership.
- `UnreadCountsByFeedScope` and `UnreadCountsByAuthor` count membership.
- `authorFeedFilter` validates that the feed is in the author's membership, not
  that the author owns it.
- Collection feed expansion (`ListFeedsInCollection`) joins
  `collection_authors` to `author_feeds`.
- Global reads, `visible_items`, auto-read, auto-delete, momentum and OPML keep
  using `feeds.author_id`.

### UI

- Author edit gains a `feeds` multi-select for membership. Keep the existing
  views/lens editor.
- Author page feed cards come from membership. The inline feed accordion can
  stay during step 3, but the canonical feed editor is now
  `/feeds/{id}/edit`; prefer linking feed cards there rather than editing feed
  settings inside a shared author page.
- A feed can show "used by N authors" in the library. Add an author picker or
  membership control to the feed editor.
- Author create can select feeds up front. The add-feed flow can select several
  authors later, but is not required for step 3.

### Tests

- Two authors share one feed; both author pages show it, one feed row and one
  item set.
- Author page scope is membership + views; global unread is unchanged.
- Collection scope is unchanged for the same authors.
- Feed-scoped list under a non-owner member author works.
- Author delete removes memberships, keeps feeds and items, and hands ownership
  to another member or 0.
- Adding a member to an ownerless feed promotes the first author.
- Removing the owner from a multi-member feed hands ownership to the other
  member.
- Unassigned feed + membership: a feed with no owner but a member still appears
  on that author's page.

## Deferred later steps

### Step 5: drop the compatibility owner

Gated on deciding global filter semantics. Once decided:

- Make `visible_items` no longer depend on the owner's rules, or replace it.
- Move auto-read and auto-delete to feed or user windows.
- Group momentum by feed.
- Make attribution scope-derived.
- Rebuild `feeds` without `author_id` and remove the owner handoff logic.

### Step 6: simplify `visible_items`

A separate cleanup after step 5. Touch one read path at a time: global lists,
author lists, collections, search, history, list items, shares and stats. Keep a
test per read path for both a global and a scoped read.

### Cancelled

- Flattening views into authors. Keep views as author lenses. A future rename to
  "lenses" is cosmetic.

## Open questions for later review

1. Global filter semantics after shared feeds: author rules page-local, feed
   defaults, or user-level rules. Recommended: page-local, with feed defaults
   later if needed.
2. Auto-read and auto-delete ownership once feeds are shared: feed window, user
   window, or minimum positive window across member authors. Recommended: feed
   or user.
3. Momentum: group by feed on global lists. Recommended.
4. Compatibility owner handoff: confirm "lowest remaining member author" when an
   owner is deleted or removed.
5. Should `saved_views` be renamed to "lenses" in the UI once step 3 lands?

## Risks

- **Owner handoff is the subtle part of step 3.** A feed can outlive its owner.
  Every path that deletes or removes an author must leave the owner consistent,
  or global rules and auto-read silently stop applying.
- **Author page reads must use membership everywhere.** Missing one
  owner-based join makes a shared feed invisible on the second author's page.
- **Do not change global visibility in step 3.** Keeping `visible_items`
  owner-based is what makes this step safe and reversible.
- **Do not flatten views.** It would break multi-lens authors and make
  collections expand to the entire library.
- **Shared read state.** Reading an item on one author page reads it on every
  author page, because items are shared. This is already true for cross-feed
  items and becomes more common.
