// Package oembed resolves third-party pages to their embeddable player via
// the oEmbed spec. It performs oEmbed discovery on a page URL (find the
// <link rel="alternate" type="application/json+oembed">), fetches the
// provider's JSON, and extracts a safe iframe from the returned html. No
// provider is hardcoded: anything that publishes an oEmbed endpoint works
// (imgur, vimeo, youtube, ...). Results are cached briefly so the
// item modal does not re-fetch on every open.
package oembed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
)

// ErrNotFound is returned when the page exposes no usable oEmbed endpoint or
// the provider response carries no embeddable iframe.
var ErrNotFound = errors.New("no oEmbed endpoint")

const bodyCap = 4 << 20

// BrowserUserAgent is used for oEmbed discovery requests; some providers
// (reddit) reject generic crawler agents.
const BrowserUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36"

// Embed is an embeddable third-party player.
type Embed struct {
	Src      string // iframe src (always http(s))
	Width    int
	Height   int
	Title    string
	ThumbURL string
	Provider string
}

// Resolver fetches oEmbed metadata, caching results by page URL.
type Resolver struct {
	client *http.Client
	ttl    time.Duration
	miss   time.Duration
	max    int

	mu    sync.Mutex
	items map[string]cacheEntry
}

type cacheEntry struct {
	emb Embed
	ok  bool
	at  time.Time
}

// New returns a Resolver with the given TTLs and cache cap. A nil client uses
// http.DefaultClient.
func New(client *http.Client, ttl, missTTL time.Duration, maxEntries int) *Resolver {
	if client == nil {
		client = http.DefaultClient
	}
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	if missTTL <= 0 {
		missTTL = 30 * time.Second
	}
	if maxEntries <= 0 {
		maxEntries = 2000
	}
	return &Resolver{
		client: client,
		ttl:    ttl,
		miss:   missTTL,
		max:    maxEntries,
		items:  make(map[string]cacheEntry),
	}
}

// Resolve returns an embeddable player for pageURL, or ErrNotFound when the
// page does not expose oEmbed. Successful and failed lookups are cached for
// their respective TTLs.
func (r *Resolver) Resolve(ctx context.Context, pageURL string) (Embed, error) {
	r.mu.Lock()
	if e, ok := r.items[pageURL]; ok {
		ttl := r.ttl
		if !e.ok {
			ttl = r.miss
		}
		if time.Since(e.at) < ttl {
			r.mu.Unlock()
			return e.emb, nil
		}
		delete(r.items, pageURL)
	}
	r.mu.Unlock()

	emb, err := discoverWith(ctx, clientFetcher(r.client), pageURL)

	r.mu.Lock()
	if len(r.items) >= r.max {
		oldest := ""
		var oldestAt time.Time
		for k, e := range r.items {
			if oldest == "" || e.at.Before(oldestAt) {
				oldest, oldestAt = k, e.at
			}
		}
		delete(r.items, oldest)
	}
	r.items[pageURL] = cacheEntry{emb: emb, ok: err == nil, at: time.Now()}
	r.mu.Unlock()
	return emb, err
}

// Discover fetches pageURL, follows oEmbed discovery, and returns the
// provider's embeddable player. It never inserts the provider's raw html into
// the page: only the iframe src and dimensions are carried across.
func Discover(ctx context.Context, client *http.Client, pageURL string) (Embed, error) {
	return discoverWith(ctx, clientFetcher(client), pageURL)
}

// Getter fetches a URL for oEmbed discovery. It returns the HTTP status and the
// response body. A plugin implements it over Host.Do so oEmbed requests are
// mediated (User-Agent, timeouts, per-host pacing) like every other request.
type Getter func(ctx context.Context, rawurl string) (status int, body []byte, err error)

// DiscoverWith is Discover for callers that fetch through their own transport
// (e.g. a plugin's Host.Do) rather than an *http.Client.
func DiscoverWith(ctx context.Context, get Getter, pageURL string) (Embed, error) {
	return discoverWith(ctx, get, pageURL)
}

