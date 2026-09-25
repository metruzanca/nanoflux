// Package reddit is the native plugin for reddit subreddits and user profiles.
//
// Reddit's .rss feeds are public and are handled by the generic feed parser via
// discover's host rules, so this plugin does not fetch feeds. What it owns is
// the *view-time* media resolution for a post: reddit marks a post's real
// content in its feed HTML with a "[link]" anchor, and that content — an
// external destination, an oEmbed player, or a multi-image gallery — cannot be
// represented on a stored item, so it is resolved when the item modal opens.
package reddit

import (
	"bytes"
	"context"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mmcdole/gofeed"
	"golang.org/x/net/html"

	"github.com/metruzanca/nanoflux/internal/oembed"
	"github.com/metruzanca/nanoflux/pluginapi"
)

// Name is the plugin's stable identifier.
const Name = "reddit"

// RSSBaseURL is the origin used for subreddit RSS lookups. A var so tests can
// point it at a mock host.
var RSSBaseURL = "https://www.reddit.com"

// EmbedBaseURL hosts reddit's embed pages, which render a post's full media
// (including every gallery image) and are served without a login. A var so
// tests can point it at a mock host.
var EmbedBaseURL = "https://embed.reddit.com"

// redditHosts are the hosts this plugin recognizes.
var redditHosts = []string{"reddit.com", "www.reddit.com", "old.reddit.com", "np.reddit.com"}

// Plugin implements the reddit view-time renderer. Its caches persist across
// item opens (nanoflux keeps the plugin alive).
type Plugin struct {
	mu      sync.Mutex
	sub     map[string]subEntry // subreddit -> guid -> [link] href
	gallery map[string]galEntry // post id -> full-res images
}

var (
	_ pluginapi.Fetcher  = (*Plugin)(nil)
	_ pluginapi.Renderer = (*Plugin)(nil)
)

func (*Plugin) Meta() pluginapi.Meta {
	return pluginapi.Meta{Name: Name, APIVersion: pluginapi.APIVersion, UserAgent: oembed.BrowserUserAgent}
}

// Match handles view-time rendering for reddit post permalinks. It does not
// claim fetch or discover: reddit's .rss feeds are standard feeds.
func (*Plugin) Match(u *url.URL, cap pluginapi.Capability) bool {
	if cap != pluginapi.CapRender || u == nil {
		return false
	}
	return isRedditHost(u.Hostname())
}

// Discover is unsupported: reddit's feeds are plain RSS found by host rules.
func (*Plugin) Discover(context.Context, string, pluginapi.Host) ([]pluginapi.Candidate, error) {
	return nil, pluginapi.ErrUnsupportedCapability
}

// Fetch is unsupported: the generic parser handles reddit's .rss feeds.
func (*Plugin) Fetch(context.Context, pluginapi.FetchRequest, pluginapi.Host) (pluginapi.Result, error) {
	return pluginapi.Result{}, pluginapi.ErrUnsupportedCapability
}

// Render resolves a reddit post's view-time media: an embeddable player for a
// link post, the full-res images of a gallery, or an image post's original.
func (p *Plugin) Render(ctx context.Context, req pluginapi.RenderRequest, h pluginapi.Host) (pluginapi.Media, error) {
	dest, ok := p.destination(ctx, req.Summary, req.Link, h)
	if !ok {
		return pluginapi.Media{}, nil
	}
	if gid, ok := galleryID(dest); ok {
		sub, _, _ := postID(req.Link)
		return pluginapi.Media{Gallery: p.galleryImages(ctx, sub, gid, h)}, nil
	}
	// A reddit image post's [link] points straight at the image itself (usually
	// the full-res i.redd.it original). Render it as the post's content rather
	// than treating it as an external destination: a JPEG has no oEmbed, so the
	// "source" path would otherwise leave the post with no media at all.
	if isImageURL(dest) {
		if full := fullImage(dest); full != "" {
			dest = full
		}
		return pluginapi.Media{Gallery: []string{dest}}, nil
	}
	if e, err := oembed.DiscoverWith(ctx, hGetter(h), dest); err == nil && e.Src != "" {
		return pluginapi.Media{SourceURL: dest, EmbedSrc: e.Src}, nil
	}
	return pluginapi.Media{SourceURL: dest}, nil
}

// hGetter adapts a pluginapi.Host to an oembed.Getter so oEmbed requests are
// mediated by the host (User-Agent, timeouts, per-host pacing).
func hGetter(h pluginapi.Host) oembed.Getter {
	return func(ctx context.Context, rawurl string) (int, []byte, error) {
		resp, err := h.Do(ctx, pluginapi.HTTPRequest{Method: "GET", URL: rawurl})
		if err != nil {
			return 0, nil, err
		}
		return resp.Status, resp.Body, nil
	}
}

// destination finds the "[link]" anchor target of a reddit post. The post's own
// summary is tried first; feeds that strip that content fall back to a lookup
// in the post's subreddit RSS, keyed by the post id.
func (p *Plugin) destination(ctx context.Context, summary, link string, h pluginapi.Host) (string, bool) {
	if u, ok := extractLinkAnchor(summary); ok {
		return u, true
	}
	sub, id, ok := postID(link)
	if !ok {
		return "", false
	}
	return p.subExternalLink(ctx, sub, id, h)
}

