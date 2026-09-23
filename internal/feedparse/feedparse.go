// Package feedparse fetches and normalizes RSS/Atom/JSON feeds into the
// app's item model.
package feedparse

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/mmcdole/gofeed"
	"golang.org/x/net/html"
)

// ErrNotModified is returned when the server answers 304 for a conditional GET.
var ErrNotModified = errors.New("not modified")

// StatusError is an HTTP response status >= 400 from a feed or page fetch. It
// carries the code separately so callers can turn it into a user-facing message
// (e.g. a 429 is a rate limit, not a missing feed) without parsing the string.
type StatusError struct {
	Code int
	URL  string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("get %s: status %d", e.URL, e.Code)
}

// Feed carries normalized feed-level metadata.
type Feed struct {
	Title       string
	HomeURL     string
	Description string
	ImageURL    string
}

// Enclosure is one media attachment (podcast episode, video, file) on an item.
type Enclosure struct {
	URL      string
	MIMEType string
	Length   int64
}

// Item is one normalized entry.
type Item struct {
	GUID        string
	Title       string
	Link        string
	Summary     string
	ImageURL    string
	PublishedAt string // "" when unknown
	Enclosures  []Enclosure
}

// Result is the normalized output of a successful fetch.
type Result struct {
	Feed         Feed
	Items        []Item
	ETag         string
	LastModified string
	// NextPageURL is the feed's advertised next page, or "" when the feed is
	// not paginated / this is the last page. Used by "load older items".
	NextPageURL string
}

// Plugin is a feed integration installed by the plugin host. When set, Fetch
// defers to it for feeds it matches, before the generic parser.
type Plugin interface {
	// MatchFetch reports whether the plugin handles the request's feed.
	MatchFetch(req FetchRequest) bool
	// FetchPlugin fetches the feed via the plugin.
	FetchPlugin(ctx context.Context, req FetchRequest) (Result, error)
}

// FetchRequest is one feed fetch: the URL and conditional-GET validators.
type FetchRequest struct {
	URL          string
	ETag         string
	LastModified string
}

var installedPlugin Plugin

// SetPlugin installs the plugin dispatcher (called once at startup by the
// plugin package). A nil plugin disables plugin routing.
func SetPlugin(p Plugin) { installedPlugin = p }

// Fetch retrieves and parses feedURL. When etag or lastModified are non-empty
// they are sent as conditional-GET headers; a 304 returns ErrNotModified.
//
// Site-specific integrations (X, Instagram, Patreon, …) are handled by plugins,
// installed via SetPlugin; this generic path covers standard RSS/Atom/JSON feeds.
func Fetch(ctx context.Context, feedURL string, client *http.Client, etag, lastModified string) (Result, error) {
	return FetchFeed(ctx, FetchRequest{URL: feedURL, ETag: etag, LastModified: lastModified}, client)
}

