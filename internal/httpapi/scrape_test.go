package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/metruzanca/nanoflux/internal/store"
)

const scrapeTestPage = `<!DOCTYPE html>
<html><head><title>Scrape Blog</title></head><body>
<article>
  <h2><a href="/posts/one">One</a></h2>
  <p class="excerpt">First excerpt.</p>
  <time datetime="2026-02-01T09:00:00Z">Feb 1, 2026</time>
</article>
<article>
  <h2><a href="/posts/two">Two</a></h2>
  <p class="excerpt">Second excerpt.</p>
  <time datetime="2026-01-31T09:00:00Z">Jan 31, 2026</time>
</article>
</body></html>`

// scrapeTestServer returns a Server whose http client trusts a test TLS page
// server serving scrapeTestPage.
func scrapeTestServer(t *testing.T) (*Server, http.Handler, *http.Cookie, *httptest.Server) {
	t.Helper()
	s, h := newTestServer(t)
	page := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(scrapeTestPage))
	}))
	t.Cleanup(page.Close)
	s.client = page.Client() // trust the test TLS cert
	return s, h, sessionCookie(t, h), page
}

func TestNoFeedFoundRendersScrapeOption(t *testing.T) {
	s, h, cookie, page := scrapeTestServer(t)

	// A page with no feed at all: the preview must offer the scrape builder.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><head><title>No Feed</title></head><body><p>hi</p></body></html>`))
	}))
	defer srv.Close()
	s.client = srv.Client()

	rr := doForm(h, "POST", "/fragments/feed-preview", url.Values{"url": {srv.URL}}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "no feed found at that url") || !strings.Contains(body, "role=\"alert\"") {
		t.Fatalf("should render the no-feed banner: %s", body)
	}
	if !strings.Contains(body, `hx-post="/fragments/scrape-builder"`) {
		t.Fatalf("should offer the scrape builder: %s", body)
	}
	_ = page
}

