package discover

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestHostSpecificURLs(t *testing.T) {
	u := func(s string) *url.URL {
		parsed, err := url.Parse(s)
		if err != nil {
			t.Fatal(err)
		}
		return parsed
	}

	cases := []struct {
		page   string
		want   []string
		titles map[string]string
	}{
		{"https://bsk.app/profile/metru.dev", []string{"https://bsk.app/profile/metru.dev/rss"}, nil},
		{"https://bsky.app/profile/metru.dev.bsky.social", []string{"https://bsky.app/profile/metru.dev.bsky.social/rss"}, nil},
		{"https://www.reddit.com/r/golang/", []string{"https://www.reddit.com/r/golang/.rss"}, nil},
		{"https://github.com/metru", []string{"https://github.com/metru.atom"}, map[string]string{"https://github.com/metru.atom": "metru's Github activity"}},
		{"https://github.com/spf13/cobra", []string{
			"https://github.com/spf13/cobra/releases.atom",
			"https://github.com/spf13/cobra/commits.atom",
			"https://github.com/spf13/cobra/tags.atom",
		}, nil},
		{"https://example.com/anything", nil, nil},
		{"https://www.youtube.com/watch?v=x", nil, nil}, // needs channel_id scraping
	}
	for _, c := range cases {
		got := hostSpecificURLs(u(c.page))
		if c.want == nil {
			if len(got) != 0 {
				t.Errorf("%s: got %v, want none", c.page, got)
			}
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("%s: got %d candidates %v, want %d", c.page, len(got), got, len(c.want))
			continue
		}
		for i, w := range c.want {
			if got[i].URL != w {
				t.Errorf("%s[%d]: url = %q, want %q", c.page, i, got[i].URL, w)
			}
			if c.titles != nil {
				if want := c.titles[w]; got[i].Title != want {
					t.Errorf("%s[%d]: title = %q, want %q", c.page, i, got[i].Title, want)
				}
			}
		}
	}
}

func TestChannelIDFromPath(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/channel/UC5--wS0Ljbin1TjWQX6eafA", "UC5--wS0Ljbin1TjWQX6eafA"},
		{"/@bigboxSWE", ""},
		{"/user/foo", ""},
		{"/channel/short", ""}, // not a channel id shape
		{"", ""},
	}
	for _, c := range cases {
		if got := channelIDFromPath(c.path); got != c.want {
			t.Errorf("channelIDFromPath(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestYouTubeChannelID(t *testing.T) {
	const id = "UC5--wS0Ljbin1TjWQX6eafA"
	handlePage := `<html><head>
	  <title>bigboxSWE</title>
	  <link rel="canonical" href="https://www.youtube.com/channel/` + id + `">
	  <script>var ytInitialData = {"header":{"externalId":"` + id + `"}}</script>
	</head></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(handlePage))
	}))
	defer srv.Close()

	d := New(srv.Client())

	// /channel/<id> resolves from the path alone, no fetch.
	if got := d.youtubeChannelID(context.Background(), srv.URL+"/channel/"+id); got != id {
		t.Fatalf("/channel id = %q", got)
	}
	// A handle page resolves via the canonical link.
	if got := d.youtubeChannelID(context.Background(), srv.URL+"/@bigboxSWE"); got != id {
		t.Fatalf("handle page id = %q", got)
	}
	// The legacy channelId JSON key still works when no canonical/externalId.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<script>var ytInitialData = {"header":{"channelId":"UCrLkzvXK6fN4oD9XVjvjKxQ"}}</script>`))
	}))
	defer srv2.Close()
	if got := New(srv2.Client()).youtubeChannelID(context.Background(), srv2.URL); got != "UCrLkzvXK6fN4oD9XVjvjKxQ" {
		t.Fatalf("legacy channelId = %q", got)
	}
	// A non-channel page yields nothing.
	srv3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><head><title>nope</title></head></html>`))
	}))
	defer srv3.Close()
	if got := New(srv3.Client()).youtubeChannelID(context.Background(), srv3.URL); got != "" {
		t.Fatalf("non-channel id = %q", got)
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

// hostRewriter is a RoundTripper that redirects every request to base, letting
// tests serve a page under a "real" hostname (e.g. youtube.com) that isYouTube
// recognizes while actually hitting the test server.
type hostRewriter struct {
	base *url.URL
	rt   http.RoundTripper
}

func (h hostRewriter) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.URL.Scheme = h.base.Scheme
	r.URL.Host = h.base.Host
	return h.rt.RoundTrip(r)
}

// clientTo serves youtube.com via the test server.
func clientTo(srv *httptest.Server) *http.Client {
	transport := srv.Client().Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &http.Client{Transport: hostRewriter{base: mustURL(srv.URL), rt: transport}}
}

func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}

// TestPageMetaPatreon asserts that a Patreon creator page yields a clean author
// name (the "— creating … | Patreon" suffix stripped) and the creator avatar
// from the page's structured-data JSON-LD rather than the generic favicon.
func TestPageMetaPatreon(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><head>
		  <title>Chris &amp; Jack — creating Sketch Comedy Videos | Patreon</title>
		  <link rel="shortcut icon" href="https://www.patreon.com/favicon.ico">
		  <script type="application/ld+json">{"@type":"ProfilePage","mainEntity":{"name":"Chris & Jack","image":{"contentUrl":"https://c10.patreonusercontent.com/avatar.jpg"}}}</script>
		</head><body>hi</body></html>`))
	}))
	defer srv.Close()

	meta, err := New(clientTo(srv)).PageMeta(context.Background(), "https://www.patreon.com/cw/chrisandjack")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Title != "Chris & Jack" {
		t.Fatalf("title = %q, want Chris & Jack", meta.Title)
	}
	if meta.IconURL != "https://c10.patreonusercontent.com/avatar.jpg" {
		t.Fatalf("icon = %q, want the creator avatar", meta.IconURL)
	}
}

