// Package x is the native plugin for X (Twitter) profile feeds. X offers no
// public RSS or guest API, so the profile page is scraped and its embedded
// recent posts are extracted. The payload format is unofficial; treat it as
// best-effort.
package x

import (
	"context"
	"encoding/base64"
	"errors"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/metruzanca/nanoflux/pluginapi"
)

// Name is the plugin's stable identifier.
const Name = "x"

// twitterEpoch is the millisecond epoch Twitter's snowflake IDs are offset from.
const twitterEpoch = 1288834974657 // 2010-11-04T01:42:54Z

// hosts lists hosts treated as X profile pages. It is a var so tests can inject
// a mock host.
var hosts = []string{"x.com", "www.x.com", "twitter.com", "www.twitter.com"}

// Plugin is the X Fetcher.
type Plugin struct{}

var _ pluginapi.Fetcher = Plugin{}

func (Plugin) Meta() pluginapi.Meta {
	return pluginapi.Meta{Name: Name, APIVersion: pluginapi.APIVersion}
}

// Match handles both discover and fetch for X profile URLs.
func (Plugin) Match(u *url.URL, _ pluginapi.Capability) bool {
	return isProfileURL(u)
}

// Discover: the profile URL is itself the feed URL.
func (Plugin) Discover(_ context.Context, pageURL string, _ pluginapi.Host) ([]pluginapi.Candidate, error) {
	u, err := url.Parse(pageURL)
	if err != nil || !isProfileURL(u) {
		return nil, pluginapi.ErrUnsupportedCapability
	}
	handle := handleFromURL(u)
	return []pluginapi.Candidate{{
		FeedURL: pageURL,
		Title:   handle,
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
	tweets := tweetsFromPage(resp.Body, handle)
	if len(tweets) == 0 {
		return pluginapi.Result{}, errors.New("no posts found on profile page")
	}
	return pluginapi.Result{
		Feed:  pluginapi.Feed{Title: displayName(resp.Body, handle), HomeURL: req.URL},
		Items: tweets,
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

// isProfileURL reports whether u is an X profile page (x.com/<handle>), not a
// status, search, home, or other page.
func isProfileURL(u *url.URL) bool {
	if u == nil || !isHost(u.Hostname()) {
		return false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 1 || parts[0] == "" {
		return false
	}
	if strings.TrimPrefix(parts[0], "@") == "" {
		return false
	}
	switch parts[0] {
	case "home", "explore", "search", "notifications", "messages", "settings",
		"login", "signup", "logout", "i", "intent", "hashtag", "moments",
		"share", "compose", "tos", "privacy", "manifest.json":
		return false
	}
	return true
}

func handleFromURL(u *url.URL) string {
	return strings.TrimPrefix(strings.Trim(strings.Trim(u.Path, "/"), "/"), "@")
}

var (
	titleRe = regexp.MustCompile(`<title>([^<]*)</title>`)
	nameRe  = regexp.MustCompile(`^(.*?)\s*\(@[^)]*\)\s*(?:on X|/ X)`)
	tweetRe = regexp.MustCompile(`"client:([A-Za-z0-9+/=]+):details":\$R\[\d+\]=\{.{0,300}?full_text:"((?:[^"\\]|\\.)*)"`)
	orderRe = regexp.MustCompile(`TimelineTimelineEntry:tweet-(\d+)`)
)

// displayName extracts the profile's display name from the page <title>
// (e.g. "Sam Altman (@sama) / X" -> "Sam Altman"), falling back to the handle.
func displayName(body []byte, handle string) string {
	if m := titleRe.FindSubmatch(body); len(m) == 2 {
		if name := nameRe.FindSubmatch(m[1]); len(name) == 2 {
			return string(name[1])
		}
		return string(m[1])
	}
	return handle
}

type tweet struct {
	id   string
	text string
	pos  int
	when time.Time
}

// tweetsFromPage extracts the posts embedded in the page, ordered as the
// timeline shows them. The tweet text lives in a client:...:details object keyed
// by base64("Tweet:<id>"); the display order comes from the timeline entry ids.
func tweetsFromPage(body []byte, handle string) []pluginapi.Item {
	order := map[string]int{}
	for i, m := range orderRe.FindAllSubmatch(body, -1) {
		id := string(m[1])
		if _, ok := order[id]; !ok {
			order[id] = i
		}
	}

	var tws []tweet
	for _, m := range tweetRe.FindAllSubmatch(body, -1) {
		b64, err := base64.StdEncoding.DecodeString(string(m[1]))
		if err != nil {
			continue
		}
		id := strings.TrimPrefix(string(b64), "Tweet:")
		if id == "" || id == string(b64) {
			continue
		}
		text := string(m[2])
		if unq, err := strconv.Unquote(`"` + text + `"`); err == nil {
			text = unq
		}
		tws = append(tws, tweet{id: id, text: text, pos: order[id], when: snowflakeTime(id)})
	}

	sort.SliceStable(tws, func(i, j int) bool {
		if tws[i].pos != tws[j].pos {
			return tws[i].pos < tws[j].pos
		}
		return tws[i].when.After(tws[j].when)
	})

	out := make([]pluginapi.Item, 0, len(tws))
	for _, t := range tws {
		out = append(out, pluginapi.Item{
			GUID:        "tweet:" + t.id,
			Title:       PostTitle(t.text),
			Link:        "https://x.com/" + handle + "/status/" + t.id,
			Summary:     t.text,
			PublishedAt: formatTime(t.when),
		})
	}
	return out
}

// PostTitle derives a list title from a post: the first line, truncated.
func PostTitle(text string) string {
	title := text
	if i := strings.IndexByte(title, '\n'); i >= 0 {
		title = title[:i]
	}
	title = strings.TrimSpace(title)
	if len(title) > 100 {
		title = title[:99] + "…"
	}
	return title
}

// snowflakeTime recovers a tweet's publish time from its snowflake ID.
func snowflakeTime(id string) time.Time {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.UnixMilli((n >> 22) + twitterEpoch).UTC()
}

const timeFormat = "2006-01-02 15:04:05"

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(timeFormat)
}
