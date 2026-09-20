package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mmcdole/gofeed"
	"golang.org/x/net/html"

	"github.com/metruzanca/nanoflux/internal/oembed"
)

// redditRSSBaseURL is the origin used for subreddit RSS lookups. It is a
// package var so tests can inject a mock host.
var redditRSSBaseURL = "https://www.reddit.com"

// redditEmbedBaseURL hosts reddit's embed pages, which render a post's full
// media (including every gallery image) and are served without a login. It is
// a package var so tests can inject a mock host.
var redditEmbedBaseURL = "https://embed.reddit.com"

// resolveItemSource follows a post's external link chain. For reddit link
// posts (content is an external site: imgur, a news article) it
// returns the destination URL and, when the site publishes an oEmbed endpoint,
// an embeddable iframe src. For reddit gallery posts it returns the post's
// full-res images. Nothing here knows about specific destinations: the [link]
// anchor reddit emits, the provider's oEmbed discovery, and reddit's embed
// page are all generic. Empty/nil results mean no extra content to show.
func (s *Server) resolveItemSource(ctx context.Context, it itemViewData) (source, embedSrc string, gallery []string) {
	dest, ok := s.redditDestination(ctx, it.Summary, it.Link)
	if !ok {
		return "", "", nil
	}
	if gid, ok := redditGalleryID(dest); ok {
		sub, _, _ := redditPostID(it.Link)
		return "", "", s.redditGalleryImages(ctx, sub, gid)
	}
	if e, err := s.oembed.Resolve(ctx, dest); err == nil && e.Src != "" {
		embedSrc = e.Src
	}
	return dest, embedSrc, nil
}

// redditDestination finds the [link] anchor destination of a reddit post. The
// post's own summary is tried first: reddit's feed content marks the
// destination (an external URL for link posts, a /gallery/{id} URL for
// galleries) with an anchor whose text is exactly "[link]". Feeds that strip
// that content (third-party proxies) fall back to a lookup in the post's
// subreddit RSS, keyed by the post id from the item link.
func (s *Server) redditDestination(ctx context.Context, summary, link string) (string, bool) {
	if u, ok := extractLinkAnchor(summary); ok {
		return u, true
	}
	sub, id, ok := redditPostID(link)
	if !ok {
		return "", false
	}
	return s.redditSubExternalLink(ctx, sub, id)
}

// redditGalleryID returns the gallery id when dest is reddit's own gallery
// page (https://www.reddit.com/gallery/{id}), which the [link] anchor of a
// gallery post points at.
func redditGalleryID(dest string) (string, bool) {
	u, err := url.Parse(dest)
	if err != nil {
		return "", false
	}
	switch strings.ToLower(u.Hostname()) {
	case "reddit.com", "www.reddit.com", "old.reddit.com", "np.reddit.com":
	default:
		return "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 2 && parts[0] == "gallery" && parts[1] != "" {
		return parts[1], true
	}
	return "", false
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

// redditSubExternalLink looks up a post's [link] anchor in its subreddit's RSS
// feed, keyed by the post's t3_ id. The subreddit RSS is public (unlike the
// JSON/HTML APIs, which reddit gates behind a login wall). Feeds are cached
// per subreddit; only the guid -> [link] mapping is retained.
func (s *Server) redditSubExternalLink(ctx context.Context, sub, id string) (string, bool) {
	if sub == "" {
		return "", false
	}
	guid := "t3_" + id
	if u, ok := s.reddit.subLookup(sub, guid); ok {
		return u, true
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
	links := make(map[string]string, len(parsed.Items))
	for _, it := range parsed.Items {
		content := it.Content
		if content == "" {
			content = it.Description
		}
		if u, ok := extractLinkAnchor(content); ok {
			links[it.GUID] = u
		}
	}
	s.reddit.setSub(sub, links)
	u, ok := links[guid]
	return u, ok
}

// redditCache caches subreddit RSS lookups and gallery image enumerations for
// a short TTL. Results are keyed by subreddit/post id.
type redditCache struct {
	mu      sync.Mutex
	sub     map[string]subEntry  // subreddit -> guid -> [link] href
	gallery map[string]galEntry  // post id -> full-res images
}

type subEntry struct {
	links map[string]string
	at    time.Time
}

type galEntry struct {
	imgs []string
	at   time.Time
}

const (
	redditSubTTL    = 10 * time.Minute
	redditGalleryTTL = 15 * time.Minute
	redditCacheCap   = 1000
)

func newRedditCache() *redditCache {
	return &redditCache{sub: make(map[string]subEntry), gallery: make(map[string]galEntry)}
}

func (c *redditCache) subLookup(sub, guid string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.sub[sub]
	if !ok || time.Since(e.at) > redditSubTTL {
		return "", false
	}
	u, ok := e.links[guid]
	return u, ok
}

func (c *redditCache) setSub(sub string, links map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.sub) >= redditCacheCap {
		for k := range c.sub {
			delete(c.sub, k)
			break
		}
	}
	c.sub[sub] = subEntry{links: links, at: time.Now()}
}

func (c *redditCache) galleryGet(id string) ([]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.gallery[id]
	if !ok || time.Since(e.at) > redditGalleryTTL {
		return nil, false
	}
	return e.imgs, true
}

func (c *redditCache) gallerySet(id string, imgs []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.gallery) >= redditCacheCap {
		for k := range c.gallery {
			delete(c.gallery, k)
			break
		}
	}
	c.gallery[id] = galEntry{imgs: imgs, at: time.Now()}
}

