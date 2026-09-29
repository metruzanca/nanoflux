package reddit

import (
	"net/url"
	"strings"

	"github.com/metruzanca/nanoflux/pluginapi"
)

// redditCanonicalOrigin is nanoflux's preferred reddit origin. Reddit serves
// the same .rss feeds on the bare host (reddit.com redirects to www), so the
// www host is used: it is the one that answers without a redirect, and each
// redirect hop spends a request from reddit's tight anonymous rate-limit
// budget. old./np./m. hosts are rewritten to it: old.reddit.com sends .rss to a
// login wall.
const redditCanonicalOrigin = "https://www.reddit.com"

// deriveFeed resolves a feed URL for a page URL using fixed URL rules, without
// any network request, and reports whether a rule matched. Reddit's .rss
// endpoints sit behind a tight anonymous rate limit, so discovery never probes
// them: the candidate is built directly and the host's request budget is left
// for polling.
//
// It normalizes any reddit host (www./old./np./m.) and both user path forms to
// the canonical shape, then appends .rss:
//
//	/r/{sub}[/...]        -> https://www.reddit.com/r/{sub}.rss
//	/user/{name}[/...]    -> https://www.reddit.com/user/{name}/submitted.rss
//	/u/{name}[/...]       -> https://www.reddit.com/user/{name}/submitted.rss
//
// The /user/ form (not the /u/ short form) is used because reddit 301-redirects
// /u/ to /user/, and the extra hop spends a request from the host's tight
// anonymous rate-limit budget. A user's bare overview feed mixes posts and
// comments; /submitted.rss is posts-only, which is what a reader wants and what
// keeps the entries' own categories meaningful. An already-.rss URL of those
// shapes is accepted unchanged (idempotent).
//
// Title is the feed's display name ("r/sub" or "u/name"). AuthorName is the
// preferred name for a newly created author: "r/sub" for a subreddit (the user
// is subscribing to the community) and just "{name}" for a user (who may have
// feeds on other sites, where a "u/" prefix would read oddly).
func deriveFeed(rawurl string) (pluginapi.Candidate, bool) {
	u, err := url.Parse(strings.TrimSpace(rawurl))
	if err != nil || u.Host == "" {
		return pluginapi.Candidate{}, false
	}
	if !isRedditHost(u.Hostname()) {
		return pluginapi.Candidate{}, false
	}
	// A search page is not a subreddit/user feed; leave it for the search owner.
	if isSearchURL(u) {
		return pluginapi.Candidate{}, false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return pluginapi.Candidate{}, false
	}

	switch {
	case parts[0] == "r":
		sub := trimRSS(parts[1])
		if sub == "" {
			return pluginapi.Candidate{}, false
		}
		home := redditCanonicalOrigin + "/r/" + sub
		return pluginapi.Candidate{
			FeedURL:    home + ".rss",
			Title:      "r/" + sub,
			AuthorName: "r/" + sub,
			HomeURL:    home,
			Derived:    true,
		}, true
	case parts[0] == "user" || parts[0] == "u":
		name := trimRSS(parts[1])
		if name == "" {
			return pluginapi.Candidate{}, false
		}
		home := redditCanonicalOrigin + "/user/" + name
		return pluginapi.Candidate{
			FeedURL:    home + "/submitted.rss",
			Title:      "u/" + name,
			AuthorName: name,
			HomeURL:    home,
			Derived:    true,
		}, true
	}
	return pluginapi.Candidate{}, false
}

// trimRSS strips a trailing .rss so an already-feed URL (e.g. /r/x.rss) derives
// to itself rather than appending a second suffix.
func trimRSS(seg string) string {
	return strings.TrimSuffix(seg, ".rss")
}

// canonicalFeedURL rewrites a reddit feed URL to the form reddit answers without
// a redirect (see CanonicalizeFeedURL on Plugin). Ported from the store's former
// reddit-specific CanonicalFeedURL.
func canonicalFeedURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return raw
	}
	if !isRedditHost(u.Hostname()) {
		return raw
	}
	if isSearchURL(u) {
		return raw
	}
	// Canonical host: www.reddit.com, keeping any port.
	u.Scheme = "https"
	if port := u.Port(); port != "" {
		u.Host = "www.reddit.com:" + port
	} else {
		u.Host = "www.reddit.com"
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	// /u/{name} -> /user/{name}: reddit 301-redirects the short form, and the
	// extra hop costs a request from the host's tight anonymous budget.
	if len(parts) >= 2 && parts[0] == "u" {
		parts[0] = "user"
	}
	// A user's bare feed -> the posts-only /submitted.rss. Both bare forms are
	// handled: the ".rss" suffix on the name (/user/{name}.rss) and a trailing
	// ".rss" segment (/user/{name}/.rss). Explicit /submitted.rss and
	// /comments.rss are preserved.
	if len(parts) >= 2 && parts[0] == "user" {
		if name, ok := strings.CutSuffix(parts[1], ".rss"); ok {
			parts = append([]string{parts[0], name}, parts[2:]...)
		}
		if len(parts) >= 3 && parts[2] == ".rss" {
			parts = append([]string{parts[0], parts[1]}, parts[3:]...)
		}
		if len(parts) == 2 {
			parts = append(parts, "submitted.rss")
		}
	}
	if len(parts) >= 1 && parts[0] != "" {
		u.Path = "/" + strings.Join(parts, "/")
	}
	return u.String()
}

// feedToken derives the "r/<sub>" or "u/<name>" token a reddit feed URL
// represents, lowercased, or "" when the URL is not a reddit feed. The token is
// the same string reddit puts in an entry's <category> (and so in Item
// categories), which lets the host map a category back to the subscribed feed.
// It is derived from the URL, not the feed title, so a renamed feed still
// resolves.
func feedToken(feedURL string) string {
	u, err := url.Parse(strings.TrimSpace(feedURL))
	if err != nil || u.Host == "" {
		return ""
	}
	if !isRedditHost(u.Hostname()) {
		return ""
	}
	if isSearchURL(u) {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return ""
	}
	name := trimRSS(parts[1])
	if name == "" {
		return ""
	}
	switch parts[0] {
	case "r":
		return "r/" + strings.ToLower(name)
	case "user", "u":
		return "u/" + strings.ToLower(name)
	}
	return ""
}
