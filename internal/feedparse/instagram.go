package feedparse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/metruzanca/nanoflux/internal/db"
)

// Instagram has no public feed and no credential-free API: the profile page
// only serves its recent-post grid to crawler user-agents, embedded in the web
// client's Relay payload. These posts are scraped here. The payload is
// minified, unofficial, and only covers the first page — treat it as
// best-effort, exactly like the X (Twitter) scraper.

// instagramProfileHosts lists hosts treated as Instagram profile pages. It is a
// package var so tests can inject a mock host.
var instagramProfileHosts = []string{"instagram.com", "www.instagram.com", "m.instagram.com"}

// instagramUserAgent is the crawler identity Instagram serves the logged-out
// post grid to. A normal browser user-agent gets a login wall with no posts.
var instagramUserAgent = "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"

const instagramProfileBodyCap = 8 << 20

// instagramEpoch is the millisecond epoch Instagram's media ids are offset
// from. Deriving the publish time from the id avoids a per-post fetch; it is
// approximate to within about a minute (see instagramMediaTime).
const instagramEpoch = 1314220021721

// instagramShortcodeAlphabet is Instagram's URL-safe base64 variant used to
// encode a media id into the shortcode that appears in post URLs.
const instagramShortcodeAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"

var (
	instagramFullNameRe       = regexp.MustCompile(`"full_name":"((?:[^"\\]|\\.)*)"`)
	instagramTitleRe          = regexp.MustCompile(`<title>([^<]*)</title>`)
	instagramTitleSuffixRe    = regexp.MustCompile(`\s*\(\s*@[^)]*\)\s*[•·]\s*Instagram.*$`)
	instagramNodeMarker       = []byte(`{"__typename":"XIGPolaris`)
	instagramReservedSegments = map[string]bool{
		"p": true, "reel": true, "reels": true, "tv": true, "stories": true,
		"explore": true, "accounts": true, "direct": true, "about": true,
		"legal": true, "developer": true, "api": true, "graphql": true,
		"web": true, "challenge": true, "session": true, "emails": true,
		"oauth": true, "privacy": true, "terms": true, "help": true,
		"blog": true, "press": true, "jobs": true, "directory": true,
		"nametag": true, "location": true, "music": true, "topics": true,
		"invites": true, "ar": true, "creators": true, "settings": true,
		"notifications": true, "login": true, "signup": true, "logout": true,
	}
)

func isInstagramHost(host string) bool {
	host = strings.ToLower(host)
	for _, h := range instagramProfileHosts {
		if host == h {
			return true
		}
	}
	return false
}

// isInstagramProfileURL reports whether u is an Instagram profile page
// (instagram.com/<handle>), not a post, story, explore, or other page.
func isInstagramProfileURL(u *url.URL) bool {
	if !isInstagramHost(u.Hostname()) {
		return false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 1 || parts[0] == "" {
		return false
	}
	if instagramReservedSegments[strings.ToLower(parts[0])] {
		return false
	}
	handle := strings.TrimPrefix(parts[0], "@")
	if handle == "" {
		return false
	}
	for _, r := range handle {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_') {
			return false
		}
	}
	return true
}

func isInstagramProfileFeedURL(feedURL string) bool {
	u, err := url.Parse(feedURL)
	return err == nil && isInstagramProfileURL(u)
}

// fetchInstagramProfile scrapes an Instagram profile page and extracts its
// embedded recent posts, ordered as the page grid shows them. Post links are
// derived from the media id; publish times are approximate (see
// instagramMediaTime).
func fetchInstagramProfile(ctx context.Context, profileURL string, client *http.Client) (Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, profileURL, nil)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("User-Agent", instagramUserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("get %s: %w", profileURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return Result{}, fmt.Errorf("get %s: status %d", profileURL, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, instagramProfileBodyCap))
	if err != nil {
		return Result{}, err
	}

	u, _ := url.Parse(profileURL)
	handle := strings.TrimPrefix(strings.Trim(strings.Trim(u.Path, "/"), "/"), "@")
	posts := instagramPosts(body)
	if len(posts) == 0 {
		return Result{}, errors.New("no posts found on profile page")
	}

	return Result{
		Feed:  Feed{Title: instagramDisplayName(body, handle), HomeURL: profileURL},
		Items: posts,
	}, nil
}

