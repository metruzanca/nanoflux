// Package feedparse fetches and normalizes RSS/Atom/JSON feeds into the
// app's item model.
package feedparse

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
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

	parsed, err := gofeed.NewParser().Parse(resp.Body)
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
	for _, it := range parsed.Items {
		res.Items = append(res.Items, normalizeItem(it))
	}
	return res, nil
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
