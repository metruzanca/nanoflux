package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func mappingFeedServer(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/feed/"):
			w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>Mapped Feed</title><link>https://home.dev/</link><item><guid>1</guid><title>i</title><link>https://home.dev/1</link></item></channel></rss>`))
		case r.URL.Path == "/rss":
			w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>Fallback Feed</title><link>https://home.dev/</link><item><guid>1</guid><title>i</title><link>https://home.dev/1</link></item></channel></rss>`))
		case r.URL.Path == "/jane":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, `<html><head><title>Jane's Home</title><link rel="alternate" type="application/rss+xml" href="%s/rss"></head></html>`, srv.URL)
		default:
			http.NotFound(w, r)
		}
	}))
	return srv
}

// srvPort returns the port of a loopback test server.
func srvPort(srv *httptest.Server) string {
	host := strings.TrimPrefix(srv.URL, "http://")
	return host[strings.LastIndex(host, ":")+1:]
}

// userMapping seeds a mapping for the test user alice.
func userMapping(t *testing.T, s *Server, pattern, template string) {
	t.Helper()
	u, err := s.store.Users.ByUsername("alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.UrlMappings.Create(u.ID, pattern, template); err != nil {
		t.Fatal(err)
	}
}

func TestSettingsMappingsFlow(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	// The settings page renders the mapping card and test field.
	page := doGet(h, "/settings", cookie).Body.String()
	for _, want := range []string{"url mappings", `hx-post="/settings/mappings"`, `hx-post="/fragments/mapping-test"`, `name="pattern"`, `name="template"`, `name="test_url"`} {
		if !strings.Contains(page, want) {
			t.Fatalf("settings page missing %q", want)
		}
	}

	// Test endpoint: match, no-match, and invalid input.
	rr := doForm(h, "POST", "/fragments/mapping-test", url.Values{
		"pattern": {`abc\.com/(?P<user>[^/]+)`}, "template": {"{user}.abc.com/feed"}, "test_url": {"https://abc.com/john"},
	}, cookie)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "mapped url") || !strings.Contains(rr.Body.String(), "john.abc.com/feed") {
		t.Fatalf("test match: %d %s", rr.Code, rr.Body.String())
	}
	rr = doForm(h, "POST", "/fragments/mapping-test", url.Values{
		"pattern": {`abc\.com/(?P<user>[^/]+)`}, "template": {"{user}.abc.com/feed"}, "test_url": {"https://other.com/john"},
	}, cookie)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "no match") {
		t.Fatalf("test no match: %d %s", rr.Code, rr.Body.String())
	}
	rr = doForm(h, "POST", "/fragments/mapping-test", url.Values{
		"pattern": {`abc\.com/(?P<user>[^/]`}, "template": {"{user}.abc.com/feed"}, "test_url": {"https://abc.com/john"},
	}, cookie)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), `role="alert"`) {
		t.Fatalf("test invalid pattern: %d %s", rr.Code, rr.Body.String())
	}

	// Add a valid mapping.
	rr = doForm(h, "POST", "/settings/mappings", url.Values{
		"pattern": {`abc\.com/(?P<user>[^/]+)`}, "template": {"{user}.abc.com/feed"},
	}, cookie)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `id="settings-mapping-`) || !strings.Contains(rr.Body.String(), "{user}.abc.com/feed") {
		t.Fatalf("add mapping: %d %s", rr.Code, rr.Body.String())
	}

	// Duplicate pattern -> 400 with a visible error.
	rr = doForm(h, "POST", "/settings/mappings", url.Values{
		"pattern": {`abc\.com/(?P<user>[^/]+)`}, "template": {"{user}.abc.com/rss"},
	}, cookie)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "already exists") || !strings.Contains(rr.Body.String(), `role="alert"`) {
		t.Fatalf("duplicate mapping: %d %s", rr.Code, rr.Body.String())
	}

	// Invalid pattern/template on add -> 400 with the compile error.
	rr = doForm(h, "POST", "/settings/mappings", url.Values{
		"pattern": {`abc\.com/(?P<user>[^/]+)`}, "template": {"{missing}.abc.com/feed"},
	}, cookie)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "references {missing}") {
		t.Fatalf("template referencing unknown group: %d %s", rr.Code, rr.Body.String())
	}

	// Edit fragment swaps in the inline form with current values.
	id := mappingIDs(t, h, cookie)[0]
	rr = doGet(h, "/fragments/mapping-edit/"+id, cookie)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `value="abc\.com/(?P&lt;user&gt;[^/]+)"`) || !strings.Contains(rr.Body.String(), `hx-post="/settings/mappings/`+id) {
		t.Fatalf("edit fragment: %d %s", rr.Code, rr.Body.String())
	}

	// Update the template.
	rr = doForm(h, "POST", "/settings/mappings/"+id, url.Values{
		"pattern": {`abc\.com/(?P<user>[^/]+)`}, "template": {"{user}.abc.com/rss"},
	}, cookie)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "{user}.abc.com/rss") {
		t.Fatalf("update mapping: %d %s", rr.Code, rr.Body.String())
	}

	// Update with a duplicate pattern -> 400.
	userMapping(t, s, `def\.com/(?P<name>[^/]+)`, "{name}.def.com/feed")
	otherID := mappingIDs(t, h, cookie)[1]
	rr = doForm(h, "POST", "/settings/mappings/"+otherID, url.Values{
		"pattern": {`abc\.com/(?P<user>[^/]+)`}, "template": {"{user}.abc.com/rss"},
	}, cookie)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "already exists") {
		t.Fatalf("update duplicate pattern: %d %s", rr.Code, rr.Body.String())
	}

	// Cancel fragment renders the plain row.
	rr = doGet(h, "/fragments/mapping-row/"+id, cookie)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "{user}.abc.com/rss") || strings.Contains(rr.Body.String(), "test url") {
		t.Fatalf("row fragment: %d %s", rr.Code, rr.Body.String())
	}

	// Delete -> the list no longer contains the mapping.
	rr = doForm(h, "POST", "/settings/mappings/"+id+"/delete", url.Values{}, cookie)
	if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), "settings-mapping-"+id) {
		t.Fatalf("delete mapping: %d %s", rr.Code, rr.Body.String())
	}
}

