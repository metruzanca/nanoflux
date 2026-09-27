// Package youtube is the reference native plugin. It implements
// pluginapi.Fetcher for YouTube channels: discovery resolves a channel id from
// any channel URL form, and fetch reads the channel's recent videos through the
// site's internal browse API.
//
// The channel RSS (feeds/videos.xml) is not used: it carries no video duration,
// and the browse API returns duration (and views) for every entry in the same
// single request. Browse reports publish times only as relative text ("3 days
// ago"), which parseRelativeTime turns into an absolute UTC timestamp at fetch
// time — the host stores an absolute time, so a card never shows a stale "3
// days ago" that was frozen at ingest.
package youtube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/metruzanca/nanoflux/pluginapi"
)

// Name is the plugin's stable identifier (stored in feeds.plugin_name).
const Name = "youtube"

const (
	innerTubeKey        = "AIzaSyA8eiZmM1FaDVjRy-df2KTyQ_vz_yYM39w"
	browseClientVersion = "2.20250710.00.00"
	browseVideosParams  = "EgZ2aWRlb3PyBgQKAjoA"
)

// Plugin is the YouTube Fetcher.
type Plugin struct{}

var _ pluginapi.Fetcher = Plugin{}

func (Plugin) Meta() pluginapi.Meta {
	return pluginapi.Meta{Name: Name, APIVersion: pluginapi.APIVersion}
}

// Match handles discovery on any YouTube host, and fetch for the channel RSS
// URL (whose 404 fallback requires the plugin to own the fetch).
func (Plugin) Match(u *url.URL, cap pluginapi.Capability) bool {
	if !isYouTubeHost(u.Hostname()) {
		return false
	}
	switch cap {
	case pluginapi.CapDiscover:
		return true
	case pluginapi.CapFetch:
		return isChannelFeedURL(u)
	default:
		return false
	}
}

func isYouTubeHost(host string) bool {
	switch strings.ToLower(host) {
	case "youtube.com", "www.youtube.com", "m.youtube.com", "youtu.be":
		return true
	}
	return false
}

// isChannelFeedURL reports whether u is the per-channel RSS endpoint
// (https://www.youtube.com/feeds/videos.xml?channel_id=UC...).
func isChannelFeedURL(u *url.URL) bool {
	return u.Path == "/feeds/videos.xml" && u.Query().Get("channel_id") != ""
}

// Discover resolves a channel id from any channel URL form and returns its RSS
// feed, carrying the channel name/avatar so the add form shows the real author.
func (p Plugin) Discover(ctx context.Context, pageURL string, h pluginapi.Host) ([]pluginapi.Candidate, error) {
	u, err := url.Parse(pageURL)
	if err != nil || !isYouTubeHost(u.Hostname()) {
		return nil, pluginapi.ErrUnsupportedCapability
	}
	id := channelIDFromPath(u.Path)
	if id == "" {
		id = p.resolveChannelID(ctx, pageURL, h)
	}
	if id == "" {
		return nil, nil
	}
	name, avatar := p.channelMeta(ctx, pageURL, h)
	return []pluginapi.Candidate{{
		FeedURL: "https://www.youtube.com/feeds/videos.xml?channel_id=" + id,
		Title:   name,
		IconURL: avatar,
		HomeURL: pageURL,
	}}, nil
}

// Fetch reads the channel's recent videos through the browse API. The channel
// feed URL carries the channel id; anything else is rejected. The browse origin
// is taken from the request URL, so the plugin talks to the same YouTube origin
// the feed URL names (www.youtube.com in production) and a test can point it at
// a mock host without any env plumbing.
func (p Plugin) Fetch(ctx context.Context, req pluginapi.FetchRequest, h pluginapi.Host) (pluginapi.Result, error) {
	u, perr := url.Parse(req.URL)
	if perr != nil {
		return pluginapi.Result{}, perr
	}
	channelID := u.Query().Get("channel_id")
	if channelID == "" {
		return pluginapi.Result{}, &pluginapi.StatusError{Code: 400, URL: req.URL}
	}
	base := u.Scheme + "://" + u.Host
	return p.fetchViaBrowse(ctx, channelID, base, h)
}

