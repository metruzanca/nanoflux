package discover

import (
	"net/url"
	"strings"
)

// redditCanonicalOrigin is nanoflux's preferred reddit origin. Reddit serves
// the same .rss feeds on the bare host (reddit.com redirects to www), so the
// shorter form is used and kept stable. old./np./m. hosts are rewritten to it:
// old.reddit.com sends .rss to a login wall.
const redditCanonicalOrigin = "https://reddit.com"

// redditHosts are the reddit hostnames whose pages map to a derivable feed.
var redditHosts = map[string]bool{
	"reddit.com": true, "www.reddit.com": true,
	"old.reddit.com": true, "np.reddit.com": true,
	"m.reddit.com": true,
}

// Derive resolves a feed URL for a page URL using fixed URL rules, without any
// network request, and reports whether a rule matched. It exists so hosts whose
// feed shape is known — currently reddit, whose .rss endpoints sit behind a
// tight anonymous rate limit — are never probed during discovery: the candidate
// is built directly and the host's request budget is left for polling.
//
// It normalizes any reddit host (www./old./np./m.) and both user path forms to
// the canonical shape, then appends .rss:
//
//	/r/{sub}[/...]        -> https://reddit.com/r/{sub}.rss
//	/user/{name}[/...]    -> https://reddit.com/u/{name}/submitted.rss
//	/u/{name}[/...]       -> https://reddit.com/u/{name}/submitted.rss
//
// A user's bare overview feed mixes posts and comments; /submitted.rss is
// posts-only, which is what a reader wants and what keeps the entries' own
// categories meaningful. An already-.rss URL of those shapes is accepted
// unchanged (idempotent).
//
// Title is the feed's display name ("r/sub" or "u/name"). AuthorName is the
// preferred name for a newly created author: "r/sub" for a subreddit (the user
// is subscribing to the community) and just "{name}" for a user (who may have
// feeds on other sites, where a "u/" prefix would read oddly).
func Derive(rawurl string) (Candidate, bool) {
	u, err := url.Parse(strings.TrimSpace(rawurl))
	if err != nil || u.Host == "" {
		return Candidate{}, false
	}
	if !isRedditHost(u.Hostname()) {
		return Candidate{}, false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return Candidate{}, false
	}

	switch {
	case parts[0] == "r":
		sub := trimRSS(parts[1])
		if sub == "" {
			return Candidate{}, false
		}
		home := redditCanonicalOrigin + "/r/" + sub
		return Candidate{
			FeedURL:    home + ".rss",
			Title:      "r/" + sub,
			AuthorName: "r/" + sub,
			HomeURL:    home,
			Strategy:   "derived",
		}, true
	case parts[0] == "user" || parts[0] == "u":
		name := trimRSS(parts[1])
		if name == "" {
			return Candidate{}, false
		}
		home := redditCanonicalOrigin + "/u/" + name
		return Candidate{
			FeedURL:    home + "/submitted.rss",
			Title:      "u/" + name,
			AuthorName: name,
			HomeURL:    home,
			Strategy:   "derived",
		}, true
	}
	return Candidate{}, false
}

// trimRSS strips a trailing .rss so an already-feed URL (e.g. /r/x.rss) derives
// to itself rather than appending a second suffix.
func trimRSS(seg string) string {
	return strings.TrimSuffix(seg, ".rss")
}

// isRedditHost reports whether host is one of the reddit hosts whose feeds are
// derivable.
func isRedditHost(host string) bool {
	return redditHosts[strings.ToLower(host)]
}

// IsRedditHost reports whether rawurl points at a reddit host. It lets callers
// avoid fetching reddit pages too (not just their .rss), since those requests
// share the same tight anonymous per-IP rate limit.
func IsRedditHost(rawurl string) bool {
	u, err := url.Parse(strings.TrimSpace(rawurl))
	if err != nil {
		return false
	}
	return isRedditHost(u.Hostname())
}