func TestPatreonPageName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Chris & Jack — creating Sketch Comedy Videos | Patreon", "Chris & Jack"},
		{"Smarter Every Day | Creating Science Videos | Patreon", "Smarter Every Day"},
		{"Just A Name", "Just A Name"},
	}
	for _, c := range cases {
		if got := patreonPageName(c.in); got != c.want {
			t.Errorf("patreonPageName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPatreonStructuredAvatar(t *testing.T) {
	if got := patreonStructuredAvatar(`{"mainEntity":{"image":{"contentUrl":"https://c.test/a.jpg"}}}`); got != "https://c.test/a.jpg" {
		t.Errorf("contentUrl = %q", got)
	}
	if got := patreonStructuredAvatar(`{"mainEntity":{"image":{"thumbnailUrl":"https://c.test/t.jpg"}}}`); got != "https://c.test/t.jpg" {
		t.Errorf("thumbnailUrl = %q", got)
	}
	if got := patreonStructuredAvatar("not json"); got != "" {
		t.Errorf("non-json = %q", got)
	}
}

// TestPageMetaYouTubeAvatar asserts that for a YouTube channel page the real
// channel avatar (og:image, yt3.googleusercontent.com) wins over the generic,
// hash-addressed favicon.
func TestPageMetaYouTubeAvatar(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><head>
		  <title>Think Before You Sleep</title>
		  <meta property="og:image" content="https://yt3.googleusercontent.com/abc=s900-c-k-c0x00ffffff-no-rj">
		  <link rel="shortcut icon" href="https://www.youtube.com/s/desktop/hash/img/favicon.ico">
		</head><body>hi</body></html>`))
	}))
	defer srv.Close()

	meta, err := New(clientTo(srv)).PageMeta(context.Background(), "https://www.youtube.com/@ThinkBeforeYouSleepYT")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://yt3.googleusercontent.com/abc=s900-c-k-c0x00ffffff-no-rj"
	if meta.IconURL != want {
		t.Fatalf("icon = %q, want the channel avatar %q", meta.IconURL, want)
	}
}

// TestPageMetaOgImageNotPreferred asserts the og:image preference is YouTube
// only — other sites keep their <link rel=icon> favicon.
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
		t.Fatalf("non-youtube icon = %q, want the favicon", meta.IconURL)
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

func TestPageTitleYouTube(t *testing.T) {
	cases := []struct{ page, title, want string }{
		{"https://youtube.com/@EddyBurback", "Eddy Burback - YouTube", "Eddy Burback"},
		{"https://www.youtube.com/channel/UCx", "Some Channel - YouTube", "Some Channel"},
		{"https://youtube.com/@x", "Just A Name", "Just A Name"},
		{"https://example.com", "Example - Home", "Example - Home"},
	}
	for _, c := range cases {
		if got := pageTitle(c.page, c.title); got != c.want {
			t.Errorf("pageTitle(%q, %q) = %q, want %q", c.page, c.title, got, c.want)
		}
	}
}
