// Package instagram is the native plugin for Instagram profile feeds.
// Instagram has no public feed; the profile page serves its recent-post grid
// only to crawler user-agents, embedded in the web client's Relay payload.
// Treat it as best-effort.
package instagram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/metruzanca/nanoflux/pluginapi"
)

// Name is the plugin's stable identifier.
const Name = "instagram"

// userAgent is the crawler identity Instagram serves the logged-out post grid
// to. A normal browser user-agent gets a login wall with no posts. It is
// declared via Meta so the host sends it for this plugin's mediated requests.
const userAgent = "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"

// instagramEpoch is the millisecond epoch Instagram's media ids are offset from.
const instagramEpoch = 1314220021721

// shortcodeAlphabet is Instagram's URL-safe base64 variant.
const shortcodeAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"

// hosts lists hosts treated as Instagram profile pages. A var so tests can
// inject a mock host.
var hosts = []string{"instagram.com", "www.instagram.com", "m.instagram.com"}

var (
	fullNameRe       = regexp.MustCompile(`"full_name":"((?:[^"\\]|\\.)*)"`)
	titleRe          = regexp.MustCompile(`<title>([^<]*)</title>`)
	titleSuffixRe    = regexp.MustCompile(`\s*\(\s*@[^)]*\)\s*[•·]\s*Instagram.*$`)
	nodeMarker       = []byte(`{"__typename":"XIGPolaris`)
	reservedSegments = map[string]bool{
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

// Plugin is the Instagram Fetcher.
type Plugin struct{}

var _ pluginapi.Fetcher = Plugin{}

func (Plugin) Meta() pluginapi.Meta {
	return pluginapi.Meta{Name: Name, APIVersion: pluginapi.APIVersion, UserAgent: userAgent}
}

func (Plugin) Match(u *url.URL, _ pluginapi.Capability) bool {
	return isProfileURL(u)
}

// Discover: the profile URL is itself the feed URL.
func (Plugin) Discover(_ context.Context, pageURL string, _ pluginapi.Host) ([]pluginapi.Candidate, error) {
	u, err := url.Parse(pageURL)
	if err != nil || !isProfileURL(u) {
		return nil, pluginapi.ErrUnsupportedCapability
	}
	return []pluginapi.Candidate{{
		FeedURL: pageURL,
		Title:   handleFromURL(u),
		HomeURL: pageURL,
	}}, nil
}

// Fetch scrapes the profile page and extracts its embedded recent posts.
func (p Plugin) Fetch(ctx context.Context, req pluginapi.FetchRequest, h pluginapi.Host) (pluginapi.Result, error) {
	resp, err := h.Do(ctx, pluginapi.HTTPRequest{Method: "GET", URL: req.URL})
	if err != nil {
		return pluginapi.Result{}, err
	}
	if resp.RateLimited {
		return pluginapi.Result{}, &pluginapi.RateLimit{URL: req.URL, Status: resp.Status, RetryAfter: resp.RetryAfter}
	}
	if resp.Status >= 400 {
		return pluginapi.Result{}, &pluginapi.StatusError{Code: resp.Status, URL: req.URL}
	}

	u, _ := url.Parse(req.URL)
	handle := handleFromURL(u)
	posts := postsFromPage(resp.Body)
	if len(posts) == 0 {
		return pluginapi.Result{}, errors.New("no posts found on profile page")
	}
	return pluginapi.Result{
		Feed:  pluginapi.Feed{Title: displayName(resp.Body, handle), HomeURL: req.URL},
		Items: posts,
	}, nil
}

func isHost(host string) bool {
	host = strings.ToLower(host)
	for _, h := range hosts {
		if host == h {
			return true
		}
	}
	return false
}

// isProfileURL reports whether u is an Instagram profile page
// (instagram.com/<handle>), not a post, story, explore, or other page.
func isProfileURL(u *url.URL) bool {
	if u == nil || !isHost(u.Hostname()) {
		return false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 1 || parts[0] == "" {
		return false
	}
	if reservedSegments[strings.ToLower(parts[0])] {
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

func handleFromURL(u *url.URL) string {
	return strings.TrimPrefix(strings.Trim(strings.Trim(u.Path, "/"), "/"), "@")
}

// node is the subset of a Polaris timeline media node the scraper uses.
type node struct {
	PK          string `json:"pk"`
	ProductType string `json:"product_type"`
	Caption     *struct {
		Text string `json:"text"`
	} `json:"caption"`
}

// postsFromPage extracts the media nodes embedded in the page's Relay prefetch
// payload, ordered as the grid shows them, deduped by id.
func postsFromPage(body []byte) []pluginapi.Item {
	seen := map[string]bool{}
	var items []pluginapi.Item
	for i := 0; i < len(body); {
		j := bytes.Index(body[i:], nodeMarker)
		if j < 0 {
			break
		}
		start := i + j
		end := scanJSONObject(body, start)
		if end < 0 {
			break
		}
		i = end + 1
		var n node
		if err := json.Unmarshal(body[start:end+1], &n); err != nil {
			continue
		}
		if n.PK == "" || seen[n.PK] {
			continue
		}
		seen[n.PK] = true
		items = append(items, itemFor(n))
	}
	return items
}

// scanJSONObject returns the index of the brace closing the JSON object starting
// at start, or -1. It tracks string state so braces in captions don't miscount.
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

func itemFor(n node) pluginapi.Item {
	code := shortcode(n.PK)
	link := "https://www.instagram.com/p/" + code + "/"
	if n.ProductType == "clips" {
		link = "https://www.instagram.com/reel/" + code + "/"
	}
	caption := ""
	if n.Caption != nil {
		caption = n.Caption.Text
	}
	it := pluginapi.Item{
		GUID:    "instagram:" + n.PK,
		Title:   postTitle(caption),
		Link:    link,
		Summary: caption,
	}
	if t := mediaTime(n.PK); !t.IsZero() {
		it.PublishedAt = t.UTC().Format("2006-01-02 15:04:05")
	}
	// The cdn thumbnail URLs are signed and expire, so store the stable media
	// endpoint instead; it redirects to a fresh signed URL on load.
	if code != "" {
		it.ImageURL = "https://www.instagram.com/p/" + code + "/media/?size=l"
	}
	return it
}

// shortcode encodes a media id into Instagram's URL shortcode. "" for an
// unparseable id.
func shortcode(pk string) string {
	n, err := strconv.ParseUint(pk, 10, 64)
	if err != nil || n == 0 {
		return ""
	}
	var b [11]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = shortcodeAlphabet[n&63]
		n >>= 6
	}
	return string(b[i:])
}

// mediaTime recovers a post's publish time from its media id, approximate to
// within about a minute (the date is exact).
func mediaTime(pk string) time.Time {
	n, err := strconv.ParseInt(pk, 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}
	}
	return time.UnixMilli((n >> 23) + instagramEpoch).UTC()
}

func postTitle(text string) string {
	title := text
	if i := strings.IndexByte(title, '\n'); i >= 0 {
		title = title[:i]
	}
	title = strings.TrimSpace(title)
	if len([]rune(title)) > 100 {
		title = string([]rune(title)[:99]) + "…"
	}
	return title
}

// displayName extracts the profile's display name from the embedded user object
// ("full_name"), falling back to the page <title> with its suffix stripped, then
// the handle.
func displayName(body []byte, handle string) string {
	if m := fullNameRe.FindSubmatch(body); len(m) == 2 {
		if name := unescapeJSONString(string(m[1])); name != "" {
			return name
		}
	}
	if m := titleRe.FindSubmatch(body); len(m) == 2 {
		title := html.UnescapeString(string(m[1]))
		title = strings.TrimSpace(titleSuffixRe.ReplaceAllString(title, ""))
		if title != "" {
			return title
		}
	}
	return handle
}

func unescapeJSONString(s string) string {
	var out string
	if err := json.Unmarshal([]byte(`"`+s+`"`), &out); err != nil {
		return s
	}
	return out
}
