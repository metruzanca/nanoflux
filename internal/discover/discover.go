// Package discover finds the RSS/Atom/JSON-feed for a web page. It tries, in
// order: parsing the URL itself, scanning the page's HTML for feed <link>s,
// host-specific rules (Bluesky, YouTube, Reddit, GitHub), and common feed
// paths. Candidates are only returned after they are fetched and parsed.
package discover

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/metruzanca/nanoflux/internal/feedparse"
)

const maxBody = 4 << 20

// Candidate is one discovered feed.
type Candidate struct {
	FeedURL  string `json:"feed_url"`
	Title    string `json:"title,omitempty"`
	IconURL  string `json:"icon_url,omitempty"` // author avatar / site icon (plugin-supplied)
	HomeURL  string `json:"home_url,omitempty"`
	Strategy string `json:"strategy"`
}

// Discoverer finds feeds for URLs.
type Discoverer struct {
	client *http.Client
}

func New(client *http.Client) *Discoverer {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	return &Discoverer{client: client}
}

// Discover returns the feeds for pageURL, or an empty slice when none is found.
func (d *Discoverer) Discover(ctx context.Context, pageURL string) ([]Candidate, error) {
	base, err := url.Parse(pageURL)
	if err != nil {
		return nil, err
	}
	if base.Scheme == "" || base.Host == "" {
		return nil, errors.New("invalid url")
	}

	// 1. The URL itself might already be a feed.
	if c, err := d.tryFeed(ctx, pageURL, "direct", pageURL); err == nil {
		return []Candidate{c}, nil
	}

	// 2. Host-specific rules (GitHub, Reddit, Bluesky, YouTube). These are the
	// site's canonical feeds (e.g. a GitHub profile's activity atom, a repo's
	// releases/commits/tags), so they take precedence over whatever the page's
	// HTML advertises — and may carry a fixed display title.
	cs, hostErr := d.hostSpecific(ctx, pageURL)
	if len(cs) > 0 {
		return dedup(cs), nil
	}

	// 3. Scan the page HTML for feed <link>s.
	title, links, pageErr := d.htmlLinks(ctx, pageURL)
	if cs := d.validateAll(ctx, links, "html", pageURL, title); len(cs) > 0 {
		return dedup(cs), nil
	}

	// 4. Common feed paths on the same origin.
	var probes []string
	for _, p := range commonPaths {
		probes = append(probes, base.ResolveReference(&url.URL{Path: p}).String())
	}
	cs = d.validateAll(ctx, probes, "paths", pageURL, "")
	if len(cs) == 0 {
		// Nothing found: surface why when we can, instead of a bare "no feed
		// found". A known host rule that failed (e.g. reddit rate-limiting its
		// .rss probe) takes precedence, then a page-fetch failure (e.g. 429).
		if hostErr != nil {
			return nil, hostErr
		}
		if pageErr != nil {
			return nil, pageErr
		}
	}
	return dedup(cs), nil
}

var commonPaths = []string{
	"/feed",
	"/feed.xml",
	"/rss",
	"/rss.xml",
	"/atom.xml",
	"/index.xml",
	"/feeds/posts/default",
	"/?feed=rss",
}

func (d *Discoverer) tryFeed(ctx context.Context, feedURL, strategy, homeURL string) (Candidate, error) {
	res, err := feedparse.Fetch(ctx, feedURL, d.client, "", "")
	if err != nil {
		return Candidate{}, err
	}
	return Candidate{
		FeedURL:  feedURL,
		Title:    res.Feed.Title,
		HomeURL:  homeURL,
		Strategy: strategy,
	}, nil
}

// validateAll fetches each candidate and returns the ones that parse as feeds,
// along with the last non-fatal fetch error (so a caller can explain why a
// probe failed — e.g. a rate limit — rather than reporting "no feed").
func (d *Discoverer) validateAll(ctx context.Context, urls []string, strategy, homeURL, fallbackTitle string) []Candidate {
	var out []Candidate
	for _, u := range urls {
		if len(out) >= 5 {
			break
		}
		c, err := d.tryFeed(ctx, u, strategy, homeURL)
		if err != nil {
			continue
		}
		if c.Title == "" {
			c.Title = fallbackTitle
		}
		out = append(out, c)
	}
	return out
}

func dedup(cs []Candidate) []Candidate {
	seen := map[string]bool{}
	var out []Candidate
	for _, c := range cs {
		if seen[c.FeedURL] {
			continue
		}
		seen[c.FeedURL] = true
		out = append(out, c)
	}
	return out
}

