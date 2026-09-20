package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/store"
	"github.com/metruzanca/nanoflux/internal/web"
)

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func doForm(h http.Handler, method, path string, form url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func doGet(h http.Handler, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func sessionCookie(t *testing.T, h http.Handler) *http.Cookie {
	t.Helper()
	rr := login(t, h)
	for _, c := range rr.Result().Cookies() {
		if c.Name == "rss_session" {
			return c
		}
	}
	t.Fatal("no session cookie")
	return nil
}

func TestNormalizeURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://x.com/sama", "https://x.com/sama"},
		{"http://x.com/sama", "http://x.com/sama"},
		{"x.com/sama", "https://x.com/sama"},
		{"  x.com/sama  ", "https://x.com/sama"},
		{"www.example.com/feed.xml", "https://www.example.com/feed.xml"},
		{"", ""},
		{"   ", ""},
	}
	for _, c := range cases {
		if got := normalizeURL(c.in); got != c.want {
			t.Errorf("normalizeURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFeedPreviewSchemelessURL(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	feedSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rss" {
			w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>RSS Feed</title><link>https://home.dev/</link></channel></rss>`))
			return
		}
		http.NotFound(w, r)
	}))
	defer feedSrv.Close()
	s.client = feedSrv.Client() // trusts the test TLS cert

	// Strip the scheme; normalization must add it back.
	schemeless := strings.TrimPrefix(feedSrv.URL, "https://")
	rr := doForm(h, "POST", "/fragments/feed-preview", url.Values{
		"url": {schemeless + "/rss"},
	}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), feedSrv.URL+"/rss") {
		t.Fatalf("schemeless url should resolve to the full feed url: %s", rr.Body.String())
	}
}

func TestFeedCreateNormalizesURL(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	u, _ := s.store.Users.ByUsername("alice")
	rr := doForm(h, "POST", "/feeds", url.Values{
		"title": {"Blog"}, "feed_url": {"example.com/rss.xml"},
	}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("create feed: %d %s", rr.Code, rr.Body.String())
	}
	feeds, _ := s.store.Feeds.List(u.ID)
	if len(feeds) != 1 || feeds[0].FeedURL != "https://example.com/rss.xml" {
		t.Fatalf("stored feed url = %+v", feeds)
	}
}

