package youtube

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/metruzanca/nanoflux/pluginapi"
)

// hostFunc adapts a function to pluginapi.Host for tests.
type hostFunc struct {
	do func(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error)
}

func (h hostFunc) Do(ctx context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	return h.do(ctx, req)
}
func (hostFunc) Now() time.Time      { return time.Now().UTC() }
func (hostFunc) Logf(string, ...any) {}

func TestMatch(t *testing.T) {
	p := Plugin{}
	cases := []struct {
		raw  string
		cap  pluginapi.Capability
		want bool
	}{
		{"https://www.youtube.com/@SomeChannel", pluginapi.CapDiscover, true},
		{"https://www.youtube.com/channel/UC5--wS0Ljbin1TjWQX6eafA", pluginapi.CapDiscover, true},
		{"https://example.com/x", pluginapi.CapDiscover, false},
		{"https://www.youtube.com/feeds/videos.xml?channel_id=UC5--wS0Ljbin1TjWQX6eafA", pluginapi.CapFetch, true},
		{"https://www.youtube.com/@SomeChannel", pluginapi.CapFetch, false},
	}
	for _, c := range cases {
		u, _ := url.Parse(c.raw)
		if got := p.Match(u, c.cap); got != c.want {
			t.Errorf("Match(%s, cap=%d) = %v, want %v", c.raw, c.cap, got, c.want)
		}
	}
}

// browseLockup builds the minimal browse lockup shape the plugin parses: a
// content id, a title, a duration badge in the thumbnail overlay, and the
// views/relative-time metadata parts.
func browseLockup(id, title, badge, views, ago string) map[string]any {
	parts := []any{}
	for _, s := range []string{views, ago} {
		if s == "" {
			continue
		}
		parts = append(parts, map[string]any{"text": map[string]any{"content": s}})
	}
	return map[string]any{
		"contentId": id,
		"metadata": map[string]any{"lockupMetadataViewModel": map[string]any{
			"title": map[string]any{"content": title},
			"metadata": map[string]any{"contentMetadataViewModel": map[string]any{
				"metadataRows": []any{map[string]any{"metadataParts": parts}},
			}},
		}},
		"contentImage": map[string]any{"thumbnailViewModel": map[string]any{
			"image": map[string]any{"sources": []any{
				map[string]any{"url": "https://i.ytimg.com/vi/" + id + "/hqdefault.jpg"},
			}},
			"overlays": []any{map[string]any{"thumbnailBottomOverlayViewModel": map[string]any{
				"badges": []any{map[string]any{"thumbnailBadgeViewModel": map[string]any{"text": badge}}},
			}}},
		}},
	}
}

func browseHost(t *testing.T, lockups ...map[string]any) hostFunc {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"metadata": map[string]any{"channelMetadataRenderer": map[string]any{"title": "Mock Channel"}},
		"contents": []any{func() map[string]any {
			lvs := make([]any, 0, len(lockups))
			for _, lv := range lockups {
				lvs = append(lvs, map[string]any{"lockupViewModel": lv})
			}
			return map[string]any{"items": lvs}
		}()},
	})
	if err != nil {
		t.Fatal(err)
	}
	return hostFunc{do: func(_ context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		if !strings.Contains(req.URL, "/youtubei/v1/browse") {
			return pluginapi.HTTPResponse{Status: 404}, nil
		}
		return pluginapi.HTTPResponse{Status: 200, Body: body}, nil
	}}
}

func TestFetchBrowse(t *testing.T) {
	h := browseHost(t, browseLockup("abc", "Video One", "5:41", "1M views", "3 days ago"))
	res, err := Plugin{}.Fetch(context.Background(), pluginapi.FetchRequest{
		URL: "https://www.youtube.com/feeds/videos.xml?channel_id=UCx",
	}, h)
	if err != nil {
		t.Fatal(err)
	}
	if res.Feed.Title != "Mock Channel" || len(res.Items) != 1 || res.Items[0].Title != "Video One" {
		t.Fatalf("browse fetch = %+v", res)
	}
	if got := res.Items[0].ImageURL; got != "https://i.ytimg.com/vi/abc/hqdefault.jpg" {
		t.Fatalf("image = %q", got)
	}
}

