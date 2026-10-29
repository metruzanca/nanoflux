package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/metruzanca/nanoflux/internal/discover"
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

	// No feed anywhere: the preview offers the manual-entry escape hatch.
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
	if !strings.Contains(rr.Body.String(), `hx-post="/fragments/manual-feed"`) {
		t.Fatalf("no-feed preview should offer manual entry: %s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `hx-target="#author-preview"`) {
		t.Fatalf("global no-feed preview must target the add dialog container: %s", rr.Body.String())
	}
}

// TestFeedPreviewTargets covers the three preview containers. Each entry point
// swaps the chooser and no-feed fragments into a different container, and a
// wrong selector makes htmx abort with a targetError.
func TestFeedPreviewTargets(t *testing.T) {
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

	// Global add dialog: the chooser targets its own container, not the old
	// #feed-preview id that no page defines.
	rr := doForm(h, "POST", "/fragments/feed-preview", url.Values{"url": {page.URL}}, cookie)
	body := rr.Body.String()
	if !strings.Contains(body, `hx-target="#author-preview"`) || strings.Contains(body, `hx-target="#feed-preview"`) {
		t.Fatalf("global chooser target: %s", body)
	}

	// Share page: chooser targets #share-preview.
	rr = doForm(h, "POST", "/fragments/feed-preview", url.Values{
		"url": {page.URL}, "preview_target": {previewTargetShare},
	}, cookie)
	body = rr.Body.String()
	if !strings.Contains(body, `hx-target="#share-preview"`) {
		t.Fatalf("share chooser should target #share-preview: %s", body)
	}
	// The chosen feed's form must submit somewhere that exists on the share
	// page; the redirect header wins after a successful save.
	rr = doForm(h, "POST", "/fragments/feed-preview", url.Values{
		"url": {page.URL}, "feed_url": {feedSrv.URL + "/atom"}, "preview_target": {previewTargetShare},
	}, cookie)
	body = rr.Body.String()
	if !strings.Contains(body, `hx-target="#share-add-result"`) {
		t.Fatalf("share add form should target #share-add-result: %s", body)
	}

	// Share page no-feed banner: its manual button targets #share-preview.
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html><head><title>Nothing</title></head></html>"))
	}))
	defer empty.Close()
	rr = doForm(h, "POST", "/fragments/feed-preview", url.Values{
		"url": {empty.URL}, "preview_target": {previewTargetShare},
	}, cookie)
	if body := rr.Body.String(); !strings.Contains(body, `hx-target="#share-preview"`) {
		t.Fatalf("share no-feed button should target #share-preview: %s", body)
	}
}

// countingTransport counts requests and fails them, so a test can assert that a
// code path makes no network request at all.
type countingTransport struct{ hits int }

func (c *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	c.hits++
	return nil, fmt.Errorf("unexpected request")
}

// TestFeedPreviewDerivedFeed asserts a derived add path is request-free: the
// feed comes from the URL and no page or feed is fetched, leaving the host's
// tight anonymous rate limit for the immediate poll. The fixture uses reddit
// URLs as an example.
func TestFeedPreviewDerivedFeed(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	useSitePlugin(t, s)

	ct := &countingTransport{}
	client := &http.Client{Transport: ct}
	s.client = client
	s.discoverer = discover.New(client)

	rr := doForm(h, "POST", "/fragments/feed-preview", url.Values{
		"url": {"https://old.reddit.com/u/spez/"},
	}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `value="https://www.reddit.com/user/spez/submitted.rss"`) {
		t.Fatalf("preview should prefill the derived .rss feed: %s", body)
	}
	if !strings.Contains(body, `value="https://www.reddit.com/user/spez"`) {
		t.Fatalf("preview should prefill the canonical home url: %s", body)
	}
	if !strings.Contains(body, `value="spez"`) {
		t.Fatalf("new author should be prefilled as spez (no u/ prefix): %s", body)
	}
	if ct.hits != 0 {
		t.Fatalf("reddit preview made %d request(s); it must make none", ct.hits)
	}
}

// TestFeedPreviewRateLimitReason asserts that when discovery fails with an HTTP
// status the user sees a specific reason (a 429 is a rate limit, not a missing
// feed), and that no raw URL leaks.
func TestFeedPreviewRateLimitReason(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	// Every request (direct fetch, page scan, probes) answers 429.
	limited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer limited.Close()
	s.client = limited.Client()

	rr := doForm(h, "POST", "/fragments/feed-preview", url.Values{"url": {limited.URL}}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "no feed found at that url") || !strings.Contains(body, `role="alert"`) {
		t.Fatalf("should still render the no-feed banner: %s", body)
	}
	if !strings.Contains(body, "rate-limiting") {
		t.Fatalf("should explain the rate limit: %s", body)
	}
	// The raw fetch url must not be echoed back.
	if strings.Contains(body, limited.URL) {
		t.Fatalf("the underlying url must not leak: %s", body)
	}
	// The manual-entry escape hatch remains available.
	if !strings.Contains(body, `hx-post="/fragments/manual-feed"`) {
		t.Fatalf("manual entry should remain available: %s", body)
	}
}

