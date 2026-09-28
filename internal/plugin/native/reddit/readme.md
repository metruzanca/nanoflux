# reddit

reddit feeds are plain RSS, so the generic parser fetches them. This plugin adds
two things on top.

## Post media

A post's real content (an external destination, an embedded player, or a
multi-image gallery) is marked in reddit's feed HTML in a way a stored item
cannot carry, so it is resolved when the item modal opens.

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
