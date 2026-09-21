package feedparse

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIsYouTubeChannelFeed(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"https://www.youtube.com/feeds/videos.xml?channel_id=UCx", true},
		{"https://www.youtube.com/feeds/videos.xml?playlist_id=UUx", false},
		{"https://youtube.com/feeds/videos.xml?channel_id=UCx", true},
		{"https://example.com/feeds/videos.xml?channel_id=UCx", false},
		{"https://www.youtube.com/@handle", false},
	}
	for _, c := range cases {
		if got := isYouTubeChannelFeed(c.url); got != c.want {
			t.Errorf("isYouTubeChannelFeed(%q) = %v, want %v", c.url, got, c.want)
		}
	}
}

func TestParseRelativeTime(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"1 minute ago", time.Minute},
		{"3 hours ago", 3 * time.Hour},
		{"5 days ago", 5 * 24 * time.Hour},
		{"2 weeks ago", 2 * 7 * 24 * time.Hour},
		{"1 month ago", 30 * 24 * time.Hour},
		{"10 months ago", 10 * 30 * 24 * time.Hour},
		{"2 years ago", 2 * 365 * 24 * time.Hour},
		{"Streamed 2 hours ago", 2 * time.Hour},
		{"Premiered 1 day ago", 24 * time.Hour},
		{"today", 0},
		{"yesterday", 24 * time.Hour},
		{"garbage", -1}, // want zero time
	}
	for _, c := range cases {
		got := parseRelativeTime(c.in)
		if c.want == -1 {
			if !got.IsZero() {
				t.Errorf("parseRelativeTime(%q) = %v, want zero", c.in, got)
			}
			continue
		}
		want := now.Add(-c.want)
		// Allow small drift between now captures.
		if d := got.Sub(want).Abs(); d > time.Minute {
			t.Errorf("parseRelativeTime(%q) = %v, want ~%v", c.in, got, want)
		}
	}
}

const youtubeBrowseBody = `{
  "metadata": {
    "channelMetadataRenderer": {
      "title": "bigboxSWE",
      "avatar": {"thumbnails": [{"url": "https://yt3.googleusercontent.com/avatar"}]}
    }
  },
  "contents": {
    "twoColumnBrowseResultsRenderer": {
      "tabs": [
        {"tabRenderer": {"content": {
          "richGridRenderer": {
            "contents": [
              {"richItemRenderer": {"content": {"lockupViewModel": {
                "contentId": "H0KAi8AWsnM",
                "contentType": "LOCKUP_CONTENT_TYPE_VIDEO",
                "contentImage": {"thumbnailViewModel": {"image": {"sources": [
                  {"url": "https://i.ytimg.com/vi/H0KAi8AWsnM/hq720.jpg", "width": 720}
                ]}}},
                "metadata": {"lockupMetadataViewModel": {
                  "title": {"content": "Coding interviews have become insane"},
                  "metadata": {"contentMetadataViewModel": {"metadataRows": [
                    {"metadataParts": [{"text": {"content": "485K views"}}, {"text": {"content": "1 month ago"}}]}
                  ]}}
                }},
                "rendererContext": {"commandContext": {"onTap": {"innertubeCommand": {
                  "watchEndpoint": {"videoId": "H0KAi8AWsnM"}
                }}}}
              }}}},
              {"richItemRenderer": {"content": {"lockupViewModel": {
                "contentId": "pHfG0oxgshA",
                "contentType": "LOCKUP_CONTENT_TYPE_VIDEO",
                "contentImage": {"thumbnailViewModel": {"image": {"sources": [
                  {"url": "https://i.ytimg.com/vi/pHfG0oxgshA/hq720.jpg"}
                ]}}},
                "metadata": {"lockupMetadataViewModel": {
                  "title": {"content": "Another video"},
                  "metadata": {"contentMetadataViewModel": {"metadataRows": [
                    {"metadataParts": [{"text": {"content": "12K views"}}, {"text": {"content": "2 weeks ago"}}]}
                  ]}}
                }},
                "rendererContext": {"commandContext": {"onTap": {"innertubeCommand": {
                  "watchEndpoint": {"videoId": "pHfG0oxgshA"}
                }}}}
              }}}}
            ]
          }
        }}}
      ]
    }
  }
}`

func TestYouTubeBrowseFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/feeds/videos.xml" {
			http.Error(w, "nope", http.StatusNotFound)
			return
		}
		if r.URL.Path == "/youtubei/v1/browse" {
			if r.Method != http.MethodPost {
				t.Errorf("browse method = %s", r.Method)
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(youtubeBrowseBody))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	old := youtubeBrowseBaseURL
	youtubeBrowseBaseURL = srv.URL
	defer func() { youtubeBrowseBaseURL = old }()

	feedURL := srv.URL + "/feeds/videos.xml?channel_id=UC5--wS0Ljbin1TjWQX6eafA"
	res, err := Fetch(context.Background(), feedURL, srv.Client(), "", "")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.Feed.Title != "bigboxSWE" {
		t.Errorf("title = %q", res.Feed.Title)
	}
	if res.Feed.HomeURL != "https://www.youtube.com/channel/UC5--wS0Ljbin1TjWQX6eafA" {
		t.Errorf("home = %q", res.Feed.HomeURL)
	}
	if len(res.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(res.Items))
	}
	first := res.Items[0]
	if first.GUID != "yt:video:H0KAi8AWsnM" {
		t.Errorf("guid = %q, want yt:video: prefix", first.GUID)
	}
	if first.Title != "Coding interviews have become insane" {
		t.Errorf("title = %q", first.Title)
	}
	if first.Link != "https://www.youtube.com/watch?v=H0KAi8AWsnM" {
		t.Errorf("link = %q", first.Link)
	}
	if !strings.Contains(first.Summary, "485K views") {
		t.Errorf("summary = %q", first.Summary)
	}
	if strings.Contains(first.Summary, "ago") {
		t.Errorf("summary should not repeat the relative publish time: %q", first.Summary)
	}
	if first.ImageURL != "https://i.ytimg.com/vi/H0KAi8AWsnM/hq720.jpg" {
		t.Errorf("image = %q", first.ImageURL)
	}
	if first.PublishedAt == "" {
		t.Error("publishedAt should be set from relative time")
	}
}

func TestYouTubeBrowseFallbackIgnoresBrokenAPI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()

	old := youtubeBrowseBaseURL
	youtubeBrowseBaseURL = srv.URL
	defer func() { youtubeBrowseBaseURL = old }()

	_, err := Fetch(context.Background(), srv.URL+"/feeds/videos.xml?channel_id=UCx", srv.Client(), "", "")
	if err == nil {
		t.Fatal("expected error when browse API also fails")
	}
}

func TestYoutubeLockupItemSkipsNonVideo(t *testing.T) {
	var root any
	if err := json.Unmarshal([]byte(youtubeBrowseBody), &root); err != nil {
		t.Fatal(err)
	}
	lockups := youtubeLockups(root)
	if len(lockups) != 2 {
		t.Fatalf("lockups = %d, want 2", len(lockups))
	}
	it, ok := youtubeLockupItem(lockups[1])
	if !ok || it.GUID != "yt:video:pHfG0oxgshA" {
		t.Fatalf("second lockup = %+v, ok=%v", it, ok)
	}
}
