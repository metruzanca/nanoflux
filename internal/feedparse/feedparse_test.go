package feedparse

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

const rssBody = `<?xml version="1.0"?>
<rss version="2.0">
<channel>
  <title>Example</title>
  <link>https://example.com/</link>
  <description>an example feed</description>
  <item>
    <guid>https://example.com/1</guid>
    <title>Post 1</title>
    <link>https://example.com/1</link>
    <description>first summary</description>
    <pubDate>Mon, 02 Jan 2026 10:00:00 GMT</pubDate>
  </item>
  <item>
    <title>Post 2</title>
    <link>https://example.com/2</link>
    <description>second summary</description>
  </item>
</channel>
</rss>`

func TestFetchNormalizesRSS(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		w.Write([]byte(rssBody))
	}))
	defer srv.Close()

	res, err := Fetch(context.Background(), srv.URL, srv.Client(), "", "")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.Feed.Title != "Example" {
		t.Errorf("title = %q, want Example", res.Feed.Title)
	}
	if res.Feed.HomeURL != "https://example.com/" {
		t.Errorf("home = %q", res.Feed.HomeURL)
	}
	if len(res.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(res.Items))
	}
	first := res.Items[0]
	if first.GUID != "https://example.com/1" || first.Title != "Post 1" ||
		first.Summary != "first summary" {
		t.Errorf("first item malformed: %+v", first)
	}
	if first.PublishedAt == "" {
		t.Error("publishedAt should be set from pubDate")
	}
	second := res.Items[1]
	if second.GUID != "https://example.com/2" {
		t.Errorf("guid fallback to link failed: %+v", second.GUID)
	}
	if res.ETag != `"v1"` {
		t.Errorf("etag = %q", res.ETag)
	}
}

func TestStripTracking(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"https://example.com/a?utm_source=rss&utm_medium=feed&id=5", "https://example.com/a?id=5"},
		{"https://example.com/a?fbclid=abc&b=1", "https://example.com/a?b=1"},
		{"https://example.com/a?ref=x&id=1", "https://example.com/a?ref=x&id=1"}, // generic keys stay
		{"https://example.com/a?utm_campaign=x", "https://example.com/a"},
		{"https://example.com/a", "https://example.com/a"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := StripTracking(tt.in); got != tt.want {
			t.Errorf("StripTracking(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestFetchStripsTracking(t *testing.T) {
	const body = `<?xml version="1.0"?>
<rss version="2.0"><channel>
  <title>Example</title>
  <link>https://example.com/</link>
  <item>
    <title>Post</title>
    <link>https://example.com/1?utm_source=rss&utm_campaign=x&keep=1</link>
    <enclosure url="https://example.com/audio.mp3" type="audio/mpeg" length="123"/>
  </item>
</channel></rss>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	defer srv.Close()

	res, err := Fetch(context.Background(), srv.URL, srv.Client(), "", "")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("items = %d", len(res.Items))
	}
	if got := res.Items[0].Link; got != "https://example.com/1?keep=1" {
		t.Errorf("link = %q, want tracking params stripped", got)
	}
}

func TestFetchConditionalGET(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		w.Write([]byte(rssBody))
	}))
	defer srv.Close()

	res, err := Fetch(context.Background(), srv.URL, srv.Client(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = Fetch(context.Background(), srv.URL, srv.Client(), res.ETag, "")
	if !errors.Is(err, ErrNotModified) {
		t.Fatalf("expected ErrNotModified, got %v", err)
	}
	if hits != 2 {
		t.Fatalf("hits = %d, want 2", hits)
	}
}

const atomBody = `<?xml version="1.0"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Atom Example</title>
  <link href="https://atom.example/"/>
  <id>urn:atom:example</id>
  <updated>2026-01-02T10:00:00Z</updated>
  <entry>
    <id>urn:atom:1</id>
    <title>Atom Post</title>
    <link href="https://atom.example/1"/>
    <summary>atom summary</summary>
    <published>2026-01-02T10:00:00Z</published>
  </entry>
</feed>`

func TestFetchAtom(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(atomBody))
	}))
	defer srv.Close()

	res, err := Fetch(context.Background(), srv.URL, srv.Client(), "", "")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.Feed.Title != "Atom Example" {
		t.Errorf("title = %q", res.Feed.Title)
	}
	if len(res.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(res.Items))
	}
	if res.Items[0].GUID != "urn:atom:1" || res.Items[0].Title != "Atom Post" {
		t.Errorf("item malformed: %+v", res.Items[0])
	}
}

func TestFetchRejectsNonFeed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html><body>not a feed</body></html>"))
	}))
	defer srv.Close()

	_, err := Fetch(context.Background(), srv.URL, srv.Client(), "", "")
	if err == nil {
		t.Fatal("expected error for non-feed body")
	}
}

func TestMediaThumbnailFromGroup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<?xml version="1.0"?>
<rss version="2.0" xmlns:media="http://search.yahoo.com/mrss/">
<channel>
  <title>YouTube</title>
  <link>https://www.youtube.com/channel/UCx</link>
  <description>videos</description>
  <item>
    <guid>yt:video:abc</guid>
    <title>Some video</title>
    <link>https://www.youtube.com/watch?v=abc</link>
    <description>plain text only</description>
    <media:group>
      <media:content url="https://www.youtube.com/v/abc" type="application/x-shockwave-flash" width="640" height="390"/>
      <media:thumbnail url="https://i.ytimg.com/vi/abc/hqdefault.jpg" width="480" height="360"/>
    </media:group>
  </item>
</channel>
</rss>`))
	}))
	defer srv.Close()

	res, err := Fetch(context.Background(), srv.URL, srv.Client(), "", "")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(res.Items))
	}
	if got := res.Items[0].ImageURL; got != "https://i.ytimg.com/vi/abc/hqdefault.jpg" {
		t.Errorf("image = %q, want media:thumbnail inside media:group", got)
	}
}

func TestMediaThumbnailDirectChild(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<?xml version="1.0"?>
<rss version="2.0" xmlns:media="http://search.yahoo.com/mrss/">
<channel>
  <title>Podcast</title>
  <link>https://example.com/</link>
  <description>episodes</description>
  <item>
    <guid>ep1</guid>
    <title>Episode 1</title>
    <link>https://example.com/ep1</link>
    <description>text</description>
    <media:thumbnail url="https://example.com/ep1.jpg"/>
  </item>
</channel>
</rss>`))
	}))
	defer srv.Close()

	res, err := Fetch(context.Background(), srv.URL, srv.Client(), "", "")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(res.Items))
	}
	if got := res.Items[0].ImageURL; got != "https://example.com/ep1.jpg" {
		t.Errorf("image = %q, want direct media:thumbnail", got)
	}
}
