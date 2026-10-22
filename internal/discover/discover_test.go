package discover

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/metruzanca/nanoflux/internal/feedparse"
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

// TestDiscoverSurfacesPageError asserts that when the page cannot be read and
// nothing is found, the fetch error is returned (so a 429 can be explained to
// the user) rather than a bare empty result.
func TestDiscoverSurfacesPageError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	cs, err := New(srv.Client()).Discover(context.Background(), srv.URL+"/")
	if len(cs) != 0 {
		t.Fatalf("expected no candidates, got %+v", cs)
	}
	if err == nil {
		t.Fatal("expected the page fetch error to surface")
	}
	var se *feedparse.StatusError
	if !errors.As(err, &se) || se.Code != http.StatusTooManyRequests {
		t.Fatalf("error should be a 429 StatusError, got %v", err)
	}
}

func TestPageMeta(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><head>
		  <title>Metru's Corner</title>
		  <link rel="icon" type="image/png" href="/static/favicon.png">
		</head><body>hi</body></html>`))
	}))
	defer srv.Close()

	meta, err := New(srv.Client()).PageMeta(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Title != "Metru's Corner" {
		t.Fatalf("title = %q", meta.Title)
	}
	if meta.IconURL != srv.URL+"/static/favicon.png" {
		t.Fatalf("icon = %q", meta.IconURL)
	}
}

func TestPageMetaFallbackIcon(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><head><title>T</title></head></html>`))
	}))
	defer srv.Close()

	meta, err := New(srv.Client()).PageMeta(context.Background(), srv.URL+"/some/path")
	if err != nil {
		t.Fatal(err)
	}
	if meta.IconURL != srv.URL+"/favicon.ico" {
		t.Fatalf("fallback icon = %q", meta.IconURL)
	}
}

// TestPageMetaOgImageNotPreferred asserts a site keeps its <link rel=icon>
// favicon even when it advertises an og:image.
func TestPageMetaOgImageNotPreferred(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><head>
		  <title>Blog</title>
		  <meta property="og:image" content="https://cdn.example.com/hero.png">
		  <link rel="icon" href="/favicon.png">
		</head><body>hi</body></html>`))
	}))
	defer srv.Close()

	meta, err := New(srv.Client()).PageMeta(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if meta.IconURL != srv.URL+"/favicon.png" {
		t.Fatalf("icon = %q, want the favicon", meta.IconURL)
	}
}

// TestPageMetaCDATA asserts that a URL which is itself an XML feed (so its
// <title> is served as <![CDATA[name]]>) yields a clean title for author
// prefill, rather than the raw CDATA markers.
func TestPageMetaCDATA(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title><![CDATA[username123]]></title><link>https://example.com/</link></channel></rss>`))
	}))
	defer srv.Close()

	meta, err := New(srv.Client()).PageMeta(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Title != "username123" {
		t.Fatalf("title = %q, want username123", meta.Title)
	}
}

func TestStripCDATA(t *testing.T) {
	cases := []struct{ in, want string }{
		{"<![CDATA[username123]]>", "username123"},
		{"<![CDATA[user]]> name", "user name"},
		{"<![CDATA[a]]> and <![CDATA[b]]>", "a and b"},
		{"plain", "plain"},
		{"<![CDATA[unterminated", "unterminated"},
		{"", ""},
	}
	for _, c := range cases {
		if got := stripCDATA(c.in); got != c.want {
			t.Errorf("stripCDATA(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
