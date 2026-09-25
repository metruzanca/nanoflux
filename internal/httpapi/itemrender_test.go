package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/plugin"
	reddit "github.com/metruzanca/nanoflux/internal/plugin/native/reddit"
	"github.com/metruzanca/nanoflux/internal/store"
)

// withRedditPlugin attaches the native reddit renderer to the server, pointing
// its base URLs at the given test servers.
func withRedditPlugin(t *testing.T, s *Server, rssURL, embedURL string) {
	t.Helper()
	oldRSS, oldEmbed := reddit.RSSBaseURL, reddit.EmbedBaseURL
	reddit.RSSBaseURL, reddit.EmbedBaseURL = rssURL, embedURL
	t.Cleanup(func() { reddit.RSSBaseURL, reddit.EmbedBaseURL = oldRSS, oldEmbed })

	reg := plugin.NewRegistry()
	reg.RegisterNative(&reddit.Plugin{})
	s.SetPlugins(reg, plugin.NewHosts(s.client, plugin.NewCooldown()))
}

// redditItem stores a reddit post under a fresh feed and returns the server,
// handler, session cookie, and owner user id for the item-view request.
func redditItem(t *testing.T, item store.Item) (*Server, http.Handler, *http.Cookie, int64) {
	t.Helper()
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Merari01", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Merari01", "https://www.reddit.com/user/Merari01/.rss", "", "", 900)
	item.FetchedAt = db.Now()
	if _, err := s.store.Items.Upsert(f.ID, item); err != nil {
		t.Fatal(err)
	}
	return s, h, cookie, u.ID
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

	s, h, cookie, uid := redditItem(t, store.Item{
		GUID: "t3_1abcde", Title: "Mittens enjoys a sunny nap",
		Link:     "https://old.reddit.com/r/cats/comments/1abcde/mittens_enjoys_a_sunny_nap/",
		ImageURL: "https://external-preview.redd.it/1q2w3e4r.jpeg?width=320",
		Summary:  `<a href="https://www.reddit.com/r/cats/comments/1abcde/"><img src="https://external-preview.redd.it/1q2w3e4r.jpeg?width=320" alt="Mittens enjoys a sunny nap"></a>`,
	})
	withRedditPlugin(t, s, redditSrv.URL, redditSrv.URL)

	items, _ := s.store.Items.List(uid, store.ItemFilter{})
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

func TestItemViewImagePost(t *testing.T) {
	// A reddit image post's [link] points straight at the image (i.redd.it),
	// not at an external page. There is no oEmbed for a JPEG, so the post must
	// still render the image as its content rather than an empty modal.
	redditSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("subreddit RSS should not be fetched when summary has [link]")
		http.NotFound(w, r)
	}))
	defer redditSrv.Close()

	s, h, cookie, uid := redditItem(t, store.Item{
		GUID: "t3_1abcde", Title: "Mittens enjoys a sunny nap",
		Link:     "https://www.reddit.com/r/cats/comments/1abcde/mittens_enjoys_a_sunny_nap/",
		ImageURL: "https://preview.redd.it/1q2w3e4r.jpeg?width=640&crop=smart&auto=webp&s=x",
		Summary:  `<table><tr><td><a href="https://www.reddit.com/r/cats/comments/1abcde/mittens_enjoys_a_sunny_nap/"><img src="https://preview.redd.it/1q2w3e4r.jpeg?width=640&amp;crop=smart" alt="Mittens enjoys a sunny nap"></a></td><td> submitted by <a href="https://www.reddit.com/user/u">/u/u</a> <span><a href="https://i.redd.it/1q2w3e4r.jpeg">[link]</a></span> <span><a href="https://www.reddit.com/r/cats/comments/1abcde/mittens_enjoys_a_sunny_nap/">[comments]</a></span></td></tr></table>`,
	})
	withRedditPlugin(t, s, redditSrv.URL, redditSrv.URL)

	items, _ := s.store.Items.List(uid, store.ItemFilter{})
	rr := doGet(h, "/items/"+itoa(items[0].ID)+"/view", cookie)
	body := rr.Body.String()
	if rr.Code != http.StatusOK {
		t.Fatalf("item view: %d %s", rr.Code, body)
	}
	if !strings.Contains(body, `src="https://i.redd.it/1q2w3e4r.jpeg"`) {
		t.Fatalf("image post should render the full-res image: %s", body)
	}
	if strings.Contains(body, `class="external">source`) {
		t.Fatalf("image post should not show an external source link: %s", body)
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

	s, h, cookie, uid := redditItem(t, store.Item{
		GUID: "t3_1fghij", Title: "Whiskers at golden hour",
		Link:     "https://old.reddit.com/r/cats/comments/1fghij/whiskers_at_golden_hour/",
		ImageURL: "https://preview.redd.it/5t6y7u8i.jpg?width=140&height=140&crop=1:1,smart&auto=webp&s=x",
		Summary:  `<a href="https://www.reddit.com/r/cats/comments/1fghij/"><img src="https://preview.redd.it/5t6y7u8i.jpg?width=140&amp;height=140" alt="Whiskers at golden hour"></a>`,
	})
	withRedditPlugin(t, s, redditSrv.URL, embedSrv.URL)

	items, _ := s.store.Items.List(uid, store.ItemFilter{})
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

func TestItemViewMediaEmbed(t *testing.T) {
	// An item from a media plugin carries a video enclosure and links to a
	// watch page that publishes oEmbed. The modal should render the provider's
	// iframe player, not a bare <video>.
	oembedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/watch/abc":
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(`<html><head><link rel="alternate" type="application/json+oembed" href="/oembed"></head></html>`))
		case "/oembed":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"html":"<iframe src='https://embed.example/ifr/abc' width='1080' height='1920' allowfullscreen></iframe>"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer oembedSrv.Close()

	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Someone", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Someone", oembedSrv.URL+"/feed", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{
		GUID: "media:1", Title: "A clip",
		Link:      oembedSrv.URL + "/watch/abc",
		ImageURL:  "https://media.example/abc-poster.jpg",
		Summary:   `<img src="https://media.example/abc-poster.jpg"/>`,
		FetchedAt: db.Now(),
	})
	items, _ := s.store.Items.List(u.ID, store.ItemFilter{})
	if err := s.store.Items.ReplaceEnclosures(items[0].ID, []store.Enclosure{
		{URL: "https://media.example/abc.mp4", MIMEType: "video/mp4"},
	}); err != nil {
		t.Fatal(err)
	}

	rr := doGet(h, "/items/"+itoa(items[0].ID)+"/view", cookie)
	body := rr.Body.String()
	if rr.Code != http.StatusOK {
		t.Fatalf("item view: %d %s", rr.Code, body)
	}
	if !strings.Contains(body, `src="https://embed.example/ifr/abc"`) {
		t.Fatalf("item with a playable enclosure should embed its link's oEmbed player: %s", body)
	}
	if strings.Contains(body, "<video") {
		t.Fatalf("embedded player should replace the bare video element: %s", body)
	}
}
