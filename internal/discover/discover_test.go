package discover

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

const sampleRSS = `<?xml version="1.0"?>
<rss version="2.0"><channel>
  <title>Feed Title</title>
  <link>https://example.com/</link>
  <item><guid>1</guid><title>One</title><link>https://example.com/1</link></item>
</channel></rss>`

func TestDiscoverDirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sampleRSS))
	}))
	defer srv.Close()

	cs, err := New(srv.Client()).Discover(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 || cs[0].Strategy != "direct" {
		t.Fatalf("candidates = %+v", cs)
	}
	if cs[0].Title != "Feed Title" {
		t.Fatalf("title = %q", cs[0].Title)
	}
}

func TestDiscoverHTMLLinks(t *testing.T) {
	html := `<html><head>
	  <title>Some Blog</title>
	  <link rel="alternate" type="application/rss+xml" href="/rss.xml">
	  <link rel="alternate" type="application/atom+xml" href="/atom.xml">
	  <link rel="stylesheet" href="/style.css">
	</head><body>hi</body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(html))
		case "/rss.xml":
			w.Write([]byte(sampleRSS))
		case "/atom.xml":
			w.Write([]byte(`<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"><title>A</title><id>urn:x</id><updated>2026-01-01T00:00:00Z</updated></feed>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cs, err := New(srv.Client()).Discover(context.Background(), srv.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 {
		t.Fatalf("candidates = %+v", cs)
	}
	if cs[0].Strategy != "html" || cs[0].FeedURL != srv.URL+"/rss.xml" {
		t.Fatalf("first candidate = %+v", cs[0])
	}
}

func TestDiscoverCommonPaths(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("<html><head><title>No Links</title></head></html>"))
		case "/atom.xml":
			w.Write([]byte(sampleRSS))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cs, err := New(srv.Client()).Discover(context.Background(), srv.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 || cs[0].Strategy != "paths" {
		t.Fatalf("candidates = %+v", cs)
	}
	if cs[0].FeedURL != srv.URL+"/atom.xml" {
		t.Fatalf("feed = %q", cs[0].FeedURL)
	}
}

func TestDiscoverNone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("<html><head><title>x</title></head></html>"))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	cs, err := New(srv.Client()).Discover(context.Background(), srv.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 0 {
		t.Fatalf("expected no candidates, got %+v", cs)
	}
}

func TestHostSpecificURLs(t *testing.T) {
	u := func(s string) *url.URL {
		parsed, err := url.Parse(s)
		if err != nil {
			t.Fatal(err)
		}
		return parsed
	}

	cases := []struct {
		page string
		want string
	}{
		{"https://bsk.app/profile/metru.dev", "https://bsk.app/profile/metru.dev/rss"},
		{"https://bsky.app/profile/metru.dev.bsky.social", "https://bsky.app/profile/metru.dev.bsky.social/rss"},
		{"https://www.reddit.com/r/golang/", "https://www.reddit.com/r/golang/.rss"},
		{"https://github.com/spf13/cobra", "https://github.com/spf13/cobra/releases.atom"},
		{"https://example.com/anything", ""},
		{"https://www.youtube.com/watch?v=x", ""}, // needs channel_id scraping
	}
	for _, c := range cases {
		got := hostSpecificURLs(u(c.page))
		if c.want == "" {
			if len(got) != 0 {
				t.Errorf("%s: got %v, want none", c.page, got)
			}
			continue
		}
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("%s: got %v, want [%s]", c.page, got, c.want)
		}
	}
}

func TestYouTubeChannelID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<script>var ytInitialData = {"header":"channelId":"UCrLkzvXK6fN4oD9XVjvjKxQ"`))
	}))
	defer srv.Close()

	d := New(srv.Client())
	id := d.youtubeChannelID(context.Background(), srv.URL)
	if id != "UCrLkzvXK6fN4oD9XVjvjKxQ" {
		t.Fatalf("channel id = %q", id)
	}
}