func mappingIDs(t *testing.T, h http.Handler, cookie *http.Cookie) []string {
	t.Helper()
	body := doGet(h, "/settings", cookie).Body.String()
	var ids []string
	for _, part := range strings.Split(body, `id="settings-mapping-`) {
		if i := strings.IndexByte(part, '"'); i > 0 {
			id := part[:i]
			if isAllDigits(id) {
				ids = append(ids, id)
			}
		}
	}
	if len(ids) == 0 {
		t.Fatal("no mappings found on settings page")
	}
	return ids
}

func TestFeedPreviewUrlMapping(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	srv := mappingFeedServer(t)
	defer srv.Close()
	userMapping(t, s, `127\.0\.0\.1:`+srvPort(srv)+`/(?P<user>[^/]+)`, srv.URL+"/feed/{user}")

	// A profile url that matches the mapping pre-fills the mapped feed url.
	rr := doForm(h, "POST", "/fragments/feed-preview", url.Values{
		"url": {srv.URL + "/jane"},
	}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, srv.URL+"/feed/jane") {
		t.Fatalf("preview should pre-fill the mapped feed url: %s", body)
	}
	if !strings.Contains(body, `value="`+srv.URL+`/jane"`) {
		t.Fatalf("preview should keep the entered url as home: %s", body)
	}
}

func TestFeedPreviewUrlMappingFallback(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	srv := mappingFeedServer(t)
	defer srv.Close()
	// The mapped url 404s; discovery must fall back to the original page.
	userMapping(t, s, `127\.0\.0\.1:`+srvPort(srv)+`/(?P<user>[^/]+)`, srv.URL+"/gone/{user}")

	rr := doForm(h, "POST", "/fragments/feed-preview", url.Values{
		"url": {srv.URL + "/jane"},
	}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("preview fallback: %d %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, srv.URL+"/rss") {
		t.Fatalf("fallback should discover the original page's feed: %s", body)
	}
	if !strings.Contains(body, `value="`+srv.URL+`/jane"`) {
		t.Fatalf("fallback should keep the entered url as home: %s", body)
	}
}

func TestAPIDiscoverUrlMapping(t *testing.T) {
	s, h := newTestServer(t)
	token := apiToken(t, s, h, "alice", "secret")
	srv := mappingFeedServer(t)
	defer srv.Close()
	userMapping(t, s, `127\.0\.0\.1:`+srvPort(srv)+`/(?P<user>[^/]+)`, srv.URL+"/feed/{user}")

	rr := apiJSON(h, "POST", "/api/discover", token, map[string]string{"url": srv.URL + "/jane"})
	if rr.Code != http.StatusOK {
		t.Fatalf("api discover: %d %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Candidates []struct {
			FeedURL string `json:"feed_url"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Candidates) == 0 || resp.Candidates[0].FeedURL != srv.URL+"/feed/jane" {
		t.Fatalf("api discover should return the mapped feed: %+v", resp.Candidates)
	}
}