func TestFetchCarriesDuration(t *testing.T) {
	h := browseHost(t, browseLockup("abc", "Video One", "5:41", "1M views", "3 days ago"))
	res, err := Plugin{}.Fetch(context.Background(), pluginapi.FetchRequest{
		URL: "https://www.youtube.com/feeds/videos.xml?channel_id=UCx",
	}, h)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Items[0].DurationSec; got != 5*60+41 {
		t.Fatalf("duration = %d, want %d (5:41)", got, 5*60+41)
	}
}

func TestFetchNoDurationForLive(t *testing.T) {
	h := browseHost(t, browseLockup("abc", "Live Now", "LIVE", "1K watching", ""))
	res, err := Plugin{}.Fetch(context.Background(), pluginapi.FetchRequest{
		URL: "https://www.youtube.com/feeds/videos.xml?channel_id=UCx",
	}, h)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Items[0].DurationSec; got != 0 {
		t.Fatalf("live stream duration = %d, want 0 (unknown)", got)
	}
}

// TestFetchFindsNestedLockups asserts the walk finds videos at any depth, the
// shape browse uses (lockups are nested in a tab's contents, not a flat list).
func TestFetchFindsNestedLockups(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"metadata": map[string]any{"channelMetadataRenderer": map[string]any{"title": "Mock Channel"}},
		"contents": []any{map[string]any{"tabRenderer": map[string]any{
			"content": map[string]any{"richGridRenderer": map[string]any{
				"contents": []any{map[string]any{"richItemRenderer": map[string]any{
					"content": map[string]any{"lockupViewModel": browseLockup("vid1", "First video", "5:41", "1M views", "3 days ago")},
				}}},
			}},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	h := hostFunc{do: func(_ context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		return pluginapi.HTTPResponse{Status: 200, Body: body}, nil
	}}
	res, err := Plugin{}.Fetch(context.Background(), pluginapi.FetchRequest{
		URL: "https://www.youtube.com/feeds/videos.xml?channel_id=UCx",
	}, h)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 || res.Items[0].GUID != "yt:video:vid1" || res.Items[0].DurationSec != 341 {
		t.Fatalf("nested browse fetch = %+v", res)
	}
}

func TestFetchRejectsNonChannelURL(t *testing.T) {
	h := hostFunc{do: func(_ context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		t.Fatal("non-channel URL should not reach the network")
		return pluginapi.HTTPResponse{}, nil
	}}
	_, err := Plugin{}.Fetch(context.Background(), pluginapi.FetchRequest{
		URL: "https://www.youtube.com/@SomeChannel",
	}, h)
	if err == nil {
		t.Fatal("expected a status error for a URL with no channel_id")
	}
}

// TestParseDuration covers the clock parser and its rejections (live badges,
// malformed input).
func TestParseDuration(t *testing.T) {
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"5:41", 341, true},
		{"1:02:03", 3723, true},
		{"0:07", 7, true},
		{"LIVE", 0, false},
		{"PREMIERE", 0, false},
		{"", 0, false},
		{"5", 0, false},
		{"1:2:3:4", 0, false},
		{"a:b", 0, false},
	}
	for _, c := range cases {
		got, ok := parseDuration(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("parseDuration(%q) = %d, %v; want %d, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestDiscoverResolvesChannel(t *testing.T) {
	h := hostFunc{do: func(_ context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		return pluginapi.HTTPResponse{Status: 200, Body: []byte(`<html><head>
		  <title>Some Channel - YouTube</title>
		  <meta property="og:image" content="https://yt3.googleusercontent.com/abc"/>
		  <link rel="canonical" href="https://www.youtube.com/channel/UC5--wS0Ljbin1TjWQX6eafA">
		</head></html>`)}, nil
	}}
	cs, err := Plugin{}.Discover(context.Background(), "https://www.youtube.com/@SomeChannel", h)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 {
		t.Fatalf("candidates = %+v", cs)
	}
	if cs[0].FeedURL != "https://www.youtube.com/feeds/videos.xml?channel_id=UC5--wS0Ljbin1TjWQX6eafA" {
		t.Fatalf("feed url = %q", cs[0].FeedURL)
	}
	if cs[0].Title != "Some Channel" || cs[0].IconURL != "https://yt3.googleusercontent.com/abc" {
		t.Fatalf("preview metadata = %+v", cs[0])
	}
}