// redditGalleryImages returns the full-res image URLs of a gallery post,
// scraped from its embed page. Reddit's JSON/HTML APIs are login-walled, but
// embed.reddit.com renders the post's full media without one. Results are
// cached per post id.
func (s *Server) redditGalleryImages(ctx context.Context, sub, id string) []string {
	if id == "" {
		return nil
	}
	if imgs, ok := s.reddit.galleryGet(id); ok {
		return imgs
	}
	var imgs []string
	if sub != "" {
		pageURL := redditEmbedBaseURL + "/r/" + sub + "/comments/" + id
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
		if err == nil {
			req.Header.Set("User-Agent", oembed.BrowserUserAgent)
			if resp, err := s.client.Do(req); err == nil {
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
				resp.Body.Close()
				imgs = redditGalleryImagesFromHTML(body)
			}
		}
	}
	s.reddit.gallerySet(id, imgs)
	return imgs
}

// redditGalleryImgRe matches a preview.redd.it URL and captures the file id
// and extension (e.g. .../whiskers-v0-9z8x7c6v.jpg).
var redditGalleryImgRe = regexp.MustCompile(`preview\.redd\.it/[^"'\s]*?([A-Za-z0-9_]{6,})\.(jpe?g|png|gif)`)

// redditGalleryImagesFromHTML extracts the gallery's image file ids from an
// embed page and rewrites them to full-res i.redd.it URLs.
func redditGalleryImagesFromHTML(body []byte) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range redditGalleryImgRe.FindAllSubmatch(body, -1) {
		u := "https://i.redd.it/" + string(m[1]) + "." + string(m[2])
		if seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, u)
		if len(out) >= 25 {
			break
		}
	}
	return out
}

// fullRedditImage rewrites a preview.redd.it/{id}.{ext} URL to the full-res
// i.redd.it/{id}.{ext} original. Reddit signs preview URLs per-size, but the
// i.redd.it original needs no signature and serves unauthenticated.
func fullRedditImage(previewURL string) string {
	u, err := url.Parse(previewURL)
	if err != nil || u.Hostname() != "preview.redd.it" {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 1 {
		return ""
	}
	id, ext, ok := strings.Cut(parts[0], ".")
	if !ok || id == "" || ext == "" {
		return ""
	}
	if ext == "jpeg" {
		ext = "jpg"
	}
	return "https://i.redd.it/" + id + "." + ext
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