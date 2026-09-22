package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/store"
)

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

func TestRedditPostID(t *testing.T) {
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
		sub, id, ok := redditPostID(c.in)
		if sub != c.sub || id != c.id || ok != c.ok {
			t.Errorf("redditPostID(%q) = %q %q %v, want %q %q %v", c.in, sub, id, ok, c.sub, c.id, c.ok)
		}
	}
}

func TestRedditLinkPostThumb(t *testing.T) {
	if !redditLinkPostThumb("https://external-preview.redd.it/abc.jpeg?width=320") {
		t.Fatal("external-preview should count as a link post thumb")
	}
	if redditLinkPostThumb("https://preview.redd.it/abc.jpeg?width=640") {
		t.Fatal("preview.redd.it is not a link post thumb")
	}
	if redditLinkPostThumb("") {
		t.Fatal("empty thumb should not match")
	}
}

func TestRedditSubExternalLink(t *testing.T) {
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

	old := redditRSSBaseURL
	redditRSSBaseURL = srv.URL
	t.Cleanup(func() { redditRSSBaseURL = old })

	s, _ := newTestServer(t)
	u, ok := s.redditSubExternalLink(context.Background(), "cats", "1abcde")
	if !ok || u != "https://www.imgur.com/gallery/def" {
		t.Fatalf("sub lookup = %q %v", u, ok)
	}
	if _, ok := s.redditSubExternalLink(context.Background(), "cats", "missing"); ok {
		t.Fatal("missing post resolved")
	}
}

func TestItemViewLinkPost(t *testing.T) {
	// The reddit sub RSS answers with a [link] anchor pointing at an external
	// page that publishes an oEmbed endpoint.
	oembedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/watch/abc":
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(`<html><head><link rel="alternate" type="application/json+oembed" href="/oembed"></head></html>`))
		case "/oembed":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"title":"Clip","provider_name":"Mock","html":"<iframe src='https://embed.example/ifr/abc' width='1080' height='1920' allowfullscreen></iframe>"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer oembedSrv.Close()

	rss := `<feed xmlns="http://www.w3.org/2005/Atom"><title>r/cats</title>` +
		`<entry><id>t3_1abcde</id><link href="https://www.reddit.com/r/cats/comments/1abcde/"/><title>T</title>` +
		`<content type="html">&lt;a href="` + oembedSrv.URL + `/watch/abc"&gt;[link]&lt;/a&gt;</content></entry></feed>`
	redditSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/atom+xml")
		w.Write([]byte(rss))
	}))
	defer redditSrv.Close()

	old := redditRSSBaseURL
	redditRSSBaseURL = redditSrv.URL
	t.Cleanup(func() { redditRSSBaseURL = old })

	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Merari01", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Merari01", "https://www.reddit.com/user/Merari01/.rss", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{
		GUID: "t3_1abcde", Title: "Mittens enjoys a sunny nap",
		Link:      "https://old.reddit.com/r/cats/comments/1abcde/mittens_enjoys_a_sunny_nap/",
		ImageURL:  "https://external-preview.redd.it/1q2w3e4r.jpeg?width=320",
		Summary:   `<a href="https://www.reddit.com/r/cats/comments/1abcde/"><img src="https://external-preview.redd.it/1q2w3e4r.jpeg?width=320" alt="Mittens enjoys a sunny nap"></a>`,
		FetchedAt: db.Now(),
	})

	items, _ := s.store.Items.List(u.ID, store.ItemFilter{})
	rr := doGet(h, "/items/"+itoa(items[0].ID)+"/view", cookie)
	body := rr.Body.String()
	if rr.Code != http.StatusOK {
		t.Fatalf("item view: %d %s", rr.Code, body)
	}
	// The reddit link post's destination ("source") is an entry in the modal's
	// ⋯ menu, and "open live" (the reddit permalink) is in the meta line.
	if !strings.Contains(body, `href="`+oembedSrv.URL+`/watch/abc"`) || !strings.Contains(body, `>source</a>`) {
		t.Fatalf("modal missing source link in the menu: %s", body)
	}
	if !strings.Contains(body, `src="https://embed.example/ifr/abc"`) {
		t.Fatalf("modal missing oembed embed: %s", body)
	}
	if strings.Contains(body, "item-body") {
		t.Fatalf("link post should not render its thumbnail body: %s", body)
	}
}