// clientFetcher adapts an *http.Client (which always uses the browser agent) to
// a Getter.
func clientFetcher(client *http.Client) Getter {
	if client == nil {
		client = http.DefaultClient
	}
	return func(ctx context.Context, rawurl string) (int, []byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawurl, nil)
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set("User-Agent", BrowserUserAgent)
		resp, err := client.Do(req)
		if err != nil {
			return 0, nil, err
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, bodyCap))
		return resp.StatusCode, body, nil
	}
}

func discoverWith(ctx context.Context, get Getter, pageURL string) (Embed, error) {
	endpoint, err := discoverEndpoint(ctx, get, pageURL)
	if err != nil {
		return Embed{}, err
	}
	return fetchEmbed(ctx, get, endpoint)
}

// discoverEndpoint fetches pageURL and returns the absolute URL of its
// application/json+oembed (or text/html+oembed) alternate link.
func discoverEndpoint(ctx context.Context, get Getter, pageURL string) (string, error) {
	status, body, err := get(ctx, pageURL)
	if err != nil {
		return "", err
	}
	if status >= 400 {
		return "", fmt.Errorf("get %s: status %d", pageURL, status)
	}
	base, _ := url.Parse(pageURL)
	z := html.NewTokenizer(bytes.NewReader(body))
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return "", ErrNotFound
		case html.StartTagToken, html.SelfClosingTagToken:
			t := z.Token()
			if t.Data != "link" {
				continue
			}
			var rel, typ, href string
			for _, a := range t.Attr {
				switch strings.ToLower(a.Key) {
				case "rel":
					rel = strings.ToLower(a.Val)
				case "type":
					typ = strings.ToLower(a.Val)
				case "href":
					href = a.Val
				}
			}
			if href == "" || !strings.Contains(rel, "alternate") {
				continue
			}
			if typ != "application/json+oembed" && typ != "text/html+oembed" {
				continue
			}
			u, err := url.Parse(href)
			if err != nil {
				return "", ErrNotFound
			}
			endpoint := base.ResolveReference(u)
			if !endpoint.IsAbs() || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
				continue
			}
			return endpoint.String(), nil
		}
	}
}

type oembedDoc struct {
	Type         string `json:"type"`
	Version      string `json:"version"`
	Title        string `json:"title"`
	AuthorName   string `json:"author_name"`
	ProviderName string `json:"provider_name"`
	ThumbnailURL string `json:"thumbnail_url"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	HTML         string `json:"html"`
}

// fetchEmbed fetches an oEmbed endpoint and extracts the player iframe from
// its html field.
func fetchEmbed(ctx context.Context, get Getter, endpoint string) (Embed, error) {
	status, body, err := get(ctx, endpoint)
	if err != nil {
		return Embed{}, err
	}
	if status >= 400 {
		return Embed{}, fmt.Errorf("oembed %s: status %d", endpoint, status)
	}
	var doc oembedDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return Embed{}, err
	}
	src, width, height := iframeFromHTML(doc.HTML)
	if src == "" {
		return Embed{}, ErrNotFound
	}
	if width == 0 {
		width = doc.Width
	}
	if height == 0 {
		height = doc.Height
	}
	return Embed{
		Src:      src,
		Width:    width,
		Height:   height,
		Title:    doc.Title,
		ThumbURL: doc.ThumbnailURL,
		Provider: doc.ProviderName,
	}, nil
}

// iframeFromHTML finds the first <iframe> in a provider's oEmbed html and
// returns its src and width/height. Provider HTML is never rendered as-is.
func iframeFromHTML(s string) (src string, width, height int) {
	if s == "" {
		return "", 0, 0
	}
	doc, err := html.Parse(strings.NewReader(s))
	if err != nil {
		return "", 0, 0
	}
	var find func(*html.Node) *html.Node
	find = func(n *html.Node) *html.Node {
		if n.Type == html.ElementNode && n.Data == "iframe" {
			return n
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if m := find(c); m != nil {
				return m
			}
		}
		return nil
	}
	node := find(doc)
	if node == nil {
		return "", 0, 0
	}
	for _, a := range node.Attr {
		switch strings.ToLower(a.Key) {
		case "src":
			u, err := url.Parse(a.Val)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
				return "", 0, 0
			}
			src = u.String()
		case "width":
			fmt.Sscanf(a.Val, "%d", &width)
		case "height":
			fmt.Sscanf(a.Val, "%d", &height)
		}
	}
	return src, width, height
}
