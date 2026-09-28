# Kill the Newsletter

[Kill the Newsletter](https://kill-the-newsletter.com) turns email newsletters
into Atom feeds. You subscribe a newsletter to the address it gives you, and the
emails arrive as feed entries.

This plugin integrates it with nanoflux so you never have to visit the site:

- **Create** a newsletter feed from the add-feed menu ("newsletter (Kill the
  Newsletter)"). nanoflux creates the remote feed and shows you the inbox
  address to subscribe with.
- **Subscribe** the inbox address to a newsletter as you normally would. The
  confirmation email and every issue then arrive as entries in the feed.
- **Manage** the feed on its page: copy the inbox address again, sync the feed's
  title to the service, or delete the remote feed.

## How it works

The Kill the Newsletter feed is a standard Atom feed, so nanoflux reads it with
the generic parser. The inbox address is `{feed id}@{host}` and the feed URL is
`https://{host}/feeds/{feed id}.xml`; the feed id is the only credential, so
anyone with the URL or the address can read the feed or unsubscribe you. Do not
share it.

Entries older than one month are deleted by the service, so an issue you do not
read in time is gone. Mark important items as favorites or bookmarks in nanoflux
to keep them.

## Self-hosting

To use your own Kill the Newsletter instance instead of the public service, set
`NF_KTN_HOST` to its host (for example `newsletter.example.com`) and restart
nanoflux.
