package feedparse

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/andybalholm/cascadia"
	"github.com/metruzanca/nanoflux/internal/db"
	"golang.org/x/net/html"
)

// ScrapeConfig describes how to extract items from a scraped HTML page. Each
// field is a CSS selector scoped to the item container (Link/Title/etc.), or
// empty to skip that field. Item is required. Link is required in practice —
// items without a link get a stable hash GUID so dedup still works, but the
// builder warns when nothing has one.
type ScrapeConfig struct {
	Item    string `json:"item"`
	Title   string `json:"title"`
	Link    string `json:"link"`
	Summary string `json:"summary"`
	Date    string `json:"date"`
	Image   string `json:"image"`
}

// ParseScrapeConfig decodes a stored scrape feed's selector JSON.
func ParseScrapeConfig(raw string) (ScrapeConfig, error) {
	var cfg ScrapeConfig
	if strings.TrimSpace(raw) == "" {
		return cfg, errors.New("scrape config is empty")
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return cfg, fmt.Errorf("scrape config: %w", err)
	}
	if strings.TrimSpace(cfg.Item) == "" {
		return cfg, errors.New("item selector is required")
	}
	return cfg, nil
}

// scrapePageCap bounds how much of a scraped page is read, mirroring the
// discovery package's body cap.
const scrapePageCap = 4 << 20

// scrapeItemCap bounds how many items a scraped page can yield per poll.
const scrapeItemCap = 200

// Scrape fetches pageURL, applies cfg's CSS selectors to extract items, and
// normalizes them into the app's item model. Conditional-GET headers are sent
// like Fetch, so a 304 returns ErrNotModified.
func Scrape(ctx context.Context, pageURL string, client *http.Client, cfg ScrapeConfig, etag, lastModified string) (Result, error) {
	sel, err := cascadia.Compile(cfg.Item)
	if err != nil {
		return Result{}, fmt.Errorf("item selector: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("User-Agent", UserAgent())
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if lastModified != "" {
		req.Header.Set("If-Modified-Since", lastModified)
	}

	resp, err := client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("get %s: %w", pageURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return Result{}, ErrNotModified
	}
	if isRateLimited(resp) {
		return Result{}, &RateLimitError{URL: pageURL, Status: resp.StatusCode, RetryAfter: rateLimitBackoff(resp)}
	}
	if resp.StatusCode >= 400 {
		return Result{}, &StatusError{Code: resp.StatusCode, URL: pageURL}
	}

	doc, err := html.Parse(io.LimitReader(resp.Body, scrapePageCap))
	if err != nil {
		return Result{}, fmt.Errorf("parse %s: %w", pageURL, err)
	}

	base, _ := url.Parse(pageURL)
	nodes := cascadia.QueryAll(doc, sel)
	if len(nodes) == 0 {
		return Result{}, errors.New("no items match the item selector")
	}

	title := pageTitle(doc)
	if title == "" {
		title = base.Host
	}

	res := Result{
		Feed:         Feed{Title: title, HomeURL: pageURL},
		ETag:         resp.Header.Get("ETag"),
		LastModified: resp.Header.Get("Last-Modified"),
	}
	for _, n := range nodes {
		if len(res.Items) >= scrapeItemCap {
			break
		}
		if it, ok := scrapeItem(n, base, cfg); ok {
			res.Items = append(res.Items, it)
		}
	}
	if len(res.Items) == 0 {
		return Result{}, errors.New("no items extracted from the page")
	}
	return res, nil
}

// pageTitle returns the document's <title> text, or "".
func pageTitle(doc *html.Node) string {
	var walk func(*html.Node) string
	walk = func(n *html.Node) string {
		if n.Type == html.ElementNode && n.Data == "title" {
			return strings.TrimSpace(nodeText(n))
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if t := walk(c); t != "" {
				return t
			}
		}
		return ""
	}
	return walk(doc)
}

// nodeText returns the concatenated text of a node's subtree.
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
	return strings.TrimSpace(b.String())
}

// attr returns a node's attribute value, or "".
func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return strings.TrimSpace(a.Val)
		}
	}
	return ""
}

// scrapeItem extracts one item from a matched container node using cfg's
// selectors, resolving relative URLs against base. ok is false when the item
// has neither a link nor a title, so junk nodes (nav, ads) don't pollute the
// feed.
func scrapeItem(n *html.Node, base *url.URL, cfg ScrapeConfig) (Item, bool) {
	it := Item{}
	it.Title = firstMatchText(n, cfg.Title)
	link := firstMatchAttr(n, cfg.Link, "href")

	// Fall back to the first in-item link when no link selector is set or it
	// matched nothing.
	if link == "" {
		if a := firstElem(n, "a[href]"); a != nil {
			link = attr(a, "href")
		}
	}
	if link != "" {
		it.Link = StripTracking(resolveURL(base, link))
	}
	if it.Title == "" {
		// Fall back to the link's text so a bare "a" selector still yields
		// readable titles.
		if a := firstElem(n, "a[href]"); a != nil {
			it.Title = nodeText(a)
		}
	}
	if it.Link == "" && it.Title == "" {
		return Item{}, false
	}

	it.GUID = "scrape:" + stableHash(it.Link, it.Title)
	it.Summary = firstMatchText(n, cfg.Summary)
	if img := firstMatchAttr(n, cfg.Image, "src"); img != "" {
		it.ImageURL = StripTracking(resolveURL(base, img))
	} else if im := firstElem(n, "img[src]"); im != nil {
		it.ImageURL = StripTracking(resolveURL(base, attr(im, "src")))
	}
	it.PublishedAt = scrapeDate(n, cfg.Date)
	return it, true
}