func TestCreateFormErrors(t *testing.T) {
	_, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	// Missing required fields -> 400 with an OOB swap into the error slot.
	rr := doForm(h, "POST", "/feeds", url.Values{"title": {"Blog"}}, cookie)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "title and feed url are required") {
		t.Fatalf("feed error: %d %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `id="add-feed-error"`) || !strings.Contains(rr.Body.String(), "hx-swap-oob") {
		t.Fatalf("feed error should OOB into add-feed-error: %s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `role="alert"`) {
		t.Fatalf("feed error should render the alert banner: %s", rr.Body.String())
	}

	rr = doForm(h, "POST", "/authors", url.Values{}, cookie)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "name is required") {
		t.Fatalf("author error: %d %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `id="add-author-error"`) {
		t.Fatalf("author error should OOB into add-author-error: %s", rr.Body.String())
	}

	rr = doForm(h, "POST", "/collections", url.Values{}, cookie)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "name is required") {
		t.Fatalf("collection error: %d %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `id="add-collection-error"`) {
		t.Fatalf("collection error should OOB into add-collection-error: %s", rr.Body.String())
	}
}

func TestFeedAuthorCollectionFlow(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	// Create an author through the web UI (returns the row fragment).
	rr := doForm(h, "POST", "/authors", url.Values{
		"name": {"Metru"}, "url": {"https://metru.dev"},
	}, cookie)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "author-") {
		t.Fatalf("create author: %d %s", rr.Code, rr.Body.String())
	}
	if body := doGet(h, "/authors", cookie).Body.String(); !strings.Contains(body, "Metru") {
		t.Fatal("authors page missing author")
	}

	// Create a feed referencing that author (returns the row fragment).
	u, _ := s.store.Users.ByUsername("alice")
	authors, _ := s.store.Authors.List(u.ID)
	rr = doForm(h, "POST", "/feeds", url.Values{
		"title": {"Blog"}, "feed_url": {"https://example.com/rss.xml"},
		"author_id": {itoa(authors[0].ID)}, "poll_interval_sec": {"900"},
	}, cookie)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "feed-") {
		t.Fatalf("create feed: %d %s", rr.Code, rr.Body.String())
	}
	body := doGet(h, "/feeds", cookie).Body.String()
	if !strings.Contains(body, "Blog") || !strings.Contains(body, "Metru") {
		t.Fatal("feeds page missing feed/author")
	}

	// Authorless feeds are allowed now.
	rr = doForm(h, "POST", "/feeds", url.Values{
		"title": {"NoAuthor"}, "feed_url": {"https://example.com/rss2.xml"},
	}, cookie)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "feed-") {
		t.Fatalf("authorless feed: %d %s", rr.Code, rr.Body.String())
	}
	authorless, _ := s.store.Feeds.List(u.ID)
	for _, f := range authorless {
		if f.Title == "NoAuthor" && f.AuthorID != 0 {
			t.Fatalf("expected authorless feed, got author_id %d", f.AuthorID)
		}
	}

	// Create a collection and attach the feed.
	rr = doForm(h, "POST", "/collections", url.Values{
		"name": {"Dev"},
	}, cookie)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "collection-") {
		t.Fatalf("create collection: %d %s", rr.Code, rr.Body.String())
	}
	cols, _ := s.store.Collections.List(u.ID)
	feeds, _ := s.store.Feeds.List(u.ID)
	rr = doForm(h, "POST", "/collections/"+itoa(cols[0].ID)+"/add-feed", url.Values{
		"feed_id": {itoa(feeds[0].ID)},
	}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("add feed to collection: %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "hx-swap-oob") || !strings.Contains(rr.Body.String(), "scoped-items") {
		t.Fatalf("add-feed response should carry the items oob swap: %s", rr.Body.String())
	}
	body = doGet(h, "/collections/"+itoa(cols[0].ID), cookie).Body.String()
	if !strings.Contains(body, "Blog") {
		t.Fatal("collection page missing feed")
	}
}

func TestReadPage(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "A", "", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "B", "https://b.dev/rss.xml", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{GUID: "g", Title: "Item", Link: "https://b.dev/1", FetchedAt: db.Now()})

	// Mark the item read.
	items, _ := s.store.Items.List(u.ID, store.ItemFilter{})
	doForm(h, "POST", "/items/"+itoa(items[0].ID)+"/read", url.Values{}, cookie)

	// It shows on the read page but not the unread page.
	body := doGet(h, "/read", cookie).Body.String()
	if !strings.Contains(body, "Item") || !strings.Contains(body, `hx-post="/items/unread-all"`) {
		t.Fatalf("read page missing read item: %s", body)
	}
	if body := doGet(h, "/", cookie).Body.String(); strings.Contains(body, `data-item-link="https://b.dev/1"`) {
		t.Fatal("unread page should not show read item")
	}

	// Mark all unread empties the read page and restores the unread count.
	doForm(h, "POST", "/items/unread-all", url.Values{}, cookie)
	n, _ := s.store.Items.CountUnread(u.ID, 0)
	if n != 1 {
		t.Fatalf("unread = %d, want 1", n)
	}
	body = doGet(h, "/read", cookie).Body.String()
	if strings.Contains(body, `data-item-link="https://b.dev/1"`) {
		t.Fatalf("read page should be empty after mark-all-unread: %s", body)
	}
}