// instagramNode is the subset of a Polaris timeline media node the scraper
// uses.
type instagramNode struct {
	PK          string `json:"pk"`
	ProductType string `json:"product_type"`
	Caption     *struct {
		Text string `json:"text"`
	} `json:"caption"`
}

// instagramPosts extracts the media nodes embedded in the page's Relay
// prefetch payload, ordered as the grid shows them. Each node is an object
// starting with {"__typename":"XIGPolaris..."; the same media can appear in
// more than one preloader, so nodes are deduped by id.
func instagramPosts(body []byte) []Item {
	seen := map[string]bool{}
	var items []Item
	for i := 0; i < len(body); {
		j := bytes.Index(body[i:], instagramNodeMarker)
		if j < 0 {
			break
		}
		start := i + j
		end := scanJSONObject(body, start)
		if end < 0 {
			break
		}
		i = end + 1
		var node instagramNode
		if err := json.Unmarshal(body[start:end+1], &node); err != nil {
			continue
		}
		if node.PK == "" || seen[node.PK] {
			continue
		}
		seen[node.PK] = true
		items = append(items, instagramItem(node))
	}
	return items
}

// scanJSONObject returns the index of the brace closing the JSON object that
// starts at start, or -1. It tracks string state so braces inside captions do
// not throw off the count.
func scanJSONObject(b []byte, start int) int {
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(b); i++ {
		c := b[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// instagramItem converts one media node into an Item.
func instagramItem(n instagramNode) Item {
	code := instagramShortcode(n.PK)
	link := "https://www.instagram.com/p/" + code + "/"
	if n.ProductType == "clips" {
		link = "https://www.instagram.com/reel/" + code + "/"
	}
	caption := ""
	if n.Caption != nil {
		caption = n.Caption.Text
	}
	it := Item{
		GUID:    "instagram:" + n.PK,
		Title:   postTitle(caption),
		Link:    link,
		Summary: caption,
	}
	if t := instagramMediaTime(n.PK); !t.IsZero() {
		it.PublishedAt = db.FormatTime(t)
	}
	// The cdn thumbnail URLs are signed and expire, so store the stable
	// media endpoint instead; it redirects to a fresh signed URL on load.
	if code != "" {
		it.ImageURL = "https://www.instagram.com/p/" + code + "/media/?size=l"
	}
	return it
}

// instagramShortcode encodes a media id into Instagram's URL shortcode (the
// base64-url-safe form that appears in post URLs). It returns "" for an
// unparseable id.
func instagramShortcode(pk string) string {
	n, err := strconv.ParseUint(pk, 10, 64)
	if err != nil || n == 0 {
		return ""
	}
	var b [11]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = instagramShortcodeAlphabet[n&63]
		n >>= 6
	}
	return string(b[i:])
}

// instagramMediaTime recovers a post's publish time from its media id, which
// carries a millisecond timestamp in its high bits. Instagram assigns the id
// when the media is created, so the result can be up to about a minute before
// the real publish time; the date is exact.
func instagramMediaTime(pk string) time.Time {
	n, err := strconv.ParseInt(pk, 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}
	}
	return time.UnixMilli((n >> 23) + instagramEpoch).UTC()
}

// instagramDisplayName extracts the profile's display name from the embedded
// user object ("full_name"), falling back to the page <title> with its
// "(@handle) • Instagram photos and videos" suffix stripped, then the handle.
func instagramDisplayName(body []byte, handle string) string {
	if m := instagramFullNameRe.FindSubmatch(body); len(m) == 2 {
		if name := unescapeJSONString(string(m[1])); name != "" {
			return name
		}
	}
	if m := instagramTitleRe.FindSubmatch(body); len(m) == 2 {
		title := html.UnescapeString(string(m[1]))
		title = strings.TrimSpace(instagramTitleSuffixRe.ReplaceAllString(title, ""))
		if title != "" {
			return title
		}
	}
	return handle
}

// unescapeJSONString decodes a captured JSON string body (without its quotes),
// returning it unchanged when it cannot be decoded.
func unescapeJSONString(s string) string {
	var out string
	if err := json.Unmarshal([]byte(`"`+s+`"`), &out); err != nil {
		return s
	}
	return out
}