// subExternalLink looks up a post's "[link]" anchor in its subreddit's RSS
// feed, keyed by the post's t3_ id. The subreddit RSS is public. Feeds are
// cached per subreddit; only the guid -> [link] mapping is retained.
func (p *Plugin) subExternalLink(ctx context.Context, sub, id string, h pluginapi.Host) (string, bool) {
	if sub == "" {
		return "", false
	}
	guid := "t3_" + id
	if u, ok := p.subLookup(sub, guid); ok {
		return u, true
	}
	feedURL := RSSBaseURL + "/r/" + sub + "/.rss"
	resp, err := h.Do(ctx, pluginapi.HTTPRequest{Method: "GET", URL: feedURL})
	if err != nil || resp.Status >= 400 {
		return "", false
	}
	parsed, err := parseFeedLinks(resp.Body)
	if err != nil {
		return "", false
	}
	p.setSub(sub, parsed)
	u, ok := parsed[guid]
	return u, ok
}

// galleryImages returns the full-res image URLs of a gallery post, scraped from
// its embed page. Results are cached per post id.
func (p *Plugin) galleryImages(ctx context.Context, sub, id string, h pluginapi.Host) []string {
	if id == "" {
		return nil
	}
	if imgs, ok := p.galleryGet(id); ok {
		return imgs
	}
	var imgs []string
	if sub != "" {
		pageURL := EmbedBaseURL + "/r/" + sub + "/comments/" + id
		if resp, err := h.Do(ctx, pluginapi.HTTPRequest{Method: "GET", URL: pageURL}); err == nil && resp.Status < 400 {
			imgs = galleryImagesFromHTML(resp.Body)
		}
	}
	p.gallerySet(id, imgs)
	return imgs
}

// subLookup / setSub / galleryGet / gallerySet are the TTL caches.

type subEntry struct {
	links map[string]string
	at    time.Time
}

type galEntry struct {
	imgs []string
	at   time.Time
}

const (
	subTTL     = 10 * time.Minute
	galleryTTL = 15 * time.Minute
	cacheCap   = 1000
)

func (p *Plugin) subLookup(sub, guid string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.sub[sub]
	if !ok || time.Since(e.at) > subTTL {
		return "", false
	}
	u, ok := e.links[guid]
	return u, ok
}

func (p *Plugin) setSub(sub string, links map[string]string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sub == nil {
		p.sub = make(map[string]subEntry)
	}
	if len(p.sub) >= cacheCap {
		for k := range p.sub {
			delete(p.sub, k)
			break
		}
	}
	p.sub[sub] = subEntry{links: links, at: time.Now()}
}

func (p *Plugin) galleryGet(id string) ([]string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.gallery[id]
	if !ok || time.Since(e.at) > galleryTTL {
		return nil, false
	}
	return e.imgs, true
}

func (p *Plugin) gallerySet(id string, imgs []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.gallery == nil {
		p.gallery = make(map[string]galEntry)
	}
	if len(p.gallery) >= cacheCap {
		for k := range p.gallery {
			delete(p.gallery, k)
			break
		}
	}
	p.gallery[id] = galEntry{imgs: imgs, at: time.Now()}
}

func isRedditHost(host string) bool {
	host = strings.ToLower(host)
	for _, h := range redditHosts {
		if host == h {
			return true
		}
	}
	return false
}

// galleryID returns the gallery id when dest is reddit's own gallery page
// (https://www.reddit.com/gallery/{id}), which the [link] anchor of a gallery
// post points at.
func galleryID(dest string) (string, bool) {
	u, err := url.Parse(dest)
	if err != nil || !isRedditHost(u.Hostname()) {
		return "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 2 && parts[0] == "gallery" && parts[1] != "" {
		return parts[1], true
	}
	return "", false
}

// postID extracts the subreddit and post id from a reddit comments URL like
// /r/{sub}/comments/{id}/slug or /comments/{id}/slug.
func postID(link string) (sub, id string, ok bool) {
	u, err := url.Parse(link)
	if err != nil || !isRedditHost(u.Hostname()) {
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

// isImageURL reports whether raw points directly at an image, by URL path
// extension. The parsed path is used so signed URLs (photo.jpg?e=…&t=…) match.
func isImageURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Path == "" {
		return false
	}
	switch strings.ToLower(path.Ext(u.Path)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".avif", ".bmp":
		return true
	}
	return false
}

// fullImage rewrites a preview.redd.it/{id}.{ext} URL to the full-res
// i.redd.it/{id}.{ext} original. Reddit signs preview URLs per-size, but the
// i.redd.it original needs no signature and serves unauthenticated.
func fullImage(previewURL string) string {
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

// galleryImgRe matches a preview.redd.it URL and captures the file id and
// extension (e.g. .../whiskers-v0-9z8x7c6v.jpg).
var galleryImgRe = regexp.MustCompile(`preview\.redd\.it/[^"'\s]*?([A-Za-z0-9_]{6,})\.(jpe?g|png|gif)`)

// galleryImagesFromHTML extracts the gallery's image file ids from an embed
// page and rewrites them to full-res i.redd.it URLs.
func galleryImagesFromHTML(body []byte) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range galleryImgRe.FindAllSubmatch(body, -1) {
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

// extractLinkAnchor finds the anchor whose text is exactly "[link]" — reddit's
// marker for a post's destination — and returns its href.
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

// parseFeedLinks parses an Atom/RSS feed and maps each item's GUID to its
// "[link]" anchor href.
func parseFeedLinks(body []byte) (map[string]string, error) {
	parsed, err := gofeed.NewParser().Parse(bytes.NewReader(body))
	if err != nil {
		return nil, err
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
	return links, nil
}
