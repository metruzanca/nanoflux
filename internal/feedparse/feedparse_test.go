package feedparse

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
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

func TestFetchRateLimited(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		headers map[string]string
		want    time.Duration
	}{
		{"retry-after seconds", 429, map[string]string{"Retry-After": "12"}, 12 * time.Second},
		{"ratelimit-reset float", 429, map[string]string{"x-ratelimit-reset": "18.0"}, 18 * time.Second},
		{"retry-after http-date", 429, map[string]string{"Retry-After": time.Now().Add(90 * time.Second).UTC().Format(http.TimeFormat)}, 0},
		{"no hint uses fallback", 429, nil, defaultRateLimitBackoff},
		{"503 with hint", 503, map[string]string{"Retry-After": "7"}, 7 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tc.headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()

			_, err := Fetch(context.Background(), srv.URL, srv.Client(), "", "")
			var rl *RateLimitError
			if !errors.As(err, &rl) {
				t.Fatalf("error = %v, want *RateLimitError", err)
			}
			if rl.Status != tc.status {
				t.Errorf("status = %d, want %d", rl.Status, tc.status)
			}
			if tc.name == "retry-after http-date" {
				// Date-based hints vary with the clock; just require a positive
				// duration within range.
				if rl.RetryAfter <= 0 || rl.RetryAfter > 2*time.Minute {
					t.Errorf("date hint = %v, want a positive delay", rl.RetryAfter)
				}
				return
			}
			if rl.RetryAfter != tc.want {
				t.Errorf("RetryAfter = %v, want %v", rl.RetryAfter, tc.want)
			}
		})
	}
}

func TestFetchRateLimitClamped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "999999")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	_, err := Fetch(context.Background(), srv.URL, srv.Client(), "", "")
	var rl *RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("error = %v, want *RateLimitError", err)
	}
	if rl.RetryAfter != maxRateLimitBackoff {
		t.Errorf("RetryAfter = %v, want clamp to %v", rl.RetryAfter, maxRateLimitBackoff)
	}
}

