# reddit

reddit feeds are plain RSS, so the generic parser fetches them. This plugin adds
reddit-specific behavior on top.

## Post media

A post's real content (an external destination, an embedded player, or a
multi-image gallery) is marked in reddit's feed HTML in a way a stored item
cannot carry, so it is resolved when the item modal opens.

## Source attribution

A post is shown as "r/cats by u/sam". Both the subreddit and the poster link to
your own subscribed feed for them when you have one, so you stay inside
nanoflux; otherwise they link to reddit.

## Cross-feed dedup

The same post seen through a subreddit feed and through the poster's user feed is
stored once, so reading it in one leaves it read in the other.

A user who reposts the same link to several subreddits under different titles is
also shown once: the plugin keys each link post by its poster and external
destination, and the host collapses the duplicates into a single item that lists
the other subreddits as sources. This applies on the author and collection pages.

## Categories (for filtering)

Every reddit item carries labels you can write filter rules against. A rule's
**field** is `category` and its **pattern** is matched against any one of them,
case-insensitively (or as a regex when **regex** is checked).

| Feed | Category labels |
| --- | --- |
| Subreddit (`r/foo`) | the post's author, e.g. `u/someuser` |
| User (`u/someuser`) | the destination subreddit, e.g. `r/foo` |

So on a subreddit feed you filter by who posted, and on a user feed you filter
by where they posted.

## Discovery mode (hide authors you follow)

A feed's edit page has a **discovery mode** toggle. With it on, the feed drops
every post whose author you already follow through another subscription, so a
subreddit feed can show only what you do not already get from the users you
follow. Subscribe to an author and their posts leave the community feed; the
posts stay in the author's own feed.

reddit's plugin identifies the author as the item's `u/<name>` category (never
the `r/<sub>`, which is a community, not a person), and the host matches it
against the tokens of your subscribed feeds. This is why a post by `u/sam` in
`r/cats` disappears from `r/cats` when you follow `u/sam`, but `r/cats` itself
never hides its own posts.

### Examples

Delete everything from a user in a subreddit you otherwise follow:

- action: `delete`
- field: `category`
- pattern: `u/thatguy`

Keep a user feed focused on one subreddit, deleting the rest:

- action: `delete`
- field: `category`
- pattern: `r/politics`

A rule acts when its pattern matches, so to keep only certain subs, add one
`delete` rule per unwanted sub. To filter by title or body text instead, use the
`title` or `summary` field. The same `category` field works on any feed type
whose parser provides categories, not just reddit.