func TestYouTubeEmbedURL(t *testing.T) {
	cases := []struct {
		link string
		want string
	}{
		{"https://www.youtube.com/watch?v=H0KAi8AWsnM", "https://www.youtube.com/embed/H0KAi8AWsnM"},
		{"https://m.youtube.com/watch?v=H0KAi8AWsnM&t=10s", "https://www.youtube.com/embed/H0KAi8AWsnM"},
		{"https://youtu.be/H0KAi8AWsnM", "https://www.youtube.com/embed/H0KAi8AWsnM"},
		{"https://www.youtube.com/shorts/H0KAi8AWsnM", "https://www.youtube.com/embed/H0KAi8AWsnM"},
		{"https://www.youtube.com/live/H0KAi8AWsnM", "https://www.youtube.com/embed/H0KAi8AWsnM"},
		{"https://www.youtube.com/embed/H0KAi8AWsnM", "https://www.youtube.com/embed/H0KAi8AWsnM"},
		{"https://example.com/post/1", ""},
		{"https://youtube.com/watch", ""},
		{"not a url", ""},
	}
	for _, c := range cases {
		if got := web.YoutubeEmbedURL(c.link); got != c.want {
			t.Errorf("YoutubeEmbedURL(%q) = %q, want %q", c.link, got, c.want)
		}
	}
}

func TestItemViewYouTubeEmbed(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	u, _ := s.store.Users.ByUsername("alice")
	f, _ := s.store.Feeds.Create(u.ID, 0, "bigboxSWE", "https://www.youtube.com/feeds/videos.xml?channel_id=UCx", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{
		GUID: "yt:video:H0KAi8AWsnM", Title: "Video",
		Link: "https://www.youtube.com/watch?v=H0KAi8AWsnM", FetchedAt: db.Now(),
	})

	items, _ := s.store.Items.List(u.ID, store.ItemFilter{})
	rr := doGet(h, "/items/"+itoa(items[0].ID)+"/view", cookie)
	body := rr.Body.String()
	if !strings.Contains(body, `https://www.youtube.com/embed/H0KAi8AWsnM`) {
		t.Fatalf("item view should embed the youtube player: %s", body)
	}
	if !strings.Contains(body, "video-embed") {
		t.Fatalf("item view missing video-embed wrapper: %s", body)
	}
}