// fetchViaBrowse pulls a channel's recent videos through youtubei/v1/browse on
// baseURL.
func (p Plugin) fetchViaBrowse(ctx context.Context, channelID, baseURL string, h pluginapi.Host) (pluginapi.Result, error) {
	payload := fmt.Sprintf(
		`{"context":{"client":{"clientName":"WEB","clientVersion":%q}},"browseId":%q,"params":%q}`,
		browseClientVersion, channelID, browseVideosParams,
	)
	resp, err := h.Do(ctx, pluginapi.HTTPRequest{
		Method:  "POST",
		URL:     baseURL + "/youtubei/v1/browse?key=" + innerTubeKey,
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    []byte(payload),
	})
	if err != nil {
		return pluginapi.Result{}, err
	}
	if resp.RateLimited {
		return pluginapi.Result{}, &pluginapi.RateLimit{URL: baseURL, Status: resp.Status, RetryAfter: resp.RetryAfter}
	}
	if resp.Status >= 400 {
		return pluginapi.Result{}, &pluginapi.StatusError{Code: resp.Status, URL: baseURL}
	}
	var root any
	if err := json.Unmarshal(resp.Body, &root); err != nil {
		return pluginapi.Result{}, fmt.Errorf("youtube browse: %w", err)
	}
	res := pluginapi.Result{
		Feed: pluginapi.Feed{
			Title:   channelTitle(root),
			HomeURL: "https://www.youtube.com/channel/" + channelID,
		},
	}
	for _, lv := range lockups(root) {
		if it, ok := lockupItem(lv); ok {
			res.Items = append(res.Items, it)
		}
	}
	if len(res.Items) == 0 {
		return pluginapi.Result{}, errors.New("youtube browse: no videos in channel tab")
	}
	return res, nil
}

// channelMeta reads the channel's display name and avatar (og:image) from the
// page, used for the add-form preview.
func (p Plugin) channelMeta(ctx context.Context, pageURL string, h pluginapi.Host) (name, avatar string) {
	resp, err := h.Do(ctx, pluginapi.HTTPRequest{Method: "GET", URL: pageURL})
	if err != nil || resp.Status >= 400 {
		return "", ""
	}
	body := string(resp.Body)
	name = metaContent(body, "og:title")
	if name == "" {
		name = firstTag(body, "title")
	}
	name = strings.TrimSuffix(name, " - YouTube")
	avatar = metaContent(body, "og:image")
	return strings.TrimSpace(name), avatar
}

var (
	channelIDRe    = regexp.MustCompile(`"channelId"\s*:\s*"(UC[A-Za-z0-9_-]{20,})"`)
	externalIDRe   = regexp.MustCompile(`"externalId"\s*:\s*"(UC[A-Za-z0-9_-]{20,})"`)
	browseIDRe     = regexp.MustCompile(`"browseId"\s*:\s*"(UC[A-Za-z0-9_-]{20,})"`)
	canonicalTagRe = regexp.MustCompile(`<link[^>]*\brel=["']canonical["'][^>]*>`)
	hrefAttrRe     = regexp.MustCompile(`href=["']([^"']+)["']`)
	titleTagRe     = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	ogContentRe    = regexp.MustCompile(`(?is)<meta[^>]*\bproperty=["']%s["'][^>]*\bcontent=["']([^"']*)["']`)
)

// resolveChannelID fetches the page and extracts the channel id from its
// canonical link or embedded JSON.
func (Plugin) resolveChannelID(ctx context.Context, pageURL string, h pluginapi.Host) string {
	resp, err := h.Do(ctx, pluginapi.HTTPRequest{Method: "GET", URL: pageURL})
	if err != nil || resp.Status >= 400 {
		return ""
	}
	data := resp.Body
	if id := canonicalChannelID(data); id != "" {
		return id
	}
	for _, re := range []*regexp.Regexp{externalIDRe, browseIDRe, channelIDRe} {
		if m := re.FindSubmatch(data); len(m) == 2 {
			return string(m[1])
		}
	}
	return ""
}