func TestItemViewLinkPostFromSummary(t *testing.T) {
	// Native reddit feeds carry the [link] anchor in the summary itself, so no
	// subreddit RSS lookup is needed.
	oembedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/watch/abc":
			w.Write([]byte(`<link rel="alternate" type="application/json+oembed" href="/oembed">`))
		case "/oembed":
			w.Write([]byte(`{"html":"<iframe src='https://embed.example/ifr/abc'></iframe>"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer oembedSrv.Close()

	redditSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("subreddit RSS should not be fetched when summary has [link]")
		http.NotFound(w, r)
	}))
	defer redditSrv.Close()

	old := redditRSSBaseURL
	redditRSSBaseURL = redditSrv.URL
	t.Cleanup(func() { redditRSSBaseURL = old })

	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Merari01", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Merari01", "https://www.reddit.com/user/Merari01/.rss", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{
		GUID: "t3_1abcde", Title: "Mittens enjoys a sunny nap",
		Link:      "https://www.reddit.com/r/cats/comments/1abcde/mittens_enjoys_a_sunny_nap/",
		ImageURL:  "https://external-preview.redd.it/x.jpeg?width=320",
		Summary:   `<a href="` + oembedSrv.URL + `/watch/abc">[link]</a>`,
		FetchedAt: db.Now(),
	})

	items, _ := s.store.Items.List(u.ID, store.ItemFilter{})
	rr := doGet(h, "/items/"+itoa(items[0].ID)+"/view", cookie)
	body := rr.Body.String()
	if !strings.Contains(body, `src="https://embed.example/ifr/abc"`) {
		t.Fatalf("modal missing embed from summary [link]: %s", body)
	}
}

func TestRedditGalleryID(t *testing.T) {
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
		id, ok := redditGalleryID(c.in)
		if id != c.id || ok != c.ok {
			t.Errorf("redditGalleryID(%q) = %q %v, want %q %v", c.in, id, ok, c.id, c.ok)
		}
	}
}

func TestRedditGalleryImagesFromHTML(t *testing.T) {
	body := []byte(`<html><img src="https://preview.redd.it/whiskers-v0-9z8x7c6v.jpg?width=320&amp;crop=smart&amp;auto=webp&amp;s=abc">
<img src="https://preview.redd.it/whiskers-v0-5t6y7u8i.jpg?width=640&amp;crop=smart&amp;auto=webp&amp;s=def">
<img src="https://preview.redd.it/whiskers-v0-9z8x7c6v.jpg?width=1080&amp;crop=smart&amp;auto=webp&amp;s=ghi"></html>`)
	got := redditGalleryImagesFromHTML(body)
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

func TestFullRedditImage(t *testing.T) {
	if got := fullRedditImage("https://preview.redd.it/5t6y7u8i.jpg?width=140&height=140&crop=1:1,smart&auto=webp&s=x"); got != "https://i.redd.it/5t6y7u8i.jpg" {
		t.Fatalf("fullRedditImage = %q", got)
	}
	if got := fullRedditImage("https://i.redd.it/x.jpg"); got != "" {
		t.Fatalf("i.redd.it input should not match: %q", got)
	}
	if got := fullRedditImage("https://external-preview.redd.it/x.jpeg?width=320"); got != "" {
		t.Fatalf("external-preview should not match: %q", got)
	}
}

func TestItemViewGallery(t *testing.T) {
	// The proxy-stripped summary has no [link]; the subreddit RSS reveals the
	// gallery URL and the embed page enumerates both images.
	embedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/r/cats/comments/1fghij") {
			w.Write([]byte(`<html><img src="https://preview.redd.it/gallery-v0-9z8x7c6v.jpg?width=320&amp;s=a"><img src="https://preview.redd.it/gallery-v0-5t6y7u8i.jpg?width=320&amp;s=b"></html>`))
			return
		}
		http.NotFound(w, r)
	}))
	defer embedSrv.Close()

	rss := `<feed xmlns="http://www.w3.org/2005/Atom"><title>r/cats</title>` +
		`<entry><id>t3_1fghij</id><link href="https://www.reddit.com/r/cats/comments/1fghij/"/><title>T</title>` +
		`<content type="html">&lt;a href="https://www.reddit.com/gallery/1fghij"&gt;[link]&lt;/a&gt;</content></entry></feed>`
	redditSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/atom+xml")
		w.Write([]byte(rss))
	}))
	defer redditSrv.Close()

	oldRSS, oldEmbed := redditRSSBaseURL, redditEmbedBaseURL
	redditRSSBaseURL, redditEmbedBaseURL = redditSrv.URL, embedSrv.URL
	t.Cleanup(func() { redditRSSBaseURL, redditEmbedBaseURL = oldRSS, oldEmbed })

	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Merari01", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Merari01", "https://www.reddit.com/user/Merari01/.rss", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{
		GUID: "t3_1fghij", Title: "Whiskers at golden hour",
		Link:      "https://old.reddit.com/r/cats/comments/1fghij/whiskers_at_golden_hour/",
		ImageURL:  "https://preview.redd.it/5t6y7u8i.jpg?width=140&height=140&crop=1:1,smart&auto=webp&s=8f95727e4cbcb3293619f2368f058017fbe13f5b",
		Summary:   `<a href="https://www.reddit.com/r/cats/comments/1fghij/"><img src="https://preview.redd.it/5t6y7u8i.jpg?width=140&amp;height=140" alt="Whiskers at golden hour"></a>`,
		FetchedAt: db.Now(),
	})

	items, _ := s.store.Items.List(u.ID, store.ItemFilter{})
	rr := doGet(h, "/items/"+itoa(items[0].ID)+"/view", cookie)
	body := rr.Body.String()
	if rr.Code != http.StatusOK {
		t.Fatalf("item view: %d %s", rr.Code, body)
	}
	if !strings.Contains(body, `<div class="gallery">`) {
		t.Fatalf("gallery post should render a gallery: %s", body)
	}
	for _, img := range []string{"https://i.redd.it/9z8x7c6v.jpg", "https://i.redd.it/5t6y7u8i.jpg"} {
		if !strings.Contains(body, img) {
			t.Fatalf("gallery missing %s: %s", img, body)
		}
	}
	if strings.Contains(body, `class="external">source`) {
		t.Fatalf("gallery should not show an external source link: %s", body)
	}
}

func TestItemViewGalleryFallback(t *testing.T) {
	// When the embed page is unreachable, the item still renders without a
	// gallery or source.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer srv.Close()

	oldRSS, oldEmbed := redditRSSBaseURL, redditEmbedBaseURL
	redditRSSBaseURL, redditEmbedBaseURL = srv.URL, srv.URL
	t.Cleanup(func() { redditRSSBaseURL, redditEmbedBaseURL = oldRSS, oldEmbed })

	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Merari01", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Merari01", "https://www.reddit.com/user/Merari01/.rss", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{
		GUID: "t3_1fghij", Title: "Whiskers at golden hour",
		Link:      "https://old.reddit.com/r/cats/comments/1fghij/whiskers_at_golden_hour/",
		ImageURL:  "https://preview.redd.it/5t6y7u8i.jpg?width=140&height=140",
		Summary:   `<a href="https://www.reddit.com/r/cats/comments/1fghij/"><img src="https://preview.redd.it/5t6y7u8i.jpg"></a>`,
		FetchedAt: db.Now(),
	})

	items, _ := s.store.Items.List(u.ID, store.ItemFilter{})
	rr := doGet(h, "/items/"+itoa(items[0].ID)+"/view", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("item view should degrade: %d %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), `class="gallery"`) {
		t.Fatalf("fallback should omit the gallery: %s", rr.Body.String())
	}
}

func TestItemViewLinkPostFallback(t *testing.T) {
	// When the subreddit RSS is unreachable, the item still renders, just
	// without the source link or embed.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer srv.Close()

	old := redditRSSBaseURL
	redditRSSBaseURL = srv.URL
	t.Cleanup(func() { redditRSSBaseURL = old })

	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Merari01", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Merari01", "https://www.reddit.com/user/Merari01/.rss", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{
		GUID: "t3_1abcde", Title: "Mittens enjoys a sunny nap",
		Link:      "https://old.reddit.com/r/cats/comments/1abcde/mittens_enjoys_a_sunny_nap/",
		ImageURL:  "https://external-preview.redd.it/1q2w3e4r.jpeg?width=320",
		Summary:   `<a href="https://www.reddit.com/r/cats/comments/1abcde/"><img src="https://external-preview.redd.it/x.jpeg"></a>`,
		FetchedAt: db.Now(),
	})

	items, _ := s.store.Items.List(u.ID, store.ItemFilter{})
	rr := doGet(h, "/items/"+itoa(items[0].ID)+"/view", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("item view should degrade gracefully: %d %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if strings.Contains(body, `class="external">source`) || strings.Contains(body, `src="https://embed.example`) {
		t.Fatalf("fallback should omit source/embed: %s", body)
	}
}
