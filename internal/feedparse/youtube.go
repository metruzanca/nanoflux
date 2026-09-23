package feedparse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/metruzanca/nanoflux/internal/db"
)

// YouTube's public feeds/videos.xml endpoint is intermittently dead (serves
// 404 for active channels, a known upstream issue). When it is unreachable,
// channel feeds are fetched through the site's internal browse API — the same
// endpoint the web client uses for channel tabs.

const (
	// youtubeInnerTubeKey is the public web client key embedded in
	// youtube.com pages. It is not a secret.
	youtubeInnerTubeKey = "AIzaSyA8eiZmM1FaDVjRy-df2KTyQ_vz_yYM39w"

	// youtubeBrowseClientVersion is the WEB client version announced to the
	// browse API. Newer versions are accepted when the API drifts.
	youtubeBrowseClientVersion = "2.20250710.00.00"

	// youtubeBrowseVideosParams selects a channel's Videos tab.
	youtubeBrowseVideosParams = "EgZ2aWRlb3PyBgQKAjoA"
)

// youtubeBrowseBaseURL is overridable in tests.
var youtubeBrowseBaseURL = "https://www.youtube.com"

// isYouTubeChannelFeed reports whether feedURL is YouTube's per-channel RSS
// endpoint (https://www.youtube.com/feeds/videos.xml?channel_id=UC...).
func isYouTubeChannelFeed(feedURL string) bool {
	u, err := url.Parse(feedURL)
	if err != nil {
		return false
	}
	if !isYouTubeHost(u.Hostname()) {
		return false
	}
	return u.Path == "/feeds/videos.xml" && u.Query().Get("channel_id") != ""
}

func isYouTubeHost(host string) bool {
	if base, err := url.Parse(youtubeBrowseBaseURL); err == nil &&
		strings.EqualFold(base.Hostname(), host) {
		return true
	}
	switch strings.ToLower(host) {
	case "youtube.com", "www.youtube.com", "m.youtube.com":
		return true
	}
	return false
}

// fetchYouTubeChannelViaBrowse pulls a channel's recent videos through
// youtubei/v1/browse and normalizes them into a Result.
func fetchYouTubeChannelViaBrowse(ctx context.Context, feedURL string, client *http.Client) (Result, error) {
	u, _ := url.Parse(feedURL)
	channelID := u.Query().Get("channel_id")

	payload := fmt.Sprintf(
		`{"context":{"client":{"clientName":"WEB","clientVersion":%q}},"browseId":%q,"params":%q}`,
		youtubeBrowseClientVersion, channelID, youtubeBrowseVideosParams,
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		youtubeBrowseBaseURL+"/youtubei/v1/browse?key="+youtubeInnerTubeKey,
		strings.NewReader(payload))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", UserAgent())

	resp, err := client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("youtube browse: %w", err)
	}
	defer resp.Body.Close()
	if isRateLimited(resp) {
		return Result{}, &RateLimitError{URL: youtubeBrowseBaseURL, Status: resp.StatusCode, RetryAfter: rateLimitBackoff(resp)}
	}
	if resp.StatusCode >= 400 {
		return Result{}, &StatusError{Code: resp.StatusCode, URL: youtubeBrowseBaseURL}
	}

	var root any
	if err := json.NewDecoder(resp.Body).Decode(&root); err != nil {
		return Result{}, fmt.Errorf("youtube browse: %w", err)
	}

	title := youtubeChannelTitle(root)
	res := Result{
		Feed: Feed{
			Title:   title,
			HomeURL: "https://www.youtube.com/channel/" + channelID,
		},
	}
	for _, lv := range youtubeLockups(root) {
		it, ok := youtubeLockupItem(lv)
		if !ok {
			continue
		}
		res.Items = append(res.Items, it)
	}
	if len(res.Items) == 0 {
		return Result{}, errors.New("youtube browse: no videos in channel tab")
	}
	return res, nil
}

// youtubeLockups walks the response and collects every lockupViewModel, the
// container the current web client uses for video grid entries.
func youtubeLockups(root any) []map[string]any {
	var out []map[string]any
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			if lv, ok := t["lockupViewModel"].(map[string]any); ok {
				out = append(out, lv)
			}
			for _, c := range t {
				walk(c)
			}
		case []any:
			for _, c := range t {
				walk(c)
			}
		}
	}
	walk(root)
	return out
}

