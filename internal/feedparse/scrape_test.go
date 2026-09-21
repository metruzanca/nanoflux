package feedparse

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// blogPage is a small HTML listing page with three posts in <article> blocks.
const blogPage = `<!DOCTYPE html>
<html><head><title>Example Blog</title></head><body>
<article>
  <h2><a href="/posts/first">First post</a></h2>
  <p class="excerpt">The first excerpt.</p>
  <time datetime="2026-01-05T10:00:00Z">Jan 5, 2026</time>
  <img src="/img/first.jpg"/>
</article>
<article>
  <h2><a href="https://other.dev/second">Second post</a></h2>
  <p class="excerpt">Second excerpt.</p>
  <time datetime="2026-01-04">Jan 4, 2026</time>
</article>
<article>
  <h2><a href="/posts/third">Third post</a></h2>
  <p class="excerpt">Third excerpt.</p>
  <time>Jan 3, 2026</time>
</article>
</body></html>`

func scrapeServer(body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(body))
	}))
}

func TestScrapeExtractsItems(t *testing.T) {
	srv := scrapeServer(blogPage)
	defer srv.Close()

	cfg := ScrapeConfig{Item: "article", Title: "h2 a", Link: "h2 a"}
	res, err := Scrape(context.Background(), srv.URL, srv.Client(), cfg, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Feed.Title != "Example Blog" {
		t.Errorf("feed title = %q, want Example Blog", res.Feed.Title)
	}
	if len(res.Items) != 3 {
		t.Fatalf("items = %d, want 3", len(res.Items))
	}

	// Relative link resolved against the base, tracking stripped.
	first := res.Items[0]
	if first.Link != srv.URL+"/posts/first" {
		t.Errorf("first link = %q, want %q", first.Link, srv.URL+"/posts/first")
	}
	if first.Title != "First post" {
		t.Errorf("first title = %q", first.Title)
	}
	// Absolute external link stays absolute.
	if res.Items[1].Link != "https://other.dev/second" {
		t.Errorf("second link = %q", res.Items[1].Link)
	}
	// Date from the datetime attribute, formatted as UTC.
	if res.Items[0].PublishedAt != "2026-01-05 10:00:00" {
		t.Errorf("first date = %q, want 2026-01-05 10:00:00", res.Items[0].PublishedAt)
	}
	// Date-only datetime parses; relative text date parses via a layout.
	if res.Items[1].PublishedAt != "2026-01-04 00:00:00" {
		t.Errorf("second date = %q", res.Items[1].PublishedAt)
	}
	// GUIDs are stable prefixes derived from the link.
	if !strings.HasPrefix(first.GUID, "scrape:") {
		t.Errorf("guid %q should have scrape: prefix", first.GUID)
	}
	if first.GUID == res.Items[1].GUID {
		t.Error("guids must differ across items")
	}
}

func TestScrapeFallbackTitleFromLinkText(t *testing.T) {
	srv := scrapeServer(blogPage)
	defer srv.Close()

	// No title selector: title falls back to the first in-item link's text.
	res, err := Scrape(context.Background(), srv.URL, srv.Client(), ScrapeConfig{Item: "article"}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 3 {
		t.Fatalf("items = %d, want 3", len(res.Items))
	}
	if res.Items[0].Title != "First post" {
		t.Errorf("fallback title = %q, want First post", res.Items[0].Title)
	}
}

func TestScrapeRejectsNonHTML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("just text"))
	}))
	defer srv.Close()

	_, err := Scrape(context.Background(), srv.URL, srv.Client(), ScrapeConfig{Item: "article"}, "", "")
	if err == nil {
		t.Fatal("scrape should fail on a non-HTML body")
	}
}

func TestScrapeNoItemMatch(t *testing.T) {
	srv := scrapeServer(blogPage)
	defer srv.Close()

	_, err := Scrape(context.Background(), srv.URL, srv.Client(), ScrapeConfig{Item: ".nothing-here"}, "", "")
	if err == nil || !strings.Contains(err.Error(), "no items match") {
		t.Fatalf("err = %v, want a no-items-match error", err)
	}
}

func TestScrapeHonorsConditionalGET(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(blogPage))
	}))
	defer srv.Close()

	if _, err := Scrape(context.Background(), srv.URL, srv.Client(), ScrapeConfig{Item: "article"}, `"v1"`, ""); err != ErrNotModified {
		t.Fatalf("conditional scrape err = %v, want ErrNotModified", err)
	}
}

func TestAutoDetectFindsArticle(t *testing.T) {
	srv := scrapeServer(blogPage)
	defer srv.Close()

	cfg, err := AutoDetect(context.Background(), srv.URL, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Item != "article" {
		t.Errorf("detected item selector = %q, want article", cfg.Item)
	}
}

func TestAutoDetectRejectsListlessPage(t *testing.T) {
	srv := scrapeServer(`<html><head><title>One</title></head><body><p>hello</p></body></html>`)
	defer srv.Close()

	if _, err := AutoDetect(context.Background(), srv.URL, srv.Client()); err == nil {
		t.Fatal("auto-detect should fail on a page with no list")
	}
}

func TestParseScrapeConfig(t *testing.T) {
	cfg, err := ParseScrapeConfig(`{"item":"article","title":"h2 a","link":"h2 a"}`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Item != "article" || cfg.Title != "h2 a" {
		t.Errorf("parsed config = %+v", cfg)
	}
	if _, err := ParseScrapeConfig(`{}`); err == nil {
		t.Fatal("config without an item selector must be rejected")
	}
	if _, err := ParseScrapeConfig("not json"); err == nil {
		t.Fatal("bad json must be rejected")
	}
}
