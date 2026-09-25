package reddit

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/metruzanca/nanoflux/pluginapi"
)

// hostFunc is a test Host whose Do is the given function.
type hostFunc func(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error)

func (h hostFunc) Do(ctx context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	return h(ctx, req)
}
func (hostFunc) Now() time.Time      { return time.Now().UTC() }
func (hostFunc) Logf(string, ...any) {}

func TestExtractLinkAnchor(t *testing.T) {
	content := `<table> <tr><td> <a href="https://www.reddit.com/r/x/comments/1a/"> <img src="https://external-preview.redd.it/t.jpeg" alt="t" /> </a> </td><td> submitted by <a href="https://www.reddit.com/user/u"> /u/u </a> <br/> <span><a href="https://www.imgur.com/gallery/abc">[link]</a></span> <span><a href="https://www.reddit.com/r/x/comments/1a/">[comments]</a></span> </td></tr></table>`
	u, ok := extractLinkAnchor(content)
	if !ok || u != "https://www.imgur.com/gallery/abc" {
		t.Fatalf("extractLinkAnchor = %q %v, want imgur url", u, ok)
	}
	if _, ok := extractLinkAnchor("<p>just text</p><a href=\"https://x.com\">[comments]</a>"); ok {
		t.Fatal("non-link anchor matched")
	}
	if _, ok := extractLinkAnchor("<a href=\"https://x.com\">[link]</a>"); !ok {
		t.Fatal("missing [link] href")
	}
}

func TestPostID(t *testing.T) {
	cases := []struct {
		in      string
		sub, id string
		ok      bool
	}{
		{"https://old.reddit.com/r/cats/comments/1abcde/mittens_enjoys_a_sunny_nap/", "cats", "1abcde", true},
		{"https://www.reddit.com/comments/1abcde/", "", "1abcde", true},
		{"https://reddit.com/r/x/comments/1a/slug", "x", "1a", true},
		{"https://example.com/r/x/comments/1a/", "", "", false},
		{"https://old.reddit.com/r/x/comments/", "", "", false},
		{"not a url", "", "", false},
	}
	for _, c := range cases {
		sub, id, ok := postID(c.in)
		if sub != c.sub || id != c.id || ok != c.ok {
			t.Errorf("postID(%q) = %q %q %v, want %q %q %v", c.in, sub, id, ok, c.sub, c.id, c.ok)
		}
	}
}

func TestGalleryID(t *testing.T) {
	cases := []struct {
		in string
		id string
		ok bool
	}{
		{"https://www.reddit.com/gallery/1fghij", "1fghij", true},
		{"https://old.reddit.com/gallery/1fghij", "1fghij", true},
		{"https://www.imgur.com/gallery/abc", "", false},
		{"https://www.reddit.com/r/x/comments/1a/", "", false},
		{"https://www.reddit.com/gallery/", "", false},
	}
	for _, c := range cases {
		id, ok := galleryID(c.in)
		if id != c.id || ok != c.ok {
			t.Errorf("galleryID(%q) = %q %v, want %q %v", c.in, id, ok, c.id, c.ok)
		}
	}
}

func TestGalleryImagesFromHTML(t *testing.T) {
	body := []byte(`<html><img src="https://preview.redd.it/whiskers-v0-9z8x7c6v.jpg?width=320&amp;crop=smart&amp;auto=webp&amp;s=abc">
<img src="https://preview.redd.it/whiskers-v0-5t6y7u8i.jpg?width=640&amp;crop=smart&amp;auto=webp&amp;s=def">
<img src="https://preview.redd.it/whiskers-v0-9z8x7c6v.jpg?width=1080&amp;crop=smart&amp;auto=webp&amp;s=ghi"></html>`)
	got := galleryImagesFromHTML(body)
	want := []string{"https://i.redd.it/9z8x7c6v.jpg", "https://i.redd.it/5t6y7u8i.jpg"}
	if len(got) != len(want) {
		t.Fatalf("gallery images = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("gallery images = %v, want %v", got, want)
		}
	}
}

func TestFullImage(t *testing.T) {
	if got := fullImage("https://preview.redd.it/5t6y7u8i.jpg?width=140&height=140&crop=1:1,smart&auto=webp&s=x"); got != "https://i.redd.it/5t6y7u8i.jpg" {
		t.Fatalf("fullImage = %q", got)
	}
	if got := fullImage("https://i.redd.it/x.jpg"); got != "" {
		t.Fatalf("i.redd.it input should not match: %q", got)
	}
	if got := fullImage("https://external-preview.redd.it/x.jpeg?width=320"); got != "" {
		t.Fatalf("external-preview should not match: %q", got)
	}
}

func TestMatch(t *testing.T) {
	p := &Plugin{}
	for _, u := range []string{
		"https://www.reddit.com/r/cats/comments/1abcde/x/",
		"https://old.reddit.com/comments/1abcde/",
	} {
		parsed := mustURL(t, u)
		if !p.Match(parsed, pluginapi.CapRender) {
			t.Errorf("Match(%q, CapRender) = false", u)
		}
		if p.Match(parsed, pluginapi.CapFetch) || p.Match(parsed, pluginapi.CapDiscover) {
			t.Errorf("reddit should not claim fetch/discover for %q", u)
		}
	}
	if p.Match(mustURL(t, "https://example.com/r/x/comments/1a/"), pluginapi.CapRender) {
		t.Error("non-reddit host should not match")
	}
}

func TestRenderUnsupported(t *testing.T) {
	p := &Plugin{}
	if _, err := p.Fetch(context.Background(), pluginapi.FetchRequest{}, hostFunc(nil)); err != pluginapi.ErrUnsupportedCapability {
		t.Errorf("Fetch err = %v, want ErrUnsupportedCapability", err)
	}
	if _, err := p.Discover(context.Background(), "https://www.reddit.com/r/cats/", hostFunc(nil)); err != pluginapi.ErrUnsupportedCapability {
		t.Errorf("Discover err = %v, want ErrUnsupportedCapability", err)
	}
}

func TestRenderSubExternalLink(t *testing.T) {
	rss := `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>r/cats</title>
  <entry><id>t3_1abcde</id><link href="https://www.reddit.com/r/cats/comments/1abcde/"/><title>T</title>
    <content type="html">&lt;a href="https://www.reddit.com/r/cats/comments/1abcde/"&gt;t&lt;/a&gt;&lt;a href="https://www.imgur.com/gallery/def"&gt;[link]&lt;/a&gt;</content>
  </entry>
</feed>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/r/cats/") {
			w.Header().Set("Content-Type", "application/atom+xml")
			w.Write([]byte(rss))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	old := RSSBaseURL
	RSSBaseURL = srv.URL
	t.Cleanup(func() { RSSBaseURL = old })

	// The summary has no [link], so the plugin falls back to the subreddit RSS.
	h := hostFunc(func(ctx context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		resp, err := http.Get(req.URL)
		if err != nil {
			return pluginapi.HTTPResponse{}, err
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return pluginapi.HTTPResponse{Status: resp.StatusCode, Body: body}, nil
	})
	m, err := (&Plugin{}).Render(context.Background(), pluginapi.RenderRequest{
		Link:    "https://old.reddit.com/r/cats/comments/1abcde/mittens/",
		Summary: `<img src="https://external-preview.redd.it/x.jpeg">`,
	}, h)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if m.SourceURL != "https://www.imgur.com/gallery/def" {
		t.Fatalf("source = %q", m.SourceURL)
	}
}

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
