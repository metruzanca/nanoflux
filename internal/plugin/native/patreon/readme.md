# patreon

Patreon has no RSS. This plugin reads a creator's public posts through Patreon's
web API, which needs no key. It is **best-effort**: locked posts carry only what
an anonymous visitor can see.

## How it works

- A creator is resolved from the profile's vanity slug. Paste a creator page
  (`patreon.com/c/Name`, `patreon.com/cw/Name`, or `patreon.com/Name`); the
  campaign id is looked up and its posts are fetched.
- Posts come from the campaign-filtered posts endpoint, cursor-paginated.

## Quirks

- **Locked posts still appear**, but only with their title and image: their
  content fields come back null for an anonymous reader. A creator whose posts
  are mostly paid will therefore show titles and images, not full text.
- The campaign-filtered posts endpoint is used deliberately. Patreon's older
  per-campaign endpoint returns only the anonymous subset (observed: 1 of 137
  posts for one campaign), so a mostly-paid creator's feed would show one very
  old post and never the recent ones.
- Post bodies arrive as ProseMirror content JSON and are flattened to plain
  text; the raw HTML `content` field is only a fallback.

## Filtering

Items carry the title and body text, so the `title` and `summary` filter fields
work as usual. There are no site-specific categories.
