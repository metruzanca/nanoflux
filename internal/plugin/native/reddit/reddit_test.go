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
		if !p.Match(parsed, pluginapi.CapSharedKey) {
			t.Errorf("Match(%q, CapSharedKey) = false", u)
		}
		if !p.Match(parsed, pluginapi.CapDiscover) {
			t.Errorf("Match(%q, CapDiscover) = false", u)
		}
		if p.Match(parsed, pluginapi.CapFetch) {
			t.Errorf("reddit should not claim fetch for %q", u)
		}
	}
	if p.Match(mustURL(t, "https://example.com/r/x/comments/1a/"), pluginapi.CapRender) {
		t.Error("non-reddit host should not match")
	}
}

// SharedKeys gives only post fullnames (t3_<id>) a cross-feed SharedKey,
// leaving comments (t1_) and accounts (t2_) without one.
func TestSharedKeys(t *testing.T) {
	p := &Plugin{}
	req := pluginapi.SharedKeyRequest{Items: []pluginapi.Item{
		{GUID: "t3_1abcde"},
		{GUID: "t1_pb71vsb"},
		{GUID: "t2_someone"},
		{GUID: "t3_9Z"}, // base36 is case-insensitive
		{GUID: "t3_"},
	}}
	got, err := p.SharedKeys(context.Background(), req, hostFunc(nil))
	if err != nil {
		t.Fatalf("SharedKeys: %v", err)
	}
	want := map[int]string{0: "reddit:t3_1abcde", 3: "reddit:t3_9Z"}
	if len(got) != len(want) {
		t.Fatalf("shared keys = %+v, want %d", got, len(want))
	}
	for _, e := range got {
		if want[e.Index] != e.SharedKey {
			t.Errorf("item %d shared key = %q, want %q", e.Index, e.SharedKey, want[e.Index])
		}
	}
}

