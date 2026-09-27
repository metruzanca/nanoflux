# instagram

Instagram has no public feed. This plugin scrapes the recent-post grid from the
profile page and is **best-effort** by nature: it depends on markup Instagram
can change or restrict at any time, and a profile with no reachable grid comes
back empty rather than erroring.

## How it works

- It reads the profile page as a crawler user-agent, because Instagram serves
  the logged-out post grid only to crawler identities. A normal browser agent
  gets a login wall with no posts.
- Items are the grid's recent posts, newest first. Instagram shows only the most
  recent slice of a profile anonymously, so older posts are not available.
- No network request is made per post; media and captions come from the single
  profile-page response.

## Quirks

- **Post time is approximate.** Instagram's API does not return a publish
  timestamp for a grid post, so the time is recovered from the numeric media id
  and is accurate to within about a minute (the date is exact).
- **Thumbnails are a stable endpoint, not a signed URL.** The CDN URLs Instagram
  embeds are signed and expire, so the stored image points at Instagram's
  `.../media/?size=l` endpoint, which redirects to a fresh signed URL on load.
- **A post's link uses its shortcode.** Media ids are encoded into Instagram's
  URL-safe shortcode form to build the permalink; a post whose id cannot be
  parsed is skipped.

## Filtering

Items carry the caption as their title (first line, clamped) and body, so the
`title` and `summary` filter fields work as usual. There are no site-specific
categories.
