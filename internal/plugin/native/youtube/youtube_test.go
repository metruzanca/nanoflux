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
		{"https://youtube.com/feeds/videos.xml?channel_id=UC5--wS0Ljbin1TjWQX6eafA", pluginapi.CapURLPolicy, true},
		{"https://www.youtube.com/@SomeChannel", pluginapi.CapURLPolicy, true},
		{"https://example.com/x", pluginapi.CapURLPolicy, false},
		// Item-link capabilities match any video link, from any host.
		{"https://www.youtube.com/watch?v=H0KAi8AWsnM", pluginapi.CapRender, true},
		{"https://youtu.be/H0KAi8AWsnM", pluginapi.CapDecorate, true},
		{"https://example.com/post/1", pluginapi.CapRender, false},
		{"https://www.youtube.com/@SomeChannel", pluginapi.CapDecorate, false},
	}
	for _, c := range cases {
		u, _ := url.Parse(c.raw)
		if got := p.Match(u, c.cap); got != c.want {
			t.Errorf("Match(%s, cap=%d) = %v, want %v", c.raw, c.cap, got, c.want)
		}
	}
}

func TestRenderVideoLinks(t *testing.T) {
	p := Plugin{}
	cases := []struct{ link, want string }{
		{"https://www.youtube.com/watch?v=H0KAi8AWsnM", "https://www.youtube.com/embed/H0KAi8AWsnM"},
		{"https://m.youtube.com/watch?v=H0KAi8AWsnM&t=10s", "https://www.youtube.com/embed/H0KAi8AWsnM"},
		{"https://youtu.be/H0KAi8AWsnM", "https://www.youtube.com/embed/H0KAi8AWsnM"},
		{"https://www.youtube.com/shorts/H0KAi8AWsnM", "https://www.youtube.com/embed/H0KAi8AWsnM"},
		{"https://www.youtube.com/live/H0KAi8AWsnM", "https://www.youtube.com/embed/H0KAi8AWsnM"},
		{"https://www.youtube.com/embed/H0KAi8AWsnM", "https://www.youtube.com/embed/H0KAi8AWsnM"},
		{"https://example.com/post/1", ""},
		{"https://youtube.com/watch", ""},
		{"not a url", ""},
	}
	for _, c := range cases {
		req := pluginapi.RenderRequest{Link: c.link}
		got, err := p.Render(context.Background(), req, nil)
		if err != nil {
			t.Fatalf("Render(%q): %v", c.link, err)
		}
		if got.EmbedSrc != c.want {
			t.Errorf("Render(%q).EmbedSrc = %q, want %q", c.link, got.EmbedSrc, c.want)
		}
	}
}

func TestDecorateVideoLinks(t *testing.T) {
	p := Plugin{}
	decs, err := p.Decorate(context.Background(), pluginapi.DecorateRequest{Items: []pluginapi.Item{
		{Link: "https://www.youtube.com/watch?v=H0KAi8AWsnM"},
		{Link: "https://example.com/post/1"},
		{Link: "https://youtu.be/pHfG0oxgshA", ImageURL: "https://i.ytimg.com/vi/pHfG0oxgshA/hq720.jpg"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(decs) != 2 {
		t.Fatalf("decorated %d items, want 2", len(decs))
	}
	if decs[0].Index != 0 || decs[0].Kind != pluginapi.KindVideo ||
		decs[0].ThumbURL != "https://i.ytimg.com/vi/H0KAi8AWsnM/hqdefault.jpg" {
		t.Fatalf("unexpected decoration: %+v", decs[0])
	}
	// A stored thumbnail is kept: the plugin supplies a URL only when missing.
	if decs[1].Index != 2 || decs[1].Kind != pluginapi.KindVideo || decs[1].ThumbURL != "" {
		t.Fatalf("stored thumbnail should not be overridden: %+v", decs[1])
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

// TestFetchUsesWwwOriginForBareHost asserts a stored bare youtube.com feed URL
// is still fetched on www.youtube.com, the host YouTube answers for browse (the
// bare host returns 400).
func TestFetchUsesWwwOriginForBareHost(t *testing.T) {
	var got string
	h := hostFunc{do: func(_ context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		got = req.URL
		return browseHost(t, browseLockup("abc", "Video One", "5:41", "1M views", "3 days ago")).do(context.Background(), req)
	}}
	if _, err := (Plugin{}).Fetch(context.Background(), pluginapi.FetchRequest{
		URL: "https://youtube.com/feeds/videos.xml?channel_id=UCx",
	}, h); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "https://www.youtube.com/youtubei/v1/browse") {
		t.Fatalf("browse origin = %q, want https://www.youtube.com/youtubei/v1/browse", got)
	}
}

// TestCanonicalizeFeedURL covers the www-host canonicalization and its
// idempotence, plus the hosts it leaves alone.
func TestCanonicalizeFeedURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://youtube.com/feeds/videos.xml?channel_id=UCx", "https://www.youtube.com/feeds/videos.xml?channel_id=UCx"},
		{"https://www.youtube.com/feeds/videos.xml?channel_id=UCx", "https://www.youtube.com/feeds/videos.xml?channel_id=UCx"},
		{"https://m.youtube.com/feeds/videos.xml?channel_id=UCx", "https://www.youtube.com/feeds/videos.xml?channel_id=UCx"},
		{"http://youtube.com/feeds/videos.xml?channel_id=UCx", "https://www.youtube.com/feeds/videos.xml?channel_id=UCx"},
		{"https://youtu.be/H0KAi8AWsnM", "https://youtu.be/H0KAi8AWsnM"},
		{"https://example.com/feed", "https://example.com/feed"},
	}
	for _, c := range cases {
		if got := (Plugin{}).CanonicalizeFeedURL(c.in); got != c.want {
			t.Errorf("CanonicalizeFeedURL(%q) = %q, want %q", c.in, got, c.want)
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
