package youtube

import (
	"net/url"
	"strings"
)

// youtubeCanonicalHost is the youtube.com host nanoflux stores feed and page
// URLs under. YouTube answers its internal endpoints (youtubei/v1/browse) only
// on www.youtube.com: the bare host replies 400 to a browse POST, which broke
// every channel feed whose URL the add form had rewritten to youtube.com. The
// www host is the shape that works, so the policy keeps it and rewrites the bare
// (and m.) hosts to it.
const youtubeCanonicalHost = "www.youtube.com"

// CanonicalizeFeedURL rewrites a youtube.com URL to the www host, preserving
// the port (a test/mock origin), path and query. It is idempotent, and returns
// the input unchanged for any non-youtube.com host (youtu.be is a video link,
// not a feed, so it is left alone).
func canonicalFeedURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return raw
	}
	if !isYouTubeSiteHost(u.Hostname()) {
		return raw
	}
	u.Scheme = "https"
	if port := u.Port(); port != "" {
		u.Host = youtubeCanonicalHost + ":" + port
	} else {
		u.Host = youtubeCanonicalHost
	}
	return u.String()
}

// feedToken returns "" : YouTube items carry no attribution tokens (Decorate
// classifies a video link but emits no source parts), so there is nothing for
// the host to resolve back to a subscribed feed.
func feedToken(string) string { return "" }

// isYouTubeSiteHost reports whether host is a youtube.com variant (www., m., or
// bare). youtu.be is excluded: it is a video short link, not a page/feed origin.
func isYouTubeSiteHost(host string) bool {
	host = strings.ToLower(host)
	return host == "youtube.com" || host == "www.youtube.com" || host == "m.youtube.com"
}