func TestRenderUnsupported(t *testing.T) {
	p := &Plugin{}
	if _, err := p.Fetch(context.Background(), pluginapi.FetchRequest{}, hostFunc(nil)); err != pluginapi.ErrUnsupportedCapability {
		t.Errorf("Fetch err = %v, want ErrUnsupportedCapability", err)
	}
	if cs, err := p.Discover(context.Background(), "https://example.com/page", hostFunc(nil)); err != pluginapi.ErrUnsupportedCapability || cs != nil {
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

// Derive covers the pure URL derivation and normalization for reddit and its
// negative cases. It is the plugin-owned replacement for the former
// discover.Derive.
func TestDiscoverDerive(t *testing.T) {
	cases := []struct {
		page       string
		feedURL    string
		homeURL    string
		title      string
		authorName string
		ok         bool
	}{
		{"https://www.reddit.com/r/golang/", "https://www.reddit.com/r/golang.rss", "https://www.reddit.com/r/golang", "r/golang", "r/golang", true},
		{"https://www.reddit.com/r/golang", "https://www.reddit.com/r/golang.rss", "https://www.reddit.com/r/golang", "r/golang", "r/golang", true},
		{"https://old.reddit.com/r/golang/top/?t=week", "https://www.reddit.com/r/golang.rss", "https://www.reddit.com/r/golang", "r/golang", "r/golang", true},
		{"https://np.reddit.com/r/golang/.rss", "https://www.reddit.com/r/golang.rss", "https://www.reddit.com/r/golang", "r/golang", "r/golang", true},
		{"https://www.reddit.com/r/golang.rss", "https://www.reddit.com/r/golang.rss", "https://www.reddit.com/r/golang", "r/golang", "r/golang", true},
		{"https://www.reddit.com/user/spez", "https://www.reddit.com/user/spez/submitted.rss", "https://www.reddit.com/user/spez", "u/spez", "spez", true},
		{"https://www.reddit.com/u/spez/", "https://www.reddit.com/user/spez/submitted.rss", "https://www.reddit.com/user/spez", "u/spez", "spez", true},
		{"https://m.reddit.com/user/spez/comments", "https://www.reddit.com/user/spez/submitted.rss", "https://www.reddit.com/user/spez", "u/spez", "spez", true},
		{"https://old.reddit.com/u/spez.rss", "https://www.reddit.com/user/spez/submitted.rss", "https://www.reddit.com/user/spez", "u/spez", "spez", true},
		{"https://www.reddit.com/", "", "", "", "", false},
		{"https://www.reddit.com/r/", "", "", "", "", false},
		{"https://www.reddit.com/user/", "", "", "", "", false},
		{"https://example.com/r/golang/", "", "", "", "", false},
		{"not a url", "", "", "", "", false},
	}
	p := &Plugin{}
	for _, c := range cases {
		cs, err := p.Discover(context.Background(), c.page, hostFunc(nil))
		got := pluginapi.Candidate{}
		if len(cs) == 1 {
			got = cs[0]
		}
		ok := err == nil && len(cs) == 1
		if ok != c.ok {
			t.Errorf("Discover(%q) ok = %v, want %v", c.page, ok, c.ok)
			continue
		}
		if !ok {
			if err != nil && err != pluginapi.ErrUnsupportedCapability {
				t.Errorf("Discover(%q) err = %v", c.page, err)
			}
			continue
		}
		if got.FeedURL != c.feedURL || got.HomeURL != c.homeURL || got.Title != c.title || got.AuthorName != c.authorName || !got.Derived {
			t.Errorf("Discover(%q) = %+v, want feed=%q home=%q title=%q author=%q", c.page, got, c.feedURL, c.homeURL, c.title, c.authorName)
		}
	}
}

// TestCanonicalFeedURL covers the redirect-free canonical shape.
func TestCanonicalFeedURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://old.reddit.com/u/gopherfan.rss", "https://www.reddit.com/user/gopherfan/submitted.rss"},
		{"https://www.reddit.com/user/foo.rss", "https://www.reddit.com/user/foo/submitted.rss"},
		{"https://reddit.com/u/foo.rss", "https://www.reddit.com/user/foo/submitted.rss"},
		{"https://www.reddit.com/u/foo/.rss", "https://www.reddit.com/user/foo/submitted.rss"},
		{"https://old.reddit.com/u/foo/.rss", "https://www.reddit.com/user/foo/submitted.rss"},
		{"https://www.reddit.com/u/foo/submitted.rss", "https://www.reddit.com/user/foo/submitted.rss"},
		{"https://www.reddit.com/u/foo/comments.rss", "https://www.reddit.com/user/foo/comments.rss"},
		{"https://www.reddit.com/r/golang/.rss", "https://www.reddit.com/r/golang/.rss"},
		{"https://np.reddit.com/user/foo.rss", "https://www.reddit.com/user/foo/submitted.rss"},
		{"https://m.reddit.com/r/golang.rss", "https://www.reddit.com/r/golang.rss"},
		{"https://example.com/feed.xml", "https://example.com/feed.xml"},
		{"not a url", "not a url"},
	}
	p := &Plugin{}
	for _, c := range cases {
		if got := p.CanonicalizeFeedURL(c.in); got != c.want {
			t.Errorf("CanonicalizeFeedURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestFeedToken maps reddit feed URLs to the category token the same
// subscription represents.
func TestFeedToken(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://www.reddit.com/r/cats.rss", "r/cats"},
		{"https://www.reddit.com/r/cats/.rss", "r/cats"},
		{"https://old.reddit.com/r/GoLang.rss", "r/golang"},
		{"https://www.reddit.com/user/sam/submitted.rss", "u/sam"},
		{"https://www.reddit.com/user/Sam.rss", "u/sam"},
		{"https://www.reddit.com/u/sam/submitted.rss", "u/sam"},
		{"https://example.com/r/cats.rss", ""},
		{"https://www.reddit.com/r/cats/comments/1abc/", "r/cats"},
		{"", ""},
		{"not a url", ""},
	}
	p := &Plugin{}
	for _, c := range cases {
		if got := p.FeedToken(c.in); got != c.want {
			t.Errorf("FeedToken(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Decorate builds "r/cats by u/sam" for a post with both categories, carrying
// tokens the host resolves, and classifies the card kind.
func TestDecorate(t *testing.T) {
	p := &Plugin{}
	req := pluginapi.DecorateRequest{Items: []pluginapi.Item{
		{Categories: []string{"r/cats", "u/sam"}, Link: "https://www.reddit.com/r/cats/comments/1a/x/"},
		{Categories: []string{"reblog"}}, // not reddit-shaped
	}}
	got, err := p.Decorate(context.Background(), req)
	if err != nil {
		t.Fatalf("Decorate: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("decorations = %+v", got)
	}
	// The reddit-shaped item gets attribution; the non-reddit one gets a kind
	// but no attribution.
	if len(got[0].Attribution) == 0 {
		t.Fatalf("first item should have attribution: %+v", got[0])
	}
	if len(got[1].Attribution) != 0 {
		t.Fatalf("second (non-reddit) item should have no attribution: %+v", got[1])
	}
	d := got[0]
	if d.Kind != pluginapi.KindText {
		t.Errorf("kind = %v, want KindText", d.Kind)
	}
	if len(d.Attribution) != 3 {
		t.Fatalf("attribution = %+v", d.Attribution)
	}
	if d.Attribution[0].Token != "r/cats" || d.Attribution[0].Text != "r/cats" || d.Attribution[0].URL == "" {
		t.Errorf("sub part = %+v", d.Attribution[0])
	}
	if d.Attribution[1].Text != "by" || d.Attribution[1].Token != "" || d.Attribution[1].URL != "" {
		t.Errorf("separator = %+v", d.Attribution[1])
	}
	if d.Attribution[2].Token != "u/sam" || d.Attribution[2].URL != "https://www.reddit.com/user/sam/" {
		t.Errorf("user part = %+v", d.Attribution[2])
	}
}

// Decorate classifies a gallery (square crop) as KindGallery and overrides the
// row thumbnail with the full-res first image.
func TestDecorateGalleryThumb(t *testing.T) {
	p := &Plugin{}
	req := pluginapi.DecorateRequest{Items: []pluginapi.Item{{
		Categories: []string{"r/cats", "u/sam"},
		ImageURL:   "https://preview.redd.it/abc123.jpeg?width=140&height=140&crop=1:1,smart&s=x",
	}}}
	got, err := p.Decorate(context.Background(), req)
	if err != nil || len(got) != 1 {
		t.Fatalf("Decorate = %+v, %v", got, err)
	}
	if got[0].Kind != pluginapi.KindGallery {
		t.Errorf("kind = %v, want KindGallery", got[0].Kind)
	}
	if got[0].ThumbURL != "https://i.redd.it/abc123.jpg" {
		t.Errorf("thumb = %q", got[0].ThumbURL)
	}
}

// Decorate derives a cross-feed dedupe key from a link post's external
// destination and poster, so the same link crossposted under different titles
// collapses. A post with no [link] anchor, or no poster category, gets no key.
func TestDecorateLinkDedupeKey(t *testing.T) {
	p := &Plugin{}
	linkContent := `<p><a href="https://www.Example.com/Article/?utm_source=reddit&id=1#top">[link]</a></p>`
	req := pluginapi.DecorateRequest{Items: []pluginapi.Item{
		{Categories: []string{"r/cats", "u/sam"}, Summary: linkContent},
		{Categories: []string{"r/dogs", "u/sam"}, Summary: linkContent},
		{Categories: []string{"r/cats"}, Summary: linkContent}, // no poster
		{Categories: []string{"r/cats", "u/sam"}, Summary: "<p>self post, no link</p>"},
	}}
	got, err := p.Decorate(context.Background(), req)
	if err != nil || len(got) != 4 {
		t.Fatalf("Decorate = %+v, %v", got, err)
	}
	want := "u/sam|https://www.example.com/Article?id=1"
	if got[0].DedupeKey != want {
		t.Errorf("dedupe key = %q, want %q", got[0].DedupeKey, want)
	}
	// The same link posted to a different sub by the same user shares the key.
	if got[1].DedupeKey != want {
		t.Errorf("crosspost key = %q, want %q", got[1].DedupeKey, want)
	}
	if got[2].DedupeKey != "" {
		t.Errorf("no-poster item should have no key: %q", got[2].DedupeKey)
	}
	if got[3].DedupeKey != "" {
		t.Errorf("self post should have no key: %q", got[3].DedupeKey)
	}
}

func TestLinkDedupeKey(t *testing.T) {
	key := linkDedupeKey("https://WWW.Example.com/Path/?b=2&utm_medium=x&a=1#frag", "u/sam")
	if want := "u/sam|https://www.example.com/Path?a=1&b=2"; key != want {
		t.Errorf("linkDedupeKey = %q, want %q", key, want)
	}
	if linkDedupeKey("not a url", "u/sam") != "" {
		t.Error("unparseable destination should yield no key")
	}
	if linkDedupeKey("https://x.com/a", "") != "" {
		t.Error("missing author should yield no key")
	}
	if !strings.HasPrefix(linkDedupeKey("https://x.com/a", "u/Sam"), "u/sam|") {
		t.Error("author should be lowercased")
	}
}

// TestSearchURLNotOwned asserts the reddit plugin leaves search URLs for a
// search-owning plugin: discovery and the URL policy do not treat /search or
// /r/{sub}/search as a subreddit feed, while the per-post shared key still
// applies so search results cross-dedupe with subscribed feeds.
func TestSearchURLNotOwned(t *testing.T) {
	p := &Plugin{}
	search := mustURL(t, "https://old.reddit.com/r/cats/search?q=foo&restrict_sr=on")
	if p.Match(search, pluginapi.CapDiscover) {
		t.Error("Match(search, CapDiscover) should be false")
	}
	if p.Match(search, pluginapi.CapURLPolicy) {
		t.Error("Match(search, CapURLPolicy) should be false")
	}
	if p.Match(search, pluginapi.CapDocs) {
		t.Error("Match(search, CapDocs) should be false")
	}
	// The post-shared-key capability is feed-URL matched and still applies: the
	// items are ordinary t3_ posts.
	if !p.Match(search, pluginapi.CapSharedKey) {
		t.Error("Match(search, CapSharedKey) should be true")
	}

	// The URL policy must not rewrite or mis-derive a search URL.
	raw := "https://old.reddit.com/r/cats/search?q=foo"
	if got := p.CanonicalizeFeedURL(raw); got != raw {
		t.Errorf("CanonicalizeFeedURL(%q) = %q, want unchanged", raw, got)
	}
	if tok := p.FeedToken(raw); tok != "" {
		t.Errorf("FeedToken(%q) = %q, want empty", raw, tok)
	}
	if cs, err := p.Discover(context.Background(), raw, hostFunc(nil)); err != pluginapi.ErrUnsupportedCapability || cs != nil {
		t.Errorf("Discover(search) = %v, %v; want ErrUnsupportedCapability", cs, err)
	}
}
