package youtube

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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

func TestFetchRSS(t *testing.T) {
	h := hostFunc{do: func(_ context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		return pluginapi.HTTPResponse{Status: 200, Body: []byte(`<?xml version="1.0"?>
<feed xmlns="http://www.w3.org/2005/Atom"><title>Channel</title><link href="https://www.youtube.com/channel/UCx"/>
<entry><id>yt:video:abc</id><title>Video One</title><link href="https://www.youtube.com/watch?v=abc"/>
<published>2026-01-01T00:00:00Z</published></entry></feed>`)}, nil
	}}
	res, err := Plugin{}.Fetch(context.Background(), pluginapi.FetchRequest{
		URL: "https://www.youtube.com/feeds/videos.xml?channel_id=UCx",
	}, h)
	if err != nil {
		t.Fatal(err)
	}
	if res.Feed.Title != "Channel" || len(res.Items) != 1 || res.Items[0].Title != "Video One" {
		t.Fatalf("rss fetch = %+v", res)
	}
}

func TestFetchRSSCarriesMediaThumbnail(t *testing.T) {
	h := hostFunc{do: func(_ context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		return pluginapi.HTTPResponse{Status: 200, Body: []byte(`<?xml version="1.0"?>
<feed xmlns="http://www.w3.org/2005/Atom" xmlns:media="http://search.yahoo.com/mrss/">
<title>Channel</title><link href="https://www.youtube.com/channel/UCx"/>
<entry><id>yt:video:abc</id><title>Video One</title><link href="https://www.youtube.com/watch?v=abc"/>
<media:group><media:thumbnail url="https://i.ytimg.com/vi/abc/hqdefault.jpg" width="480" height="360"/></media:group>
<published>2026-01-01T00:00:00Z</published></entry></feed>`)}, nil
	}}
	res, err := Plugin{}.Fetch(context.Background(), pluginapi.FetchRequest{
		URL: "https://www.youtube.com/feeds/videos.xml?channel_id=UCx",
	}, h)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Items[0].ImageURL; got != "https://i.ytimg.com/vi/abc/hqdefault.jpg" {
		t.Fatalf("image = %q, want media:thumbnail inside media:group", got)
	}
}

func TestFetchFallsBackToBrowse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/youtubei/v1/browse") {
			json.NewEncoder(w).Encode(map[string]any{
				"metadata": map[string]any{"channelMetadataRenderer": map[string]any{"title": "Mock Channel"}},
				"contents": []any{map[string]any{"lockupViewModel": map[string]any{
					"contentId": "vid1",
					"metadata": map[string]any{"lockupMetadataViewModel": map[string]any{
						"title": map[string]any{"content": "First video"},
					}},
				}}},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	old := browseBaseURL
	browseBaseURL = srv.URL
	defer func() { browseBaseURL = old }()

	// RSS endpoint 404s; browse succeeds.
	h := hostFunc{do: func(_ context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		if strings.Contains(req.URL, "/feeds/videos.xml") {
			return pluginapi.HTTPResponse{Status: 404}, nil
		}
		resp, err := http.Get(req.URL)
		if err != nil {
			return pluginapi.HTTPResponse{}, err
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return pluginapi.HTTPResponse{Status: resp.StatusCode, Body: body}, nil
	}}
	res, err := Plugin{}.Fetch(context.Background(), pluginapi.FetchRequest{
		URL: "https://www.youtube.com/feeds/videos.xml?channel_id=UCx",
	}, h)
	if err != nil {
		t.Fatal(err)
	}
	if res.Feed.Title != "Mock Channel" || len(res.Items) != 1 || res.Items[0].GUID != "yt:video:vid1" {
		t.Fatalf("browse fallback = %+v", res)
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
