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
	"regexp"
	"strconv"
	"strings"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/mmcdole/gofeed"
	"golang.org/x/net/html"
)

// ErrNotModified is returned when the server answers 304 for a conditional GET.
var ErrNotModified = errors.New("not modified")

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

// Fetch retrieves and parses feedURL. When etag or lastModified are non-empty
// they are sent as conditional-GET headers; a 304 returns ErrNotModified.
func Fetch(ctx context.Context, feedURL string, client *http.Client, etag, lastModified string) (Result, error) {
	if isXProfileFeedURL(feedURL) {
		return fetchXProfile(ctx, feedURL, client)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("User-Agent", "nanoflux/0.1")
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/feed+json, application/xml, text/xml, */*")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if lastModified != "" {
		req.Header.Set("If-Modified-Since", lastModified)
	}

	resp, err := client.Do(req)
	if err != nil {
		if isYouTubeChannelFeed(feedURL) {
			return fetchYouTubeChannelViaBrowse(ctx, feedURL, client)
		}
		return Result{}, fmt.Errorf("get %s: %w", feedURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		return Result{}, ErrNotModified
	}
	if resp.StatusCode >= 400 {
		if isYouTubeChannelFeed(feedURL) {
			return fetchYouTubeChannelViaBrowse(ctx, feedURL, client)
		}
		return Result{}, fmt.Errorf("get %s: status %d", feedURL, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Result{}, fmt.Errorf("read %s: %w", feedURL, err)
	}

	parsed, err := gofeed.NewParser().Parse(bytes.NewReader(body))
	if err != nil {
		if isYouTubeChannelFeed(feedURL) {
			return fetchYouTubeChannelViaBrowse(ctx, feedURL, client)
		}
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