func TestFetchUnadorned429(t *testing.T) {
	// A 503 without a retry hint is not treated as a rate limit (it's an
	// ordinary server error), so it stays a StatusError.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	_, err := Fetch(context.Background(), srv.URL, srv.Client(), "", "")
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("error = %v, want *StatusError", err)
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

func TestImageEnclosureBecomesThumbnail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<?xml version="1.0"?>
<rss version="2.0">
<channel>
  <title>Photos</title>
  <link>https://p.dev/</link>
  <description>shots</description>
  <item>
    <guid>shot1</guid>
    <title>A shot</title>
    <link>https://p.dev/shot1</link>
    <description>a caption with real text</description>
    <enclosure url="https://p.dev/photo.jpg?e=1790070194&amp;t=signed" type="image/jpeg" length="100"/>
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
	if got := res.Items[0].ImageURL; got != "https://p.dev/photo.jpg?e=1790070194&t=signed" {
		t.Errorf("image = %q, want first image enclosure", got)
	}
	if len(res.Items[0].Enclosures) != 1 {
		t.Fatalf("enclosures = %d, want 1", len(res.Items[0].Enclosures))
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

func TestBodyImageBecomesThumbnail(t *testing.T) {
	// Blogger-style: a tiny media:thumbnail plus the real images in the body.
	// The first body <img> (higher resolution) should win as the thumbnail.
	summary := `&lt;p&gt;&lt;/p&gt;&lt;div class="separator"&gt;&lt;a href="https://p.dev/full/1.jpg"&gt;&lt;img src="https://p.dev/320/1.jpg" width="320"/&gt;&lt;/a&gt;&lt;/div&gt;&lt;div&gt;&lt;a href="https://p.dev/full/2.jpg"&gt;&lt;img src="https://p.dev/320/2.jpg" width="320"/&gt;&lt;/a&gt;&lt;/div&gt;`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<?xml version="1.0"?>
<rss version="2.0" xmlns:media="http://search.yahoo.com/mrss/">
<channel>
  <title>Photos</title>
  <link>https://p.dev/</link>
  <description>shots</description>
  <item>
    <guid>shot1</guid>
    <title>7946 to 7950</title>
    <link>https://p.dev/post</link>
    <description>` + summary + `</description>
    <media:thumbnail url="https://p.dev/s72-c/1.jpg"/>
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
	if got := res.Items[0].ImageURL; got != "https://p.dev/320/1.jpg" {
		t.Errorf("image = %q, want the first body <img> over the tiny media:thumbnail", got)
	}
}

func TestNextPageRelNext(t *testing.T) {
	// resolve builds the expected next URL by resolving href against base (the
	// URL that was actually fetched, which is the test server's URL).
	resolve := func(base, href string) string {
		b, _ := url.Parse(base)
		r, _ := url.Parse(href)
		return b.ResolveReference(r).String()
	}
	tests := []struct {
		name string
		body string
		// wantHref is resolved against the server URL when set (relative case);
		// wantAbs is used verbatim otherwise.
		wantHref string
		wantAbs  string
	}{
		{
			name:    "atom absolute",
			body:    `<feed xmlns="http://www.w3.org/2005/Atom"><title>X</title><link rel="next" href="https://x.dev/feed?page=2"/><entry><id>1</id><title>a</title></entry></feed>`,
			wantAbs: "https://x.dev/feed?page=2",
		},
		{
			name:     "atom relative query",
			body:     `<feed xmlns="http://www.w3.org/2005/Atom"><title>X</title><link rel="next" href="?page=2"/><entry><id>1</id><title>a</title></entry></feed>`,
			wantHref: "?page=2",
		},
		{
			name:     "atom relative path",
			body:     `<feed xmlns="http://www.w3.org/2005/Atom"><title>X</title><link rel="next" href="/feed/page/2"/><entry><id>1</id><title>a</title></entry></feed>`,
			wantHref: "/feed/page/2",
		},
		{
			name:     "rss atom namespace",
			body:     `<rss version="2.0" xmlns:atom="http://www.w3.org/2005/Atom"><channel><title>X</title><link>https://x.dev/</link><atom:link rel="next" href="?paged=2"/><item><guid>1</guid><title>a</title></item></channel></rss>`,
			wantHref: "?paged=2",
		},
		{
			name:    "href before rel",
			body:    `<feed xmlns="http://www.w3.org/2005/Atom"><title>X</title><link href="https://x.dev/feed?page=3" rel="next" type="application/atom+xml"/><entry><id>1</id><title>a</title></entry></feed>`,
			wantAbs: "https://x.dev/feed?page=3",
		},
		{
			name:    "no next link",
			body:    rssBody,
			wantAbs: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			want := tt.wantAbs
			if tt.wantHref != "" {
				want = resolve(srv.URL, tt.wantHref)
			}
			res, err := Fetch(context.Background(), srv.URL, srv.Client(), "", "")
			if err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			if res.NextPageURL != want {
				t.Errorf("NextPageURL = %q, want %q", res.NextPageURL, want)
			}
		})
	}
}

func TestNextPageParamFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(rssBody))
	}))
	defer srv.Close()

	tests := []struct {
		feedURL string
		want    string
	}{
		{srv.URL + "/feed?page=1", srv.URL + "/feed?page=2"},
		{srv.URL + "/feed?paged=1", srv.URL + "/feed?paged=2"},
		{srv.URL + "/feed?page=1&per=50", srv.URL + "/feed?page=2&per=50"},
		{srv.URL + "/feed", ""},
		{srv.URL + "/feed?offset=10", ""},
		{srv.URL + "/feed?page=x", ""},
	}
	for _, tt := range tests {
		res, err := Fetch(context.Background(), tt.feedURL, srv.Client(), "", "")
		if err != nil {
			t.Fatalf("Fetch(%q): %v", tt.feedURL, err)
		}
		if res.NextPageURL != tt.want {
			t.Errorf("Fetch(%q) NextPageURL = %q, want %q", tt.feedURL, res.NextPageURL, tt.want)
		}
	}
}