// PageMeta is the basic metadata discoverable from a page's HTML.
type PageMeta struct {
	Title   string
	IconURL string
	HomeURL string
}

// pageTitle returns a page's display title with YouTube's " - YouTube" and
// Patreon's " — creating … | Patreon" suffixes trimmed, so it can name an
// author cleanly. Other sites are untouched.
func pageTitle(rawurl, title string) string {
	if isYouTubePage(rawurl) {
		return strings.TrimSuffix(title, " - YouTube")
	}
	if isPatreonPage(rawurl) {
		return patreonPageName(title)
	}
	return title
}

// patreonPageName strips the suffix Patreon appends to a creator page's
// <title> so the author prefill reads cleanly. Two shapes appear:
// "Chris & Jack — creating Sketch Comedy Videos | Patreon" and
// "Smarter Every Day | Creating Science Videos | Patreon"; the creator name is
// everything before the first " | " (then before any " — ").
func patreonPageName(title string) string {
	if i := strings.Index(title, " | "); i >= 0 {
		title = title[:i]
	}
	if i := strings.Index(title, " — "); i >= 0 {
		title = title[:i]
	}
	return strings.TrimSpace(title)
}

// stripCDATA unwraps raw CDATA sections from text extracted by the HTML
// tokenizer. When the fetched URL is itself an XML feed, a `<title>` may be
// served as `<![CDATA[name]]>`; the tokenizer hands that through verbatim, so
// author names would otherwise show the markers. Content is kept, markers are
// dropped, and multiple/embedded sections are handled.
func stripCDATA(s string) string {
	const start, end = "<![CDATA[", "]]>"
	for {
		i := strings.Index(s, start)
		if i < 0 {
			return s
		}
		rest := s[i+len(start):]
		j := strings.Index(rest, end)
		if j < 0 {
			// Unterminated section: drop the marker, keep the text.
			s = s[:i] + rest
			return s
		}
		s = s[:i] + rest[:j] + rest[j+len(end):]
	}
}

// PageMeta fetches pageURL and extracts its <title> and site icon (favicon).
// Falls back to /favicon.ico on the host when no icon link is present.
func (d *Discoverer) PageMeta(ctx context.Context, pageURL string) (PageMeta, error) {
	body, base, err := d.openPage(ctx, pageURL)
	if err != nil {
		return PageMeta{}, err
	}
	defer body.Close()

	meta := PageMeta{HomeURL: pageURL}
	isYT := isYouTubePage(pageURL)
	isPatreon := isPatreonPage(pageURL)
	z := html.NewTokenizer(io.LimitReader(body, maxBody))
	inTitle := false
	inStructured := false
	var ogImage, structured string
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			// YouTube channel pages title themselves "<Channel> - YouTube";
			// drop the suffix so it can name the author cleanly. A feed served
			// directly may wrap its <title> in CDATA, which the HTML tokenizer
			// passes through raw.
			meta.Title = stripCDATA(pageTitle(pageURL, meta.Title))
			// A YouTube channel page's real avatar is its og:image
			// (yt3.googleusercontent.com), not the hashed build favicon.
			if isYT && ogImage != "" {
				meta.IconURL = ogImage
			}
			// A Patreon creator page exposes its avatar only in its
			// structured-data JSON-LD; the generic favicon would otherwise win.
			if isPatreon {
				if avatar := patreonStructuredAvatar(structured); avatar != "" {
					meta.IconURL = avatar
				}
			}
			if meta.IconURL == "" {
				meta.IconURL = fallbackIcon(pageURL)
			}
			return meta, nil
		case html.TextToken:
			if inTitle && meta.Title == "" {
				meta.Title = strings.TrimSpace(z.Token().Data)
			}
			if inStructured && structured == "" {
				structured = z.Token().Data
			}
		case html.StartTagToken, html.SelfClosingTagToken:
			t := z.Token()
			switch t.Data {
			case "title":
				inTitle = true
			case "script":
				if isPatreon {
					for _, a := range t.Attr {
						if a.Key == "type" && strings.Contains(strings.ToLower(a.Val), "ld+json") {
							inStructured = true
						}
					}
				}
			case "link":
				var rel, href string
				for _, a := range t.Attr {
					switch a.Key {
					case "rel":
						rel = strings.ToLower(a.Val)
					case "href":
						href = a.Val
					}
				}
				if meta.IconURL == "" && href != "" && strings.Contains(rel, "icon") {
					meta.IconURL = resolveURL(base, href)
				}
			case "meta":
				var prop, content string
				for _, a := range t.Attr {
					switch a.Key {
					case "property":
						prop = strings.ToLower(a.Val)
					case "content":
						content = a.Val
					}
				}
				if ogImage == "" && prop == "og:image" && content != "" {
					ogImage = content
				}
			}
		case html.EndTagToken:
			switch z.Token().Data {
			case "title":
				inTitle = false
			case "script":
				inStructured = false
			}
		}
	}
}

