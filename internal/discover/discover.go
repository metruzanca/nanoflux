// Package discover finds the RSS/Atom/JSON-feed for a web page. It tries, in
// order: parsing the URL itself, scanning the page's HTML for feed <link>s,
// host-specific rules (Bluesky, YouTube, Reddit, GitHub), and common feed
// paths. Candidates are only returned after they are fetched and parsed.
package discover

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/metruzanca/rss/internal/feedparse"
)

const maxBody = 4 << 20

// Candidate is one discovered feed.
type Candidate struct {
	FeedURL  string `json:"feed_url"`
	Title    string `json:"title,omitempty"`
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
	if c, ok := d.tryFeed(ctx, pageURL, "direct", pageURL); ok {
		return []Candidate{c}, nil
	}

	// 2. Scan the page HTML for feed <link>s.
	title, links := d.htmlLinks(ctx, pageURL)
	if cs := d.validateAll(ctx, links, "html", pageURL, title); len(cs) > 0 {
		return dedup(cs), nil
	}

	// 3. Host-specific rules.
	if cs := d.hostSpecific(ctx, pageURL); len(cs) > 0 {
		return dedup(cs), nil
	}

	// 4. Common feed paths on the same origin.
	var probes []string
	for _, p := range commonPaths {
		probes = append(probes, base.ResolveReference(&url.URL{Path: p}).String())
	}
	cs := d.validateAll(ctx, probes, "paths", pageURL, "")
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

func (d *Discoverer) tryFeed(ctx context.Context, feedURL, strategy, homeURL string) (Candidate, bool) {
	res, err := feedparse.Fetch(ctx, feedURL, d.client, "", "")
	if err != nil {
		return Candidate{}, false
	}
	return Candidate{
		FeedURL:  feedURL,
		Title:    res.Feed.Title,
		HomeURL:  homeURL,
		Strategy: strategy,
	}, true
}

func (d *Discoverer) validateAll(ctx context.Context, urls []string, strategy, homeURL, fallbackTitle string) []Candidate {
	var out []Candidate
	for _, u := range urls {
		if len(out) >= 5 {
			break
		}
		c, ok := d.tryFeed(ctx, u, strategy, homeURL)
		if !ok {
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

// htmlLinks fetches pageURL and returns its <title> and any feed <link> hrefs,
// resolved against the page URL.
func (d *Discoverer) htmlLinks(ctx context.Context, pageURL string) (string, []string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return "", nil
	}
	req.Header.Set("User-Agent", "rss/0.1")
	resp, err := d.client.Do(req)
	if err != nil {
		return "", nil
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", nil
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" && !strings.Contains(ct, "html") && !strings.Contains(ct, "xml") {
		return "", nil
	}

	base, _ := url.Parse(pageURL)
	z := html.NewTokenizer(io.LimitReader(resp.Body, maxBody))
	var title string
	inTitle := false
	var links []string
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return title, links
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
				if href == "" {
					continue
				}
				if isFeedLink(rel, typ) {
					ref, err := url.Parse(href)
					if err != nil {
						continue
					}
					if u := base.ResolveReference(ref); u.IsAbs() {
						links = append(links, u.String())
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
