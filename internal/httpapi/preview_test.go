package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func feedPreviewServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rss":
			w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>RSS Feed</title><link>https://home.dev/</link><item><guid>1</guid><title>i</title><link>https://home.dev/1</link></item></channel></rss>`))
		case "/atom":
			w.Write([]byte(`<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"><title>Atom Feed</title><id>urn:a</id><updated>2026-01-01T00:00:00Z</updated></feed>`))
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestFeedPreviewErrors(t *testing.T) {
	_, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	// Empty url.
	rr := doForm(h, "POST", "/fragments/feed-preview", url.Values{"url": {""}}, cookie)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "enter a url") {
		t.Fatalf("empty url: %d %s", rr.Code, rr.Body.String())
	}

	// Unparseable url -> a visible error, not a crash.
	rr = doForm(h, "POST", "/fragments/feed-preview", url.Values{"url": {"://not-a-url"}}, cookie)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), `role="alert"`) {
		t.Fatalf("unparseable url: %d %s", rr.Code, rr.Body.String())
	}
}

func TestFeedPreviewDirect(t *testing.T) {
	_, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	feedSrv := feedPreviewServer(t)
	defer feedSrv.Close()

	rr := doForm(h, "POST", "/fragments/feed-preview", url.Values{
		"url": {feedSrv.URL + "/rss"},
	}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "RSS Feed") || !strings.Contains(body, feedSrv.URL+"/rss") {
		t.Fatalf("direct preview missing values: %s", body)
	}
	if !strings.Contains(body, "https://home.dev/") {
		t.Fatalf("direct preview should derive home url from feed link: %s", body)
	}
}

func TestFeedPreviewSingleAndNone(t *testing.T) {
	_, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	feedSrv := feedPreviewServer(t)
	defer feedSrv.Close()

	// A page whose HTML references one feed.
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(fmt.Sprintf(`<html><head><title>Home Page</title>
		  <link rel="alternate" type="application/rss+xml" href="%s/rss"></head></html>`, feedSrv.URL)))
	}))
	defer page.Close()

	rr := doForm(h, "POST", "/fragments/feed-preview", url.Values{"url": {page.URL}}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("preview: %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, feedSrv.URL+"/rss") || !strings.Contains(body, page.URL) {
		t.Fatalf("page preview: %s", body)
	}

	// No feed anywhere: the preview offers the scrape builder.
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html><head><title>Nothing</title></head></html>"))
	}))
	defer empty.Close()
	rr = doForm(h, "POST", "/fragments/feed-preview", url.Values{"url": {empty.URL}}, cookie)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "no feed found") {
		t.Fatalf("no-feed preview: %d %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `role="alert"`) {
		t.Fatalf("no-feed preview should render the error fragment: %s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `hx-post="/fragments/scrape-builder"`) {
		t.Fatalf("no-feed preview should offer the scrape builder: %s", rr.Body.String())
	}
}

func TestFeedPreviewMultiple(t *testing.T) {
	_, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	feedSrv := feedPreviewServer(t)
	defer feedSrv.Close()

	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(fmt.Sprintf(`<html><head><title>Both</title>
		  <link rel="alternate" type="application/rss+xml" href="%s/rss">
		  <link rel="alternate" type="application/atom+xml" href="%s/atom"></head></html>`, feedSrv.URL, feedSrv.URL)))
	}))
	defer page.Close()

	// Step 1: dropdown with both candidates.
	rr := doForm(h, "POST", "/fragments/feed-preview", url.Values{"url": {page.URL}}, cookie)
	if !strings.Contains(rr.Body.String(), "pick one") || !strings.Contains(rr.Body.String(), feedSrv.URL+"/atom") {
		t.Fatalf("choose fragment: %s", rr.Body.String())
	}

	// Step 2: chosen candidate -> single form.
	rr = doForm(h, "POST", "/fragments/feed-preview", url.Values{
		"url": {page.URL}, "feed_url": {feedSrv.URL + "/atom"},
	}, cookie)
	body := rr.Body.String()
	if !strings.Contains(body, feedSrv.URL+"/atom") || strings.Contains(body, "/rss") {
		t.Fatalf("chosen preview: %s", body)
	}
}

func TestFeedPreviewPreselectsAuthor(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	feedSrv := feedPreviewServer(t)
	defer feedSrv.Close()

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "", "")

	rr := doForm(h, "POST", "/fragments/feed-preview", url.Values{
		"url":       {feedSrv.URL + "/rss"},
		"author_id": {itoa(a.ID)},
	}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `value="`+itoa(a.ID)+`" selected`) {
		t.Fatalf("expected author preselected in the form: %s", body)
	}
	// Without author_id nothing is preselected.
	rr = doForm(h, "POST", "/fragments/feed-preview", url.Values{
		"url": {feedSrv.URL + "/rss"},
	}, cookie)
	if strings.Contains(rr.Body.String(), " selected") {
		t.Fatalf("no author should be preselected by default: %s", rr.Body.String())
	}
}

// TestFeedPreviewPrefillsNewAuthor asserts the combined add form derives the
// new-author name and avatar from the discovered page (the author-centric add
// flow). The old standalone author-preview endpoint was folded into this.
func TestFeedPreviewPrefillsNewAuthor(t *testing.T) {
	_, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rss" {
			w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>RSS Feed</title><link>https://home.dev/</link></channel></rss>`))
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><head><title>Metru Site</title>
		  <link rel="alternate" type="application/rss+xml" href="/rss">
		  <link rel="icon" href="/favicon.png"></head><body>hi</body></html>`))
	}))
	defer page.Close()

	rr := doForm(h, "POST", "/fragments/feed-preview", url.Values{"url": {page.URL}}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("feed preview: %d %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `value="Metru Site"`) {
		t.Fatalf("new-author name should be prefilled from the page title: %s", body)
	}
	if !strings.Contains(body, page.URL+"/favicon.png") {
		t.Fatalf("new-author avatar should be prefilled from the site icon: %s", body)
	}
	// The combined form must not offer an authorless option.
	if strings.Contains(body, "no author") {
		t.Fatalf("combined add form must not offer an authorless option: %s", body)
	}
}

func TestAuthorFormFragmentPrefill(t *testing.T) {
	_, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><head><title>Detected Author</title></head></html>`))
	}))
	defer page.Close()

	rr := doGet(h, "/fragments/author-form?author_id=new&home_url="+url.QueryEscape(page.URL), cookie)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `value="Detected Author"`) {
		t.Fatalf("prefilled author fragment: %d %s", rr.Code, rr.Body.String())
	}
}
