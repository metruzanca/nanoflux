# youtube

Fetches a channel's recent videos through YouTube's internal browse API rather
than the channel RSS. The RSS carries no video duration; browse returns duration
(and view count) for every entry in the same request.

## Quirks

- **Duration is shown as a pill** on the card. It is read from the video's
  thumbnail badge, so a live stream (badged "LIVE") or a fresh upload (badged
  "New") has no known duration and shows none.
- **Publish time is made absolute at fetch time.** Browse reports it only as
  relative text ("3 days ago"), which is converted to a real timestamp when the
  feed is polled, so a stored item never shows a frozen "3 days ago".
- Any channel URL form is resolved to its channel id, so the feed keeps working
  regardless of how the channel was linked.

## Filtering

Items carry the video title and description, so the `title` and `summary` filter
fields work as usual. There are no site-specific categories.