// youtubeChannelTitle extracts the channel title from the response metadata.
func youtubeChannelTitle(root any) string {
	title := ""
	var walk func(v any)
	walk = func(v any) {
		if title != "" {
			return
		}
		switch t := v.(type) {
		case map[string]any:
			if cmr, ok := t["channelMetadataRenderer"].(map[string]any); ok {
				title, _ = cmr["title"].(string)
				return
			}
			for _, c := range t {
				walk(c)
			}
		case []any:
			for _, c := range t {
				walk(c)
			}
		}
	}
	walk(root)
	return title
}

// youtubeLockupItem converts one lockupViewModel (a video entry) into an Item.
// GUIDs use the yt:video: prefix so they match the native RSS feed and dedup
// cleanly when the feeds endpoint recovers.
func youtubeLockupItem(lv map[string]any) (Item, bool) {
	id := stringAt(lv, "contentId")
	if id == "" {
		return Item{}, false
	}
	title := stringAt(lv, "metadata", "lockupMetadataViewModel", "title", "content")
	parts := metadataParts(lv, "metadata", "lockupMetadataViewModel", "metadata", "contentMetadataViewModel", "metadataRows")
	thumb := firstSourceURL(lv, "contentImage", "thumbnailViewModel", "image", "sources")

	it := Item{
		GUID:    "yt:video:" + id,
		Title:   title,
		Link:    "https://www.youtube.com/watch?v=" + id,
		Summary: strings.Join(nonRelativeParts(parts), " • "),
	}
	if thumb != "" {
		it.ImageURL = thumb
	}
	if t := parseRelativeTime(relativePart(parts)); !t.IsZero() {
		it.PublishedAt = db.FormatTime(t)
	}
	return it, true
}

// stringAt digs a string out of a nested map by key path.
func stringAt(m map[string]any, keys ...string) string {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = mm[k]
	}
	s, _ := cur.(string)
	return s
}

// metadataParts collects the text content of every metadata part under the
// key path (e.g. "485K views", "1 month ago", "4:20").
func metadataParts(m map[string]any, keys ...string) []string {
	var out []string
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[k]
	}
	rows, _ := cur.([]any)
	for _, r := range rows {
		row, ok := r.(map[string]any)
		if !ok {
			continue
		}
		parts, _ := row["metadataParts"].([]any)
		for _, p := range parts {
			part, ok := p.(map[string]any)
			if !ok {
				continue
			}
			if s := stringAt(part, "text", "content"); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// relativePart returns the metadata part that looks like a relative time
// ("1 month ago", "Streamed 3 days ago"), or "".
func relativePart(parts []string) string {
	for _, p := range parts {
		if relTimeRe.MatchString(strings.ToLower(p)) {
			return p
		}
	}
	return ""
}

// nonRelativeParts filters out metadata parts that look like relative times
// ("4 days ago"). The publish time is derived from those into PublishedAt and
// rendered live by the UI, so keeping the raw text in the summary would show a
// stale duplicate date next to it.
func nonRelativeParts(parts []string) []string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if relTimeRe.MatchString(strings.ToLower(p)) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// firstSourceURL returns the first image source URL under a key path.
func firstSourceURL(m map[string]any, keys ...string) string {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = mm[k]
	}
	sources, _ := cur.([]any)
	for _, s := range sources {
		src, ok := s.(map[string]any)
		if !ok {
			continue
		}
		if u, _ := src["url"].(string); u != "" {
			return u
		}
	}
	return ""
}

var relTimeRe = regexp.MustCompile(`(?:(\d+)\s+(minute|hour|day|week|month|year)s?\s+ago|today|yesterday)`)

// parseRelativeTime turns YouTube's relative time text into an approximate
// UTC timestamp. Unknown text returns the zero time.
func parseRelativeTime(s string) time.Time {
	s = strings.ToLower(strings.TrimSpace(s))
	now := time.Now().UTC()
	switch {
	case strings.Contains(s, "today"):
		return now
	case strings.Contains(s, "yesterday"):
		return now.Add(-24 * time.Hour)
	}
	m := relTimeRe.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}
	}
	n, _ := strconv.Atoi(m[1])
	var d time.Duration
	switch m[2] {
	case "minute":
		d = time.Duration(n) * time.Minute
	case "hour":
		d = time.Duration(n) * time.Hour
	case "day":
		d = time.Duration(n) * 24 * time.Hour
	case "week":
		d = time.Duration(n) * 7 * 24 * time.Hour
	case "month":
		d = time.Duration(n) * 30 * 24 * time.Hour
	case "year":
		d = time.Duration(n) * 365 * 24 * time.Hour
	}
	return now.Add(-d)
}
