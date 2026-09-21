# Authors and feeds: a core principle

nanoflux is organized around **authors**, not feeds. Every feed you subscribe
to belongs to exactly one author, and you always reach your feeds through the
author who publishes them. This document is the canonical statement of that
relationship — treat it as a constraint, not a suggestion.

## The model

- An **author** is a person or account (or a single website) whose content you
  follow. An author owns zero or more **feeds**.
- A **feed** is a concrete RSS/Atom/JSON subscription. A feed **must** belong
  to an author — there is no such thing as an authorless feed. The database
  enforces this (`feeds.author_id` is `NOT NULL`), so it is impossible to
  create a feed without an author from any surface: the web UI, the JSON API,
  or OPML import.
- An author can own many feeds — e.g. one author might have a YouTube channel,
  a blog, and an X profile, each as a separate feed. A feed is never shared
  between authors.

## Views

The app has no "feeds" page. The primary navigation is:

- **unread** — every unread item across all your feeds
- **authors** — your authors, each with a feed count; this is where you add
  content
- **collections** — curated groups of feeds
- **history** and **settings** live in the user menu

Feed detail/edit pages still exist (`/feeds/{id}`, `/feeds/{id}/edit`) and are
reached by drilling in from an author page or an item. Feeds are always
presented in the context of their author: an author page lists the author's
feeds and their items together.

## Adding content

There are exactly two ways to add a feed, and both guarantee it gets an author.

### 1. Add an author (with their first feed)

From the **authors** page, "add author": paste a URL (the site or a feed URL).
nanoflux discovers the feed and shows one combined form:

- the feed's title/url/home page (derived by auto-detection), and
- the author — defaulting to a **new author** named after the discovered page
  (falling back to the feed title, then the page host), or an existing author
  you pick.

Submitting creates the author **and** their first feed together. The result is
always an author with a feed attached. Many authors with a single feed is
perfectly fine — do not feel you must consolidate them.

### 2. Add a feed to an existing author

From an **author page**, "add feed for `<name>`": paste a URL. The feed is
auto-detected and attached to that author — there is no author picker because
the author is fixed by the page you are on.

## Collections work on feeds, not authors

A collection is a group of **feeds**. This is deliberate: an author who posts
video on YouTube and writes a blog is still one author, but you can put only
their YouTube feed in a "video" collection and leave their blog out. The
collection's add-feed picker groups the dropdown by author so you can find
feeds within an author, but membership is always per-feed.

## Consequences

- Deleting an author deletes their feeds (and those feeds' items and
  collection memberships).
- A feed's *last poll failed* badge is shown only on its owner's pages.
- The JSON API's `POST /api/save` will auto-create an author from the feed
  when the extension does not supply one, so a save never ends up authorless.
- OPML import assigns each imported feed an author named after its outline
  (or the feed), so a subscription archive imports cleanly.