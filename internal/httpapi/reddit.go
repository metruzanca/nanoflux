package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/mmcdole/gofeed"
	"golang.org/x/net/html"

	"github.com/metruzanca/nanoflux/internal/oembed"
)

// redditRSSBaseURL is the origin used for subreddit RSS lookups. It is a
// package var so tests can inject a mock host.
var redditRSSBaseURL = "https://www.reddit.com"

// resolveItemSource follows a post's external link chain: for reddit posts
// whose primary content is an external site (a "link post" — e.g. imgur,
// a news article), it returns the destination URL and, when the site
// publishes an oEmbed endpoint, an embeddable iframe src. Nothing here knows
// about specific destinations: the [link] anchor reddit emits and the
// provider's oEmbed discovery are both generic. Returns empty strings when
// there is no external destination or the chain cannot be resolved.
func (s *Server) resolveItemSource(ctx context.Context, it itemViewData) (source, embedSrc string) {
	u, ok := s.redditExternalLink(ctx, it.Summary, it.Link, it.ImageURL)
	if !ok {
		return "", ""
	}
	if e, err := s.oembed.Resolve(ctx, u); err == nil && e.Src != "" {
		embedSrc = e.Src
	}
	return u, embedSrc
}

// redditExternalLink finds the external destination of a reddit post. The
// post's own summary is tried first: reddit's feed content marks the
// destination with an anchor whose text is exactly "[link]". Feeds that strip
// that content (third-party proxies) fall back to a lookup in the post's
// subreddit RSS, keyed by the post id from the item link.
func (s *Server) redditExternalLink(ctx context.Context, summary, link, imageURL string) (string, bool) {
	if u, ok := extractLinkAnchor(summary); ok {
		return u, true
	}
	sub, id, ok := redditPostID(link)
	if !ok || !redditLinkPostThumb(imageURL) {
		return "", false
	}
	return s.redditSubExternalLink(ctx, sub, id)
}

// redditLinkPostThumb reports whether an item's stored image is reddit's
// "external-preview" thumbnail, which is only used for link posts (posts whose
// content is an external site). Image/self posts use preview.redd.it instead.
func redditLinkPostThumb(imageURL string) bool {
	u, err := url.Parse(imageURL)
	if err != nil {
		return false
	}
	return u.Hostname() == "external-preview.redd.it"
}

// redditPostID extracts the subreddit and post id from a reddit comments URL
// like /r/{sub}/comments/{id}/slug or /comments/{id}/slug.
func redditPostID(link string) (sub, id string, ok bool) {
	u, err := url.Parse(link)
	if err != nil {
		return "", "", false
	}
	switch strings.ToLower(u.Hostname()) {
	case "reddit.com", "www.reddit.com", "old.reddit.com", "np.reddit.com":
	default:
		return "", "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) >= 4 && parts[0] == "r" && parts[2] == "comments" && parts[3] != "" {
		return parts[1], parts[3], true
	}
	if len(parts) >= 2 && parts[0] == "comments" && parts[1] != "" {
		return "", parts[1], true
	}
	return "", "", false
}

// redditSubExternalLink looks up a post's external destination in its
// subreddit's RSS feed. The subreddit RSS is public (unlike the JSON/HTML
// APIs, which reddit gates behind a login wall) and embeds the same [link]
// anchor, keyed by the post's t3_ id.
func (s *Server) redditSubExternalLink(ctx context.Context, sub, id string) (string, bool) {
	if sub == "" {
		return "", false
	}
	feedURL := redditRSSBaseURL + "/r/" + sub + "/.rss"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("User-Agent", oembed.BrowserUserAgent)
	resp, err := s.client.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", false
	}
	parsed, err := gofeed.NewParser().Parse(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", false
	}
	for _, it := range parsed.Items {
		if it.GUID != "t3_"+id {
			continue
		}
		content := it.Content
		if content == "" {
			content = it.Description
		}
		if u, ok := extractLinkAnchor(content); ok {
			return u, true
		}
	}
	return "", false
}

// extractLinkAnchor finds the anchor whose text is exactly "[link]" — reddit's
// marker for the external destination of a link post — and returns its href.
func extractLinkAnchor(htmlContent string) (string, bool) {
	if htmlContent == "" {
		return "", false
	}
	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		return "", false
	}
	var find func(*html.Node) (string, bool)
	find = func(n *html.Node) (string, bool) {
		if n.Type == html.ElementNode && n.Data == "a" && strings.TrimSpace(nodeText(n)) == "[link]" {
			for _, a := range n.Attr {
				if strings.ToLower(a.Key) != "href" {
					continue
				}
				u, err := url.Parse(a.Val)
				if err == nil && (u.Scheme == "http" || u.Scheme == "https") {
					return u.String(), true
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if u, ok := find(c); ok {
				return u, true
			}
		}
		return "", false
	}
	return find(doc)
}

func nodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}