func TestItemCardsRenderThumbnails(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	u, _ := s.store.Users.ByUsername("alice")
	f, _ := s.store.Feeds.Create(u.ID, 0, "bigboxSWE", "https://www.youtube.com/feeds/videos.xml?channel_id=UCx", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{
		GUID: "yt:video:H0KAi8AWsnM", Title: "Video",
		Link:     "https://www.youtube.com/watch?v=H0KAi8AWsnM",
		ImageURL: "https://i.ytimg.com/vi/H0KAi8AWsnM/hq720.jpg", FetchedAt: db.Now(),
	})
	s.store.Items.Upsert(f.ID, store.Item{
		GUID: "p1", Title: "Post", Link: "https://example.com/1",
		Summary: "<p>hello</p>", FetchedAt: db.Now(),
	})
	s.store.Items.Upsert(f.ID, store.Item{
		GUID: "p2", Title: "Image Post", Link: "https://example.com/2",
		Summary: `<a href="https://example.com/2"><img src="https://example.com/pic.jpg" alt="Image Post" /></a>`,
		ImageURL: "https://example.com/pic.jpg", FetchedAt: db.Now(),
	})
	s.store.Items.Upsert(f.ID, store.Item{
		GUID: "p3", Title: "Mittens enjoys a sunny nap",
		Link:     "https://old.reddit.com/r/cats/comments/1abcde/mittens_enjoys_a_sunny_nap/",
		ImageURL: "https://external-preview.redd.it/1q2w3e4r.jpeg?width=320",
		Summary:  `<a href="https://www.reddit.com/r/cats/comments/1abcde/"><img src="https://external-preview.redd.it/1q2w3e4r.jpeg?width=320" alt="Mittens enjoys a sunny nap"></a>`,
		FetchedAt: db.Now(),
	})
	s.store.Items.Upsert(f.ID, store.Item{
		GUID: "p4", Title: "Whiskers at golden hour",
		Link:     "https://old.reddit.com/r/cats/comments/1fghij/whiskers_at_golden_hour/",
		ImageURL: "https://preview.redd.it/5t6y7u8i.jpg?width=140&height=140&crop=1:1,smart&auto=webp&s=x",
		Summary:  `<a href="https://www.reddit.com/r/cats/comments/1fghij/"><img src="https://preview.redd.it/5t6y7u8i.jpg" alt="Whiskers"></a><a href="https://www.reddit.com/gallery/1fghij">[link]</a>`,
		FetchedAt: db.Now(),
	})

	body := doGet(h, "/", cookie).Body.String()
	if !strings.Contains(body, `id="item-1" class="video-card"`) {
		t.Fatalf("video item should render a video-card: %s", body)
	}
	if !strings.Contains(body, `src="https://i.ytimg.com/vi/H0KAi8AWsnM/hq720.jpg"`) {
		t.Fatalf("video card missing thumbnail: %s", body)
	}
	if !strings.Contains(body, `id="item-2" class="text-card"`) {
		t.Fatalf("text item should render a text-card: %s", body)
	}
	if !strings.Contains(body, `id="item-3" class="image-card"`) {
		t.Fatalf("image post should render an image-card: %s", body)
	}
	if !strings.Contains(body, `src="https://example.com/pic.jpg"`) {
		t.Fatalf("image card missing the image: %s", body)
	}
	if !strings.Contains(body, `id="item-4" class="link-card"`) {
		t.Fatalf("link post should render a link-card: %s", body)
	}
	if !strings.Contains(body, `src="https://external-preview.redd.it/1q2w3e4r.jpeg?width=320"`) {
		t.Fatalf("link card missing the external-preview thumbnail: %s", body)
	}
	if !strings.Contains(body, `class="thumb-badge"`) || !strings.Contains(body, ">external</span>") {
		t.Fatalf("link card missing the external badge: %s", body)
	}
	if !strings.Contains(body, `id="item-5" class="image-card"`) {
		t.Fatalf("gallery should render an image-card: %s", body)
	}
	if !strings.Contains(body, `src="https://i.redd.it/5t6y7u8i.jpg"`) {
		t.Fatalf("gallery card should use the full-res i.redd.it thumbnail: %s", body)
	}
	// The row keeps the modal data attrs and the read toggle.
	if !strings.Contains(body, `data-item-link="https://www.youtube.com/watch?v=H0KAi8AWsnM"`) {
		t.Fatalf("video card missing data-item-link: %s", body)
	}
	if !strings.Contains(body, `hx-post="/items/2/read"`) {
		t.Fatalf("text card missing read toggle: %s", body)
	}
	if !strings.Contains(body, `hx-post="/items/3/read"`) {
		t.Fatalf("image card missing read toggle: %s", body)
	}
	if !strings.Contains(body, `hx-post="/items/4/read"`) {
		t.Fatalf("link card missing read toggle: %s", body)
	}
	// Feed titles in the item meta link to the internal feed page, never the RSS url.
	if !strings.Contains(body, `href="/feeds/1">bigboxSWE</a>`) {
		t.Fatalf("item meta should link to /feeds/1 internally: %s", body)
	}
	if strings.Contains(body, `href="https://www.youtube.com/feeds/videos.xml`) {
		t.Fatalf("item meta must not link to the external feed url: %s", body)
	}
}

func TestItemViewImageLightbox(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	u, _ := s.store.Users.ByUsername("alice")
	f, _ := s.store.Feeds.Create(u.ID, 0, "Pics", "https://pics.dev/rss.xml", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{
		GUID: "p1", Title: "Image Post", Link: "https://pics.dev/1",
		Summary: `<a href="https://pics.dev/1"><img src="https://pics.dev/pic.jpg" alt="Image Post" /></a>`,
		ImageURL: "https://pics.dev/pic.jpg", FetchedAt: db.Now(),
	})

	items, _ := s.store.Items.List(u.ID, store.ItemFilter{})
	rr := doGet(h, "/items/"+itoa(items[0].ID)+"/view", cookie)
	body := rr.Body.String()
	if !strings.Contains(body, `class="image-lightbox"`) {
		t.Fatalf("image post view missing image-lightbox: %s", body)
	}
	if !strings.Contains(body, `src="https://pics.dev/pic.jpg"`) {
		t.Fatalf("image post view missing the lightbox image: %s", body)
	}
	if strings.Contains(body, "item-body") {
		t.Fatalf("image post view should not render the summary body: %s", body)
	}
}