func TestScrapeBuilderShowsSample(t *testing.T) {
	_, h, cookie, page := scrapeTestServer(t)

	rr := doForm(h, "POST", "/fragments/scrape-builder", url.Values{"url": {page.URL}}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("scrape-builder: %d %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `name="scrape_item"`) || !strings.Contains(body, `name="scrape_title"`) {
		t.Fatalf("builder should render selector fields: %s", body)
	}
	if !strings.Contains(body, ">One</a>") {
		t.Fatalf("builder should auto-detect and preview items: %s", body)
	}
}

func TestScrapeBuilderRePostUsesFeedURL(t *testing.T) {
	_, h, cookie, page := scrapeTestServer(t)

	// Auto-detect re-posts the form, which carries the URL as feed_url, not url.
	rr := doForm(h, "POST", "/fragments/scrape-builder", url.Values{
		"feed_url": {page.URL}, "detect": {"1"}, "title": {"Kept Title"},
	}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("scrape-builder re-post: %d %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `value="Kept Title"`) {
		t.Fatalf("re-post should preserve the typed title: %s", body)
	}
	if !strings.Contains(body, ">One</a>") {
		t.Fatalf("re-post should re-render the sample: %s", body)
	}
}

func TestScrapePreviewLiveSample(t *testing.T) {
	_, h, cookie, page := scrapeTestServer(t)

	rr := doForm(h, "POST", "/fragments/scrape-preview", url.Values{
		"url": {page.URL}, "scrape_item": {"article"}, "scrape_link": {"h2 a"},
	}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("scrape-preview: %d %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, ">One</a>") || !strings.Contains(body, ">Two</a>") {
		t.Fatalf("preview should show extracted items: %s", body)
	}
}

func TestScrapePreviewError(t *testing.T) {
	_, h, cookie, page := scrapeTestServer(t)

	// A selector that matches nothing must render the standard error banner.
	rr := doForm(h, "POST", "/fragments/scrape-preview", url.Values{
		"url": {page.URL}, "scrape_item": {".nothing-here"},
	}, cookie)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("scrape-preview error: got %d, want 400", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "role=\"alert\"") {
		t.Fatalf("error should be a role=alert banner: %s", rr.Body.String())
	}
}

func TestScrapeFeedCreatePersistsConfig(t *testing.T) {
	s, h, cookie, page := scrapeTestServer(t)

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Scraper", "", "")

	rr := doForm(h, "POST", "/feeds", url.Values{
		"kind": {"scrape"}, "title": {"Scraped"}, "feed_url": {page.URL},
		"home_url": {page.URL}, "author_id": {itoa(a.ID)},
		"scrape_item": {"article"}, "scrape_title": {"h2 a"},
	}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("create scrape feed: %d %s", rr.Code, rr.Body.String())
	}

	feeds, _ := s.store.Feeds.List(u.ID)
	if len(feeds) != 1 {
		t.Fatalf("feeds = %d, want 1", len(feeds))
	}
	f := feeds[0]
	if f.Kind != store.ScrapeKind {
		t.Fatalf("kind = %q, want scrape", f.Kind)
	}
	if !strings.Contains(f.ScrapeConfig, `"item":"article"`) {
		t.Fatalf("scrape config = %q, want item selector", f.ScrapeConfig)
	}
}

func TestScrapeFeedCreateRequiresItemSelector(t *testing.T) {
	s, h, cookie, page := scrapeTestServer(t)

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Scraper", "", "")

	rr := doForm(h, "POST", "/feeds", url.Values{
		"kind": {"scrape"}, "title": {"Scraped"}, "feed_url": {page.URL},
		"author_id": {itoa(a.ID)},
	}, cookie)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("create scrape feed without item: got %d, want 400", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "item selector is required") || !strings.Contains(rr.Body.String(), "role=\"alert\"") {
		t.Fatalf("should explain the missing selector: %s", rr.Body.String())
	}
}

func TestScrapeFeedEditShowsSelectors(t *testing.T) {
	s, h, cookie, page := scrapeTestServer(t)

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Scraper", "", "")
	f, _ := s.store.Feeds.CreateScrape(u.ID, a.ID, "Scraped", page.URL, page.URL, "", `{"item":"article","link":"h2 a"}`, 900)

	body := doGet(h, "/feeds/"+itoa(f.ID)+"/edit", cookie).Body.String()
	if !strings.Contains(body, `name="scrape_item"`) || !strings.Contains(body, `value="article"`) {
		t.Fatalf("edit page should render scrape selectors: %s", body)
	}
	if !strings.Contains(body, `value="h2 a"`) {
		t.Fatalf("edit page should prefill the link selector: %s", body)
	}
}

func TestScrapeFeedUpdatePersistsConfig(t *testing.T) {
	s, h, cookie, page := scrapeTestServer(t)

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Scraper", "", "")
	f, _ := s.store.Feeds.CreateScrape(u.ID, a.ID, "Scraped", page.URL, page.URL, "", `{"item":"article"}`, 900)

	rr := doForm(h, "POST", "/feeds/"+itoa(f.ID)+"/edit", url.Values{
		"title": {"Scraped"}, "feed_url": {page.URL}, "home_url": {page.URL},
		"author_id": {itoa(a.ID)}, "scrape_item": {".post"}, "poll_interval_sec": {"900"},
		"enabled": {"1"},
	}, cookie)
	if rr.Code != http.StatusFound {
		t.Fatalf("update scrape feed: %d %s", rr.Code, rr.Body.String())
	}
	got, _ := s.store.Feeds.ByID(u.ID, f.ID)
	if !strings.Contains(got.ScrapeConfig, `"item":".post"`) {
		t.Fatalf("updated scrape config = %q", got.ScrapeConfig)
	}
	if got.Kind != store.ScrapeKind {
		t.Fatalf("kind changed to %q", got.Kind)
	}
}