// isPatreonPage reports whether a URL points at a Patreon creator page.
func isPatreonPage(rawurl string) bool {
	u, err := url.Parse(rawurl)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host != "patreon.com" && host != "www.patreon.com" {
		return false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 2 && parts[0] == "cw" && parts[1] != "" {
		return true
	}
	if len(parts) == 1 && parts[0] != "" {
		switch parts[0] {
		case "api", "user", "posts", "login", "signup", "join", "home",
			"explore", "search", "settings", "notifications", "messages":
			return false
		}
		return true
	}
	return false
}

// patreonStructuredAvatar extracts the creator avatar from a Patreon page's
// structured-data JSON-LD mainEntity image. It returns "" when absent.
func patreonStructuredAvatar(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var doc struct {
		MainEntity struct {
			Image struct {
				ContentURL   string `json:"contentUrl"`
				ThumbnailURL string `json:"thumbnailUrl"`
			} `json:"image"`
		} `json:"mainEntity"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return ""
	}
	if doc.MainEntity.Image.ContentURL != "" {
		return doc.MainEntity.Image.ContentURL
	}
	return doc.MainEntity.Image.ThumbnailURL
}

// openPage fetches pageURL and returns its body, the resolved base URL, and an
// error for non-HTML or error responses.
func (d *Discoverer) openPage(ctx context.Context, pageURL string) (io.ReadCloser, *url.URL, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", feedparse.UserAgent())
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode >= 400 {
		resp.Body.Close()
		return nil, nil, &feedparse.StatusError{Code: resp.StatusCode, URL: pageURL}
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" && !strings.Contains(ct, "html") && !strings.Contains(ct, "xml") {
		resp.Body.Close()
		return nil, nil, errors.New("not an html page")
	}
	base, _ := url.Parse(pageURL)
	return resp.Body, base, nil
}

func resolveURL(base *url.URL, href string) string {
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

func fallbackIcon(pageURL string) string {
	u, err := url.Parse(pageURL)
	if err != nil {
		return ""
	}
	u.Path = "/favicon.ico"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

// htmlLinks fetches pageURL and returns its <title>, any feed <link> hrefs
// (resolved against the page URL), and the page-fetch error when the page could
// not be read (so a rate limit or server error can be surfaced to the user).
func (d *Discoverer) htmlLinks(ctx context.Context, pageURL string) (string, []string, error) {
	body, base, err := d.openPage(ctx, pageURL)
	if err != nil {
		return "", nil, err
	}
	defer body.Close()

	z := html.NewTokenizer(io.LimitReader(body, maxBody))
	var title string
	inTitle := false
	var links []string
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return stripCDATA(title), links, nil
		case html.TextToken:
			if inTitle && title == "" {
				title = strings.TrimSpace(z.Token().Data)
			}
		case html.StartTagToken, html.SelfClosingTagToken:
			t := z.Token()
			switch t.Data {
			case "title":
				inTitle = true
			case "link":
				var rel, typ, href string
				for _, a := range t.Attr {
					switch a.Key {
					case "rel":
						rel = strings.ToLower(a.Val)
					case "type":
						typ = strings.ToLower(a.Val)
					case "href":
						href = a.Val
					}
				}
				if href != "" && isFeedLink(rel, typ) {
					if u := resolveURL(base, href); u != "" {
						links = append(links, u)
					}
				}
			}
		case html.EndTagToken:
			if z.Token().Data == "title" {
				inTitle = false
			}
		}
	}
}

func isFeedLink(rel, typ string) bool {
	if strings.Contains(rel, "feed") {
		return true
	}
	if !strings.Contains(rel, "alternate") {
		return false
	}
	switch typ {
	case "application/rss+xml", "application/atom+xml", "application/feed+json",
		"application/xml", "text/xml", "application/rdf+xml":
		return true
	}
	return false
}
