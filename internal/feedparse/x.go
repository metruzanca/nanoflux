package feedparse

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/metruzanca/nanoflux/internal/db"
)

// X (Twitter) offers no public RSS or guest API in 2026: the feeds endpoint
// is long gone and the guest endpoints are blocked. The only credential-free
// source of a profile's recent posts is the page itself, which embeds them in
// the web client's Relay payload. These posts are scraped here. The format is
// minified and unofficial — treat it as best-effort.

// xProfileHosts lists hosts treated as X profile pages. It is a package var so
// tests can inject a mock host.
var xProfileHosts = []string{"x.com", "www.x.com", "twitter.com", "www.twitter.com"}

// twitterEpoch is the millisecond epoch Twitter's snowflake IDs are offset
// from. Deriving the publish time from the ID avoids pairing the page's
// scattered timestamp refs.
const twitterEpoch = 1288834974657 // 2010-11-04T01:42:54Z

const xProfileBodyCap = 4 << 20

var (
	xTitleRe = regexp.MustCompile(`<title>([^<]*)</title>`)
	xNameRe  = regexp.MustCompile(`^(.*?)\s*\(@[^)]*\)\s*(?:on X|/ X)`)
	xTweetRe = regexp.MustCompile(`"client:([A-Za-z0-9+/=]+):details":\$R\[\d+\]=\{.{0,300}?full_text:"((?:[^"\\]|\\.)*)"`)
	xOrderRe = regexp.MustCompile(`TimelineTimelineEntry:tweet-(\d+)`)
)

func isXHost(host string) bool {
	host = strings.ToLower(host)
	for _, h := range xProfileHosts {
		if host == h {
			return true
		}
	}
	return false
}

// isXProfileURL reports whether u is an X profile page (x.com/<handle> or
// twitter.com/<handle>), not a status, search, home, or other page.
func isXProfileURL(u *url.URL) bool {
	if !isXHost(u.Hostname()) {
		return false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 1 || parts[0] == "" {
		return false
	}
	handle := strings.TrimPrefix(parts[0], "@")
	if handle == "" {
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

func isXProfileFeedURL(feedURL string) bool {
	u, err := url.Parse(feedURL)
	return err == nil && isXProfileURL(u)
}

// fetchXProfile scrapes an X profile page and extracts its embedded recent
// posts. Posts are ordered as in the page timeline; publish times come from
// the tweet's snowflake ID.
func fetchXProfile(ctx context.Context, profileURL string, client *http.Client) (Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, profileURL, nil)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("User-Agent", "nanoflux/0.1")

	resp, err := client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("get %s: %w", profileURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return Result{}, fmt.Errorf("get %s: status %d", profileURL, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, xProfileBodyCap))
	if err != nil {
		return Result{}, err
	}

	u, _ := url.Parse(profileURL)
	handle := strings.TrimPrefix(strings.Trim(strings.Trim(u.Path, "/"), "/"), "@")
	tweets := xTweets(body, handle)
	if len(tweets) == 0 {
		return Result{}, errors.New("no posts found on profile page")
	}

	return Result{
		Feed:  Feed{Title: xDisplayName(body, handle), HomeURL: profileURL},
		Items: tweets,
	}, nil
}

// xDisplayName extracts the profile's display name from the page <title>
// (e.g. "Sam Altman (@sama) / X" -> "Sam Altman"), falling back to the handle.
func xDisplayName(body []byte, handle string) string {
	if m := xTitleRe.FindSubmatch(body); len(m) == 2 {
		if name := xNameRe.FindSubmatch(m[1]); len(name) == 2 {
			return string(name[1])
		}
		return string(m[1])
	}
	return handle
}

type xTweet struct {
	id   string
	text string
	pos  int
	when time.Time
}

// xTweets extracts the posts embedded in the page, ordered as the timeline
// shows them. The tweet text lives in a client:...:details object keyed by
// base64("Tweet:<id>"); the display order comes from the timeline entry ids.
func xTweets(body []byte, handle string) []Item {
	order := map[string]int{}
	for i, m := range xOrderRe.FindAllSubmatch(body, -1) {
		id := string(m[1])
		if _, ok := order[id]; !ok {
			order[id] = i
		}
	}

	var tws []xTweet
	for _, m := range xTweetRe.FindAllSubmatch(body, -1) {
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
		tws = append(tws, xTweet{id: id, text: text, pos: order[id], when: snowflakeTime(id)})
	}

	sort.SliceStable(tws, func(i, j int) bool {
		if tws[i].pos != tws[j].pos {
			return tws[i].pos < tws[j].pos
		}
		return tws[i].when.After(tws[j].when)
	})

	out := make([]Item, 0, len(tws))
	for _, t := range tws {
		out = append(out, Item{
			GUID:        "tweet:" + t.id,
			Title:       postTitle(t.text),
			Link:        "https://x.com/" + handle + "/status/" + t.id,
			Summary:     t.text,
			PublishedAt: db.FormatTime(t.when),
		})
	}
	return out
}

// postTitle derives a list title from a post: the first line, truncated.
func postTitle(text string) string {
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