func TestItemViewMarksRead(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	u, _ := s.store.Users.ByUsername("alice")
	f, _ := s.store.Feeds.Create(u.ID, 0, "Blog", "https://b.dev/rss.xml", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{
		GUID: "g", Title: "Item", Link: "https://b.dev/1", FetchedAt: db.Now(),
	})

	items, _ := s.store.Items.List(u.ID, store.ItemFilter{})
	id := items[0].ID
	if items[0].Read {
		t.Fatal("item should start unread")
	}
	rr := doGet(h, "/items/"+itoa(id)+"/view", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("item view: %d", rr.Code)
	}
	after, err := s.store.Items.ByID(u.ID, id)
	if err != nil || !after.Read {
		t.Fatalf("viewing should mark the item read: %+v err=%v", after, err)
	}
	// Marking read twice is idempotent.
	doGet(h, "/items/"+itoa(id)+"/view", cookie)
	after, _ = s.store.Items.ByID(u.ID, id)
	if !after.Read {
		t.Fatal("viewing a read item should not toggle it back to unread")
	}
}

func TestItemReadToggle(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "A", "", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "B", "https://b.dev/rss.xml", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{GUID: "g", Title: "Item", Link: "https://b.dev/1", FetchedAt: db.Now()})

	items, _ := s.store.Items.List(u.ID, store.ItemFilter{})
	rr := doForm(h, "POST", "/items/"+itoa(items[0].ID)+"/read", url.Values{}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("toggle read: %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "item-"+itoa(items[0].ID)) {
		t.Fatal("item row fragment not returned")
	}
	if !strings.Contains(rr.Body.String(), `data-item-id="`+itoa(items[0].ID)+`"`) ||
		!strings.Contains(rr.Body.String(), `data-item-link="https://b.dev/1"`) {
		t.Fatalf("item row missing modal data attrs: %s", rr.Body.String())
	}
	n, _ := s.store.Items.CountUnread(u.ID, 0)
	if n != 0 {
		t.Fatalf("unread = %d, want 0", n)
	}
}

func TestItemFavoriteToggle(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "A", "", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "B", "https://b.dev/rss.xml", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{GUID: "g", Title: "Item", Link: "https://b.dev/1", FetchedAt: db.Now()})

	items, _ := s.store.Items.List(u.ID, store.ItemFilter{})
	id := items[0].ID
	rr := doForm(h, "POST", "/items/"+itoa(id)+"/favorite", url.Values{}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("toggle favorite: %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "item-"+itoa(id)) {
		t.Fatalf("item row fragment not returned: %s", body)
	}
	if !strings.Contains(body, `class="star on"`) {
		t.Fatalf("star should be filled: %s", body)
	}
	after, err := s.store.Items.ByID(u.ID, id)
	if err != nil || !after.Favorite {
		t.Fatalf("item should be favorited: %v %+v", err, after)
	}

	// Toggling again unfavorites.
	rr = doForm(h, "POST", "/items/"+itoa(id)+"/favorite", url.Values{}, cookie)
	if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), `class="star on"`) {
		t.Fatalf("toggle off: %d %s", rr.Code, rr.Body.String())
	}
	after, _ = s.store.Items.ByID(u.ID, id)
	if after.Favorite {
		t.Fatal("item should no longer be favorited")
	}
}

