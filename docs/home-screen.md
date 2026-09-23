# Home screen customization — design plan

Status: **partially implemented**. The first cut (option 2: a dashboard of
pinned collection sections) is live — see "Implemented (first cut)" below. The
remaining options and open questions describe where it can grow.

## Problem

Today `/` **is** the unread page: `Server.home` (`internal/httpapi/web.go`)
renders every unread item across all feeds with the sort + display-mode pickers
and a "mark all read" button. The topbar's "unread" link also points at `/`.
There is no way to make the app open somewhere else, and no way to see more than
one thing at once on the landing screen.

## Goals

- Let a user choose what the home screen shows.
- Support more than a single list: a home made of a few small sections.
- Keep the existing behavior as the default (a new user still lands on unread).
- Reuse the existing item queries and row components — no new polling/storage
  engine.

## Options considered

1. **Single home target.** One dropdown in settings: unread / history /
   favorites / a specific collection, list, author, or feed. `/` dispatches to
   it. Simple, but limited.
2. **Dashboard of pinned sections (recommended).** Home is an ordered list of
   user-chosen blocks, each a compact item list with a heading and a "more →"
   link. Defaults to a single `unread` section.
   - Source kinds: `unread`, `favorites`, a `collection`, a `list`, an `author`,
     or a `feed`.
   - Per-section item limit, reorder, remove.
   - Optional: "hide when empty".
   - This subsumes option 1: a single target is just a one-section dashboard.
3. **Saved views / smart home.** Persist named searches (the existing
   `author:` / `feed:` / `unread:` FTS qualifiers) as home sections, e.g.
   "Dota news" = `feed:… unread:1`. Powerful, but overlaps heavily with search;
   better as a later extension of option 2.
4. **Cheap landing behaviors (add-ons).** Default sort/display mode for home,
   "hide empty sections", resume-last-read entry.

**Recommendation: option 2**, folding in the "hide empty sections" and
per-section limit from the start.

## Chosen design (option 2)

### Routing

- `/` becomes the **home dashboard**.
- The canonical **unread** page moves to **`/unread`** (new route +
  handler). The existing `home` handler and `homePage` template are reused
  there, with their links rebased from `/` to `/unread`.
- Topbar "unread" link and the command palette's "unread" entry repoint to
  `/unread`.
- `GET /items` (load-more fragment) is reused unchanged.
- A user with no home config gets the default: a single `unread` section on
  `/`, so behavior is unchanged out of the box. Existing bookmarks to `/`
  keep working.

### Data model

Store the ordered section list as JSON on the user:

- `users.home_config` TEXT NULL — JSON array of
  `{"kind":"unread|favorites|collection|list|author|feed","ref_id":<int>,
    "limit":<int>,"hide_empty":<bool>}`.
- `schemaV27`: `ALTER TABLE users ADD COLUMN home_config TEXT;`
- `User.HomeConfig string`; `UserStore.SetHomeConfig(userID, json)`.
- Parse/serialize in `internal/httpapi` (a small `homeConfig` type), not in the
  store — the store keeps the raw string.

Alternative considered: a relational `home_sections` table. More code and more
queries for an inherently small, per-user, ordered blob; JSON wins here.

### Rendering

- New view data: `homeSection{Heading string, MoreURL string, Items []store.ItemWithFeed}`.
- Each section renders `<h2>{Heading} <a href={MoreURL}>more</a></h2>` followed by
  a `<ul class="items" id="home-sec-<n>">` built from `ItemRow`. Unique ids avoid
  the single `#items-list` collision.
- Sections are fed by the existing queries:
  - `unread` → `Items.ListPage(userID, {UnreadOnly:true, Limit:n})`
  - `favorites` → `{FavoritesOnly:true}`
  - `collection` → `{CollectionID:id}`
  - `list` → `Lists.ItemList(...)`
  - `author` → `{AuthorID:id}`
  - `feed` → `{FeedID:id}`
- A single display-mode (list/grid) toggle applies to the whole dashboard,
  scoped `"/"` via the existing per-page display-mode mechanism.
- Empty sections: hidden when `hide_empty`, else render a muted "nothing here".

### Settings card

A "home screen" card on `/settings`, following the existing self-re-rendering
htmx pattern (like the avatar card / author-links reconcile):

- Add a source: a `<select>` of unread / favorites + the user's collections,
  lists, authors, feeds; plus a count and a "hide when empty" checkbox.
- Each row: move up / move down / remove.
- One `POST /settings/home` endpoint does read-modify-write of the JSON and
  re-renders the card; errors use `writeFormError` / `renderError` with a
  `role="alert"` fragment (`#add-home-error`).
- Include a "reset to default" action.

## Files likely touched

- `internal/db/migrate.go` — `schemaV27`, append `{27, schemaV27}`.
- `internal/store/schema.sql` — `users.home_config`.
- `internal/store/queries/users.sql` — `SetUserHomeConfig`.
- `internal/store/user.go` / `convert.go` — `HomeConfig` field + setter.
- `internal/httpapi/web.go` — new `home` (dashboard) + `unread` handler;
  `homeConfig` parse/serialize; section loaders.
- `internal/httpapi/views_layout.templ` — topbar "unread" → `/unread`.
- `internal/httpapi/views_auth.templ` — `homePage` links rebased to `/unread`;
  new `dashboardPage` / section component.
- `internal/httpapi/views_settings.templ` — home-screen card.
- `internal/httpapi/settings.go` — `settingsHome` handler + data.
- `internal/httpapi/server.go` — `GET /unread`, `POST /settings/home`.
- `internal/web/static/app.css` — section spacing, "more" link styling.
- `internal/web/static/app.js` — command palette "unread" → `/unread`.
- Tests: handler tests (default dashboard, custom sections, empty config, auth),
  a store round-trip for `home_config`, and settings-card tests.
- `AGENTS.md` — document the home screen + `/unread` split.

## Open questions

1. Confirm option 2 (dashboard) over option 1 (single target) / option 3
   (saved views).
2. Confirm moving canonical unread to `/unread` and making `/` the home.
3. Storage: JSON pref on `users` (recommended here) vs a relational
   `home_sections` table.
4. Default section limit (suggest 5) and whether "hide empty" defaults on.