func channelIDFromPath(p string) string {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	if len(parts) < 2 || parts[0] != "channel" {
		return ""
	}
	id := parts[1]
	if strings.HasPrefix(id, "UC") && len(id) >= 20 {
		return id
	}
	return ""
}

func canonicalChannelID(data []byte) string {
	tag := canonicalTagRe.Find(data)
	if tag == nil {
		return ""
	}
	m := hrefAttrRe.FindSubmatch(tag)
	if len(m) != 2 {
		return ""
	}
	u, err := url.Parse(string(m[1]))
	if err != nil || !isYouTubeHost(u.Hostname()) {
		return ""
	}
	return channelIDFromPath(u.Path)
}

func metaContent(body, property string) string {
	re := regexp.MustCompile(fmt.Sprintf(ogContentRe.String(), regexp.QuoteMeta(property)))
	if m := re.FindStringSubmatch(body); len(m) == 2 {
		return m[1]
	}
	return ""
}

func firstTag(body, tag string) string {
	if tag != "title" {
		return ""
	}
	if m := titleTagRe.FindStringSubmatch(body); len(m) == 2 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// ---- browse response parsing ----

func lockups(root any) []map[string]any {
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

func channelTitle(root any) string {
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

func lockupItem(lv map[string]any) (pluginapi.Item, bool) {
	id := stringAt(lv, "contentId")
	if id == "" {
		return pluginapi.Item{}, false
	}
	it := pluginapi.Item{
		GUID:    "yt:video:" + id,
		Title:   stringAt(lv, "metadata", "lockupMetadataViewModel", "title", "content"),
		Link:    "https://www.youtube.com/watch?v=" + id,
		Summary: strings.Join(nonRelativeParts(metadataParts(lv)), " • "),
	}
	if thumb := firstSourceURL(lv, "contentImage", "thumbnailViewModel", "image", "sources"); thumb != "" {
		it.ImageURL = thumb
	}
	if secs, ok := lockupDuration(lv); ok {
		it.DurationSec = secs
	}
	if t := parseRelativeTime(relativePart(metadataParts(lv))); !t.IsZero() {
		it.PublishedAt = dbTime(t)
	}
	return it, true
}

// lockupDuration finds a video's runtime from the lockup's thumbnail-overlay
// badges. Browse exposes the runtime only as a badge ("5:41", "1:02:03"); a
// live stream's badge reads "LIVE" and a fresh video's may read "New", so it
// takes the first badge that parses as a clock, not merely the first badge.
func lockupDuration(lv map[string]any) (int, bool) {
	var found int
	done := false
	var walk func(v any)
	walk = func(v any) {
		if done {
			return
		}
		switch t := v.(type) {
		case map[string]any:
			if badge, ok := t["thumbnailBadgeViewModel"].(map[string]any); ok {
				if s, _ := badge["text"].(string); s != "" {
					if secs, ok := parseDuration(s); ok {
						found, done = secs, true
						return
					}
				}
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
	walk(lv)
	return found, done
}

// parseDuration parses a clock-formatted runtime ("5:41", "1:02:03") into
// seconds. It rejects the "LIVE"/"PREMIERE" badges browse may put in the same
// slot, so a live stream stays duration-unknown (0).
func parseDuration(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	total := 0
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return 0, false
		}
		total = total*60 + n
	}
	return total, true
}

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

func metadataParts(m map[string]any) []string {
	var out []string
	var cur any = m
	for _, k := range []string{"metadata", "lockupMetadataViewModel", "metadata", "contentMetadataViewModel", "metadataRows"} {
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

func relativePart(parts []string) string {
	for _, p := range parts {
		if relTimeRe.MatchString(strings.ToLower(p)) {
			return p
		}
	}
	return ""
}

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

const dbTimeFormat = "2006-01-02 15:04:05"

func dbTime(t time.Time) string { return t.UTC().Format(dbTimeFormat) }