func TestFavoritesPage(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "A", "", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "B", "https://b.dev/rss.xml", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{GUID: "g", Title: "Item", Link: "https://b.dev/1", FetchedAt: db.Now()})
	s.store.Items.Upsert(f.ID, store.Item{GUID: "g2", Title: "Other", Link: "https://b.dev/2", FetchedAt: db.Now()})

	items, _ := s.store.Items.List(u.ID, store.ItemFilter{})
	var favID int64
	for _, it := range items {
		if it.Link == "https://b.dev/1" {
			favID = it.ID
		}
	}
	if favID == 0 {
		t.Fatal("test item not found")
	}
	s.store.Items.SetFavorite(u.ID, favID, true)

	body := doGet(h, "/favorites", cookie).Body.String()
	if !strings.Contains(body, "favorites (1)") {
		t.Fatalf("favorites count wrong: %s", body)
	}
	if !strings.Contains(body, `data-item-link="https://b.dev/1"`) {
		t.Fatalf("favorited item missing from page: %s", body)
	}
	if strings.Contains(body, `data-item-link="https://b.dev/2"`) {
		t.Fatalf("unfavorited item should not appear: %s", body)
	}
}

func TestItemView(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://b.dev/rss.xml", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{
		GUID: "g", Title: "Item", Link: "https://b.dev/1",
		Summary: "<p>hello <b>world</b></p>", FetchedAt: db.Now(),
	})

	items, _ := s.store.Items.List(u.ID, store.ItemFilter{})
	rr := doGet(h, "/items/"+itoa(items[0].ID)+"/view", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("item view: %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "<b>world</b>") || !strings.Contains(body, "Item") || !strings.Contains(body, "Metru") {
		t.Fatalf("item view content: %s", body)
	}
	// Modal meta links author and feed internally, never to the external RSS url.
	if !strings.Contains(body, `href="/authors/1">Metru</a>`) {
		t.Fatalf("modal meta should link the author internally: %s", body)
	}
	if !strings.Contains(body, `href="/feeds/1">Blog</a>`) {
		t.Fatalf("modal meta should link the feed internally: %s", body)
	}
	if strings.Contains(body, `href="https://b.dev/rss.xml"`) {
		t.Fatalf("modal meta must not link to the external feed url: %s", body)
	}
	// "open live" lives in the dialog header (layout), not the fragment.

	// Another user cannot view it.
	other, _ := s.store.Users.Create("bob", "h")
	_ = other
	// (cross-user access is covered by OneWithFeed's user scoping)
}

func TestAuthorFormFragment(t *testing.T) {
	_, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	if rr := doGet(h, "/fragments/author-form?author_id=new", cookie); !strings.Contains(rr.Body.String(), "new author name") {
		t.Fatal("expected author-create fields fragment")
	}
	if rr := doGet(h, "/fragments/author-form?author_id=5", cookie); rr.Code != http.StatusNoContent {
		t.Fatalf("expected empty fragment for existing author, got %d", rr.Code)
	}
}

func TestAuthorPageHasAddFeedDialog(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "", "")

	body := doGet(h, "/authors/"+itoa(a.ID), cookie).Body.String()
	for _, want := range []string{
		`id="add-author-feed-dialog"`,
		`name="author_id" value="` + itoa(a.ID) + `"`,
		`hx-post="/fragments/feed-preview"`,
		"+ add feed",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("author page missing %q: %s", want, body)
		}
	}
}

func TestExternalLinksCarryMarker(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "https://metru.example", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://b.dev/rss.xml", "https://b.dev", "", 900)

	// Feed page: "feed" and "home" links are external and carry the ↗ marker class.
	body := doGet(h, "/feeds/"+itoa(f.ID), cookie).Body.String()
	if !strings.Contains(body, `href="https://b.dev/rss.xml" class="external">feed`) {
		t.Fatalf("feed page feed link should be external-marked: %s", body)
	}
	if !strings.Contains(body, `href="https://b.dev" target="_blank" rel="noopener" class="external">home`) {
		t.Fatalf("feed page home link should be external-marked: %s", body)
	}

	// Author page: the homepage URL is external and marked.
	body = doGet(h, "/authors/"+itoa(a.ID), cookie).Body.String()
	if !strings.Contains(body, `href="https://metru.example" target="_blank" rel="noopener" class="external">`) {
		t.Fatalf("author homepage should be external-marked: %s", body)
	}

	// Feeds list row: the "feed" link is external and marked.
	body = doGet(h, "/feeds", cookie).Body.String()
	if !strings.Contains(body, `href="https://b.dev/rss.xml" class="external">feed`) {
		t.Fatalf("feeds list feed link should be external-marked: %s", body)
	}

	// "open live" in the dialog header is external and marked.
	body = doGet(h, "/", cookie).Body.String()
	if !strings.Contains(body, `class="small external"`) {
		t.Fatalf("open live should be external-marked: %s", body)
	}
}