// TestManualFeedForm asserts the "insert manually" fragment renders the add
// form pre-filled with the entered url, submitting to the normal feed endpoint.
func TestManualFeedForm(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")

	rr := doForm(h, "POST", "/fragments/manual-feed", url.Values{"url": {"https://www.example.com/feed"}}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("manual-feed: %d %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `name="feed_url"`) || !strings.Contains(body, `value="https://example.com/feed"`) {
		t.Fatalf("manual form should prefill the feed url (www stripped): %s", body)
	}
	if !strings.Contains(body, `hx-post="/feeds"`) {
		t.Fatalf("global manual form should submit to /feeds: %s", body)
	}

	// Scoped: the author is fixed and the form submits to the author endpoint.
	rr = doForm(h, "POST", "/fragments/manual-feed", url.Values{
		"url": {"https://example.com/feed"}, "author_id": {itoa(a.ID)}, "scoped": {"1"},
	}, cookie)
	body = rr.Body.String()
	if !strings.Contains(body, `hx-post="/authors/`+itoa(a.ID)+`/feeds"`) {
		t.Fatalf("scoped manual form should submit to the author endpoint: %s", body)
	}
	if !strings.Contains(body, `name="author_id" value="`+itoa(a.ID)+`"`) {
		t.Fatalf("scoped manual form should fix the author: %s", body)
	}
}

// TestAddFeedOffersManualButton asserts every "find feed" entry point also shows
// an "add manually" action, so a user who already knows the feed url is not
// forced through discovery.
func TestAddFeedOffersManualButton(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")

	// The authors index dialog.
	body := doGet(h, "/authors", cookie).Body.String()
	if !strings.Contains(body, "find feed") || !strings.Contains(body, `hx-post="/fragments/manual-feed"`) {
		t.Fatalf("authors dialog should offer both find and manual: %s", body)
	}
	if !strings.Contains(body, `hx-target="#author-preview"`) {
		t.Fatalf("authors manual button should target the author preview: %s", body)
	}

	// The author-scoped dialog.
	body = doGet(h, "/authors/"+itoa(a.ID), cookie).Body.String()
	if !strings.Contains(body, "find feed") || !strings.Contains(body, `hx-target="#author-feed-preview"`) {
		t.Fatalf("author dialog should offer both find and manual: %s", body)
	}

	// The PWA share landing page (it searches on load, so it only needs the
	// manual action, not the "find feed" button).
	body = doGet(h, "/add?url=https://example.com/feed", cookie).Body.String()
	if !strings.Contains(body, `hx-post="/fragments/manual-feed"`) || !strings.Contains(body, `hx-target="#share-preview"`) {
		t.Fatalf("share page should offer manual entry: %s", body)
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
	// The chooser must offer an explicit submit, not just auto-advance on change.
	if !strings.Contains(rr.Body.String(), "use this feed") {
		t.Fatalf("choose fragment should have a submit button: %s", rr.Body.String())
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

func TestFeedPreviewMultipleScoped(t *testing.T) {
	// The author-scoped dialog's preview container is #author-feed-preview, so
	// the chooser must target it (and the chosen feed must render the form).
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	feedSrv := feedPreviewServer(t)
	defer feedSrv.Close()
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")

	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(fmt.Sprintf(`<html><head><title>Both</title>
		  <link rel="alternate" type="application/rss+xml" href="%s/rss">
		  <link rel="alternate" type="application/atom+xml" href="%s/atom"></head></html>`, feedSrv.URL, feedSrv.URL)))
	}))
	defer page.Close()

	// Step 1: the chooser targets the scoped preview container.
	rr := doForm(h, "POST", "/fragments/feed-preview", url.Values{
		"url": {page.URL}, "author_id": {itoa(a.ID)}, "scoped": {"1"},
	}, cookie)
	body := rr.Body.String()
	if !strings.Contains(body, "pick one") || !strings.Contains(body, `hx-target="#author-feed-preview"`) {
		t.Fatalf("scoped choose fragment: %s", body)
	}
	if strings.Contains(body, `hx-target="#feed-preview"`) {
		t.Fatalf("scoped chooser must not target the global container: %s", body)
	}

	// Step 2: picking a feed renders the author-scoped add form with submit.
	rr = doForm(h, "POST", "/fragments/feed-preview", url.Values{
		"url": {page.URL}, "author_id": {itoa(a.ID)}, "scoped": {"1"}, "feed_url": {feedSrv.URL + "/atom"},
	}, cookie)
	body = rr.Body.String()
	if !strings.Contains(body, feedSrv.URL+"/atom") || !strings.Contains(body, "<button type=\"submit\">add</button>") {
		t.Fatalf("scoped chosen preview: %s", body)
	}
	if !strings.Contains(body, `hx-post="/authors/`+itoa(a.ID)+`/feeds"`) {
		t.Fatalf("scoped form should submit to the author feeds endpoint: %s", body)
	}
}

func TestFeedPreviewPreselectsAuthor(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	feedSrv := feedPreviewServer(t)
	defer feedSrv.Close()

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")

	rr := doForm(h, "POST", "/fragments/feed-preview", url.Values{
		"url":       {feedSrv.URL + "/rss"},
		"author_id": {itoa(a.ID)},
	}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	// The author combo's hidden input carries the selected author id, and its
	// wrapper includes both the field value and the caller's home-url field.
	if !strings.Contains(body, `data-combo="author_id"`) ||
		!strings.Contains(body, `hx-include="closest .combo-field, #feed-home-url"`) {
		t.Fatalf("author combo should carry scoped htmx attributes: %s", body)
	}
	if !strings.Contains(body, `name="author_id" value="`+itoa(a.ID)+`"`) {
		t.Fatalf("expected author preselected in the form: %s", body)
	}
	// Without author_id the hidden input defaults to "new".
	rr = doForm(h, "POST", "/fragments/feed-preview", url.Values{
		"url": {feedSrv.URL + "/rss"},
	}, cookie)
	if !strings.Contains(rr.Body.String(), `name="author_id" value="new"`) {
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
