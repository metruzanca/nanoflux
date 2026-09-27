# bluesky

Reads a profile's posts directly from the AT Protocol instead of the profile RSS
feed. The RSS is text-only: no titles, no media, no author, and an image or
video post is reduced to its caption. This plugin rebuilds each entry from the
raw post record, so you get:

- a title from the first line of the post
- images shown inline (and as a gallery when a post has several)
- video playable in the modal
- link cards and quoted posts rendered as links
- mentions, links and hashtags as clickable anchors

## Quirks

- **The stored feed URL uses the account's DID**, not its handle, so the feed
  keeps working if the handle changes.
- **Replies are not included.** A feed contains the profile's original posts
  only, matching the old RSS behavior.
- **Quotes are links, not expanded.** A quoted post is rendered as a link to it;
  its content is not fetched in, because that would cost one request per quote.
- **A poll reads the most recent window of posts** (currently 50). A very active
  profile may post past that between polls.

## Filtering

Items carry the post text, so the `title` and `summary` filter fields work as
usual. Mentions, links and tags appear as anchors in the summary, and a filter
matches against their visible text. There are no site-specific categories.