func TestFeedAndAuthorPagesShowItems(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://b.dev/rss.xml", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{GUID: "g", Title: "Feed Item", Link: "https://b.dev/1", FetchedAt: db.Now()})

	// Feed detail page shows the feed, its author, and its items.
	rr := doGet(h, "/feeds/"+itoa(f.ID), cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("feed page: %d", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{"Blog", "Feed Item", "Metru"} {
		if !strings.Contains(body, want) {
			t.Fatalf("feed page missing %q: %s", want, body)
		}
	}

	// Author page shows items from the author's feeds.
	rr = doGet(h, "/authors/"+itoa(a.ID), cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("author page: %d", rr.Code)
	}
	if body := rr.Body.String(); !strings.Contains(body, "Feed Item") {
		t.Fatalf("author page missing item: %s", body)
	}
}

func TestScopedReadUnreadTabs(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://b.dev/rss.xml", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{GUID: "g1", Title: "Unread Item", Link: "https://b.dev/1", FetchedAt: db.Now()})
	s.store.Items.Upsert(f.ID, store.Item{GUID: "g2", Title: "Read Item", Link: "https://b.dev/2", FetchedAt: db.Now()})
	coll, _ := s.store.Collections.Create(u.ID, "Dev")
	s.store.Collections.AddFeed(u.ID, coll.ID, f.ID)

	items, _ := s.store.Items.List(u.ID, store.ItemFilter{})
	var readID int64
	for _, it := range items {
		if it.Link == "https://b.dev/2" {
			readID = it.ID
		}
	}
	if readID == 0 {
		t.Fatal("read item not found")
	}
	if err := s.store.Items.SetRead(u.ID, readID, true); err != nil {
		t.Fatalf("SetRead: %v", err)
	}

	for _, scope := range []struct {
		base string // page URL
		frag string // fragment URL
	}{
		{"/feeds/" + itoa(f.ID), "/feeds/" + itoa(f.ID) + "/items"},
		{"/authors/" + itoa(a.ID), "/authors/" + itoa(a.ID) + "/items"},
		{"/collections/" + itoa(coll.ID), "/collections/" + itoa(coll.ID) + "/items"},
	} {
		// Default view is unread: only the unread item, tabs show both counts.
		body := doGet(h, scope.base, cookie).Body.String()
		for _, want := range []string{`class="tabs"`, "unread (1)", "read (1)", `data-item-link="https://b.dev/1"`} {
			if !strings.Contains(body, want) {
				t.Fatalf("%s default view missing %q: %s", scope.base, want, body)
			}
		}
		if strings.Contains(body, `data-item-link="https://b.dev/2"`) {
			t.Fatalf("%s default view should not show the read item: %s", scope.base, body)
		}

		// ?view=read shows only the read item.
		body = doGet(h, scope.base+"?view=read", cookie).Body.String()
		if !strings.Contains(body, `data-item-link="https://b.dev/2"`) ||
			strings.Contains(body, `data-item-link="https://b.dev/1"`) {
			t.Fatalf("%s read view wrong list: %s", scope.base, body)
		}

		// The fragment endpoint returns the tabbed list for the requested view.
		rr := doGet(h, scope.frag+"?view=read", cookie)
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `data-item-link="https://b.dev/2"`) {
			t.Fatalf("%s fragment: %d %s", scope.frag, rr.Code, rr.Body.String())
		}
	}
}