// scrapeDate extracts a publish time from the date selector's element: the
// datetime attribute wins, then its text. Returns "" when unparseable.
func scrapeDate(n *html.Node, dateSel string) string {
	var el *html.Node
	if dateSel != "" {
		if s, err := cascadia.Compile(dateSel); err == nil {
			el = cascadia.Query(n, s)
		}
	}
	if el == nil {
		el = firstElem(n, "time[datetime], time")
	}
	if el == nil {
		return ""
	}
	raw := attr(el, "datetime")
	if raw == "" {
		raw = nodeText(el)
	}
	t, ok := parseDate(raw)
	if !ok {
		return ""
	}
	return db.FormatTime(t)
}

// parseDate tries the common formats scraped sites use for datetimes.
func parseDate(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02",
		"02 Jan 2006 15:04",
		"02 Jan 2006",
		"January 2, 2006",
		"Jan 2, 2006",
		"2 January 2006",
		"2 Jan 2006",
		time.RFC1123,
		time.RFC1123Z,
	} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t, true
		}
	}
	// ISO dates like 2024-05-01T00:00:00+02:00 with a zone are RFC3339; a bare
	// date-only value is already covered above.
	return time.Time{}, false
}

// firstMatchText returns the text of the first element matching sel within n,
// or "".
func firstMatchText(n *html.Node, sel string) string {
	if sel == "" {
		return ""
	}
	s, err := cascadia.Compile(sel)
	if err != nil {
		return ""
	}
	if m := cascadia.Query(n, s); m != nil {
		return nodeText(m)
	}
	return ""
}

// firstMatchAttr returns the named attribute of the first element matching sel
// within n, or "".
func firstMatchAttr(n *html.Node, sel, key string) string {
	if sel == "" {
		return ""
	}
	s, err := cascadia.Compile(sel)
	if err != nil {
		return ""
	}
	if m := cascadia.Query(n, s); m != nil {
		return attr(m, key)
	}
	return ""
}

// firstElem returns the first element matching sel within n's subtree
// (including n itself).
func firstElem(n *html.Node, sel string) *html.Node {
	s, err := cascadia.Compile(sel)
	if err != nil {
		return nil
	}
	return cascadia.Query(n, s)
}

// stableHash derives a stable item GUID from its link and title, mirroring the
// hash fallback in normalizeItem.
func stableHash(link, title string) string {
	sum := sha1.Sum([]byte(link + title))
	return hex.EncodeToString(sum[:])
}

// resolveURL resolves href against base and returns an absolute URL, or "" when
// the result isn't absolute.
func resolveURL(base *url.URL, href string) string {
	href = strings.TrimSpace(href)
	if href == "" || strings.HasPrefix(strings.ToLower(href), "javascript:") {
		return ""
	}
	ref, err := url.Parse(href)
	if err != nil {
		return ""
	}
	u := base.ResolveReference(ref)
	if !u.IsAbs() {
		return ""
	}
	return u.String()
}

// autoPresets are container selectors tried in order by AutoDetect, most
// specific first.
var autoPresets = []string{
	"article",
	".post",
	".entry",
	".blog-post",
	".hentry",
	".news-item",
	".list-item",
}

// AutoDetect guesses a workable scrape config for pageURL by trying common
// article-list containers and picking the first whose item selector yields at
// least 2 items with links. Title/link/date selectors are left empty so Scrape
// falls back to per-item heuristics.
func AutoDetect(ctx context.Context, pageURL string, client *http.Client) (ScrapeConfig, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return ScrapeConfig{}, err
	}
	req.Header.Set("User-Agent", UserAgent())
	resp, err := client.Do(req)
	if err != nil {
		return ScrapeConfig{}, fmt.Errorf("get %s: %w", pageURL, err)
	}
	defer resp.Body.Close()
	if isRateLimited(resp) {
		return ScrapeConfig{}, &RateLimitError{URL: pageURL, Status: resp.StatusCode, RetryAfter: rateLimitBackoff(resp)}
	}
	if resp.StatusCode >= 400 {
		return ScrapeConfig{}, &StatusError{Code: resp.StatusCode, URL: pageURL}
	}
	doc, err := html.Parse(io.LimitReader(resp.Body, scrapePageCap))
	if err != nil {
		return ScrapeConfig{}, fmt.Errorf("parse %s: %w", pageURL, err)
	}

	for _, itemSel := range autoPresets {
		sel, err := cascadia.Compile(itemSel)
		if err != nil {
			continue
		}
		links := 0
		for _, n := range cascadia.QueryAll(doc, sel) {
			if a := firstElem(n, "a[href]"); a != nil && attr(a, "href") != "" {
				links++
			}
		}
		if links >= 2 {
			return ScrapeConfig{Item: itemSel}, nil
		}
	}
	return ScrapeConfig{}, errors.New("could not guess a selector; use the manual fields")
}