// FetchFeed fetches a feed described by req, routing to a matching plugin first.
func FetchFeed(ctx context.Context, req FetchRequest, client *http.Client) (Result, error) {
	if installedPlugin != nil && installedPlugin.MatchFetch(req) {
		return installedPlugin.FetchPlugin(ctx, req)
	}
	feedURL, etag, lastModified := req.URL, req.ETag, req.LastModified
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return Result{}, err
	}
	httpReq.Header.Set("User-Agent", UserAgent())
	httpReq.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/feed+json, application/xml, text/xml, */*")
	if etag != "" {
		httpReq.Header.Set("If-None-Match", etag)
	}
	if lastModified != "" {
		httpReq.Header.Set("If-Modified-Since", lastModified)
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return Result{}, fmt.Errorf("get %s: %w", feedURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		return Result{}, ErrNotModified
	}
	if isRateLimited(resp) {
		return Result{}, &RateLimitError{URL: feedURL, Status: resp.StatusCode, RetryAfter: rateLimitBackoff(resp)}
	}
	if resp.StatusCode >= 400 {
		return Result{}, &StatusError{Code: resp.StatusCode, URL: feedURL}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Result{}, fmt.Errorf("read %s: %w", feedURL, err)
	}

	parsed, err := gofeed.NewParser().Parse(bytes.NewReader(body))
	if err != nil {
		return Result{}, fmt.Errorf("parse %s: %w", feedURL, err)
	}
	if parsed == nil {
		return Result{}, errors.New("empty feed")
	}

	res := Result{
		Feed: Feed{
			Title:       parsed.Title,
			HomeURL:     parsed.Link,
			Description: parsed.Description,
			ImageURL:    imageURL(parsed.Image),
		},
		ETag:         resp.Header.Get("ETag"),
		LastModified: resp.Header.Get("Last-Modified"),
	}
	if next := nextPageFromBody(body, feedURL); next != "" {
		res.NextPageURL = next
	} else {
		res.NextPageURL = nextPageByParam(feedURL)
	}
	for _, it := range parsed.Items {
		res.Items = append(res.Items, normalizeItem(it))
	}
	return res, nil
}

// nextLinkPatterns match a feed-level <link rel="next" href="..."> (Atom, or
// RSS carrying an atom:link) in either attribute order. Only feed-level links
// carry a rel attribute, so matching rel="next" cannot collide with an item's
// <link> element.
var nextLinkPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?is)<(?:\w+:)?link\b[^>]*?\brel\s*=\s*["']next["'][^>]*?\bhref\s*=\s*["']([^"']+)["']`),
	regexp.MustCompile(`(?is)<(?:\w+:)?link\b[^>]*?\bhref\s*=\s*["']([^"']+)["'][^>]*?\brel\s*=\s*["']next["']`),
}

// nextPageFromBody extracts the feed-level rel="next" link from the raw feed
// document, resolved against currentURL (the page that was fetched). The scan
// is skipped for documents larger than nextLinkScanMax because the regexes are
// cheap but not free on multi-megabyte bodies.
func nextPageFromBody(body []byte, currentURL string) string {
	const nextLinkScanMax = 4 << 20
	if len(body) == 0 || len(body) > nextLinkScanMax {
		return ""
	}
	base, err := url.Parse(currentURL)
	if err != nil {
		return ""
	}
	for _, re := range nextLinkPatterns {
		for _, m := range re.FindAllSubmatch(body, -1) {
			href := string(m[1])
			if href == "" {
				continue
			}
			ref, err := url.Parse(href)
			if err != nil {
				continue
			}
			return base.ResolveReference(ref).String()
		}
	}
	return ""
}

// nextPageByParam returns currentURL with an existing page or paged query
// parameter incremented, or "" when the URL carries neither. This is the
// fallback for feeds that paginate via a ?page=N convention without
// advertising a rel="next" link.
func nextPageByParam(currentURL string) string {
	u, err := url.Parse(currentURL)
	if err != nil {
		return ""
	}
	q := u.Query()
	for _, key := range []string{"page", "paged"} {
		v := q.Get(key)
		if v == "" {
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			continue
		}
		q.Set(key, strconv.Itoa(n+1))
		u.RawQuery = q.Encode()
		return u.String()
	}
	return ""
}

// isImageEnclosure reports whether an enclosure is an image, by MIME type or
// URL extension. The URL's parsed path is used so signed URLs
// ("photo.jpg?e=…&t=…") still match.
func isImageEnclosure(e Enclosure) bool {
	if strings.HasPrefix(strings.ToLower(e.MIMEType), "image/") {
		return true
	}
	u, err := url.Parse(e.URL)
	if err != nil || u.Path == "" {
		return false
	}
	switch strings.ToLower(path.Ext(u.Path)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".avif", ".svg", ".bmp":
		return true
	}
	return false
}

// firstImageEnclosureURL returns the URL of an item's first image enclosure,
// or "" when it has none.
func firstImageEnclosureURL(encs []Enclosure) string {
	for _, e := range encs {
		if isImageEnclosure(e) {
			return e.URL
		}
	}
	return ""
}

func normalizeItem(it *gofeed.Item) Item {
	out := Item{
		Title: it.Title,
		Link:  it.Link,
	}
	out.GUID = it.GUID
	if out.GUID == "" {
		out.GUID = it.Link
	}
	if out.GUID == "" {
		// Last resort: a stable hash so dedup still works.
		sum := sha1.Sum([]byte(it.Title + it.Content))
		out.GUID = hex.EncodeToString(sum[:])
	}
	if d := strings.TrimSpace(it.Description); d != "" {
		out.Summary = d
	} else {
		out.Summary = it.Content
	}
	if it.Image != nil {
		out.ImageURL = StripTracking(it.Image.URL)
	}
	// Prefer the first image actually embedded in the post body: it is the
	// real content, and many feeds ship a tiny media:thumbnail crop (e.g.
	// Blogger's 72px s72-c) while the body holds the full-size image.
	if out.ImageURL == "" {
		out.ImageURL = StripTracking(firstSummaryImageURL(it))
	}
	if out.ImageURL == "" {
		out.ImageURL = StripTracking(mediaThumbnailURL(it))
	}
	for _, e := range it.Enclosures {
		if e == nil || e.URL == "" {
			continue
		}
		length, _ := strconv.ParseInt(e.Length, 10, 64)
		out.Enclosures = append(out.Enclosures, Enclosure{URL: StripTracking(e.URL), MIMEType: e.Type, Length: length})
	}
	// Some feeds ship images only as enclosures with no media:thumbnail, so a
	// text item would otherwise have no image. Fall back to the first image
	// enclosure so it renders a thumbnail in lists (and the masonry grid).
	if out.ImageURL == "" {
		out.ImageURL = firstImageEnclosureURL(out.Enclosures)
	}
	switch {
	case it.PublishedParsed != nil:
		out.PublishedAt = db.FormatTime(*it.PublishedParsed)
	case it.UpdatedParsed != nil:
		out.PublishedAt = db.FormatTime(*it.UpdatedParsed)
	}
	// The GUID is derived from the raw link above; the stored link is the
	// cleaned one so tracking params never leak to the client.
	out.Link = StripTracking(out.Link)
	return out
}

// trackingParams are query-string keys that exist only for marketing and
// analytics. They are dropped from stored links for privacy (Miniflux does the
// same). Generic keys like "ref" or "s" are left alone — they are often
// functional.
var trackingParams = []string{
	"utm_source", "utm_medium", "utm_campaign", "utm_term", "utm_content",
	"utm_id", "utm_visitor_id", "utm_reader", "utm_ref",
	"fbclid", "gclid", "dclid", "gbraid", "wbraid",
	"mc_cid", "mc_eid", "igshid", "ref_src", "ref_url", "spm",
}

// StripTracking removes well-known tracking parameters from a URL. The URL is
// returned unchanged when it has no query string or cannot be parsed.
func StripTracking(raw string) string {
	if raw == "" {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	changed := false
	for _, k := range trackingParams {
		if _, ok := q[k]; ok {
			q.Del(k)
			changed = true
		}
	}
	if !changed {
		return raw
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// PlainText extracts visible text from feed-provided HTML, for rule matching
// and plain-text contexts.
func PlainText(s string) string {
	if s == "" || !strings.Contains(s, "<") {
		return strings.TrimSpace(s)
	}
	doc, err := html.Parse(strings.NewReader(s))
	if err != nil {
		return s
	}
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
	for c := doc.FirstChild; c != nil; c = c.NextSibling {
		walk(c)
	}
	return strings.TrimSpace(b.String())
}

func imageURL(img *gofeed.Image) string {
	if img == nil {
		return ""
	}
	return img.URL
}

// mediaThumbnailURL returns the first media:thumbnail URL on an item, from a
// direct <media:thumbnail> child or nested inside <media:group> (the shape
// YouTube uses). gofeed's Item.Image ignores media:thumbnail, so feeds that
// advertise their artwork only through it (YouTube, podcasts, ...) need this
// fallback.
func mediaThumbnailURL(it *gofeed.Item) string {
	media, ok := it.Extensions["media"]
	if !ok {
		return ""
	}
	for _, g := range media["group"] {
		for _, th := range g.Children["thumbnail"] {
			if u := th.Attrs["url"]; u != "" {
				return u
			}
		}
	}
	for _, th := range media["thumbnail"] {
		if u := th.Attrs["url"]; u != "" {
			return u
		}
	}
	return ""
}

// firstSummaryImageURL returns the src of the first <img> in an item's
// summary HTML. Many feeds embed their actual images in the content (Blogger
// being the clearest example) with only a tiny media:thumbnail alongside, so
// the body's first image is a better listing thumbnail. Returns "" when the
// summary has no <img> or cannot be parsed.
func firstSummaryImageURL(it *gofeed.Item) string {
	body := it.Description
	if body == "" {
		body = it.Content
	}
	if body == "" || !strings.Contains(body, "<") {
		return ""
	}
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return ""
	}
	var find func(*html.Node) string
	find = func(n *html.Node) string {
		if n.Type == html.ElementNode && n.Data == "img" {
			for _, a := range n.Attr {
				if a.Key == "src" && a.Val != "" {
					return a.Val
				}
			}
			return ""
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if u := find(c); u != "" {
				return u
			}
		}
		return ""
	}
	for c := doc.FirstChild; c != nil; c = c.NextSibling {
		if u := find(c); u != "" {
			return u
		}
	}
	return ""
}
