package feedparse

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestIsPatreonProfileURL(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"https://www.patreon.com/cw/chrisandjack", true},
		{"https://www.patreon.com/chrisandjack", true},
		{"https://patreon.com/cw/smartereveryday", true},
		{"https://www.patreon.com/api/campaigns/793924/posts", false},
		{"https://www.patreon.com/user?u=123", false},
		{"https://www.patreon.com/posts/123", false},
		{"https://www.patreon.com/", false},
		{"https://example.com/chrisandjack", false},
	}
	for _, c := range cases {
		u, err := url.Parse(c.raw)
		if err != nil {
			t.Fatalf("parse %q: %v", c.raw, err)
		}
		if got := isPatreonProfileURL(u); got != c.want {
			t.Errorf("isPatreonProfileURL(%q) = %v, want %v", c.raw, got, c.want)
		}
	}
}

func TestIsPatreonPostsURL(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"https://www.patreon.com/api/campaigns/793924/posts", true},
		{"https://www.patreon.com/api/campaigns/793924/posts?page[count]=20", true},
		{"https://www.patreon.com/cw/chrisandjack", false},
		{"https://www.patreon.com/api/campaigns/793924", false},
	}
	for _, c := range cases {
		u, err := url.Parse(c.raw)
		if err != nil {
			t.Fatalf("parse %q: %v", c.raw, err)
		}
		if got := isPatreonPostsURL(u); got != c.want {
			t.Errorf("isPatreonPostsURL(%q) = %v, want %v", c.raw, got, c.want)
		}
	}
}

func TestPatreonDocText(t *testing.T) {
	doc := `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"First line."}]},{"type":"paragraph","content":[{"type":"text","text":"Second "},{"type":"text","text":"line."}]}]}`
	if got := patreonDocText(doc); got != "First line.\nSecond line." {
		t.Errorf("patreonDocText = %q", got)
	}
	if got := patreonDocText(""); got != "" {
		t.Errorf("empty = %q", got)
	}
	if got := patreonDocText("not json"); got != "" {
		t.Errorf("non-json = %q", got)
	}
}

func TestPatreonImageURLShapes(t *testing.T) {
	if got := patreonImageURL([]byte(`"https://x.test/a.jpg"`)); got != "https://x.test/a.jpg" {
		t.Errorf("string shape = %q", got)
	}
	if got := patreonImageURL([]byte(`{"url":"https://x.test/b.jpg","large_url":"https://x.test/big.jpg"}`)); got != "https://x.test/big.jpg" {
		t.Errorf("object shape = %q", got)
	}
	if got := patreonImageURL(nil); got != "" {
		t.Errorf("nil = %q", got)
	}
}

// patreonTestServer serves a mock Patreon API: a campaign lookup and a
// cursor-paginated posts endpoint.
func patreonTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/campaigns", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("filter[vanity]") != "chrisandjack" {
			w.Write([]byte(`{"data":[]}`))
			return
		}
		w.Write([]byte(`{"data":[{"id":"793924","type":"campaign","attributes":{
			"name":"Chris & Jack","url":"https://www.patreon.com/chrisandjack",
			"summary":"sketch comedy","avatar_photo_url":"https://c.test/avatar.jpg"}}]}`))
	})
	mux.HandleFunc("/campaigns/793924/posts", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page[cursor]") != "" {
			w.Write([]byte(`{"data":[{"id":"1","type":"post","attributes":{
				"title":"Older","content_json_string":"{\"type\":\"doc\",\"content\":[{\"type\":\"paragraph\",\"content\":[{\"type\":\"text\",\"text\":\"old body\"}]}]}",
				"url":"https://www.patreon.com/chrisandjack/posts/older-1",
				"published_at":"2020-02-21T19:22:47.000+00:00"}}],"links":{"next":null}}`))
			return
		}
		w.Write([]byte(`{"data":[
			{"id":"2","type":"post","attributes":{
				"title":"Newest","content":null,
				"content_json_string":"{\"type\":\"doc\",\"content\":[{\"type\":\"paragraph\",\"content\":[{\"type\":\"text\",\"text\":\"new body\"}]}]}",
				"url":"https://www.patreon.com/chrisandjack/posts/newest-2",
				"published_at":"2026-07-20T16:36:52.000+00:00",
				"image":{"url":"https://c.test/post.jpg","large_url":"https://c.test/post-large.jpg"}}},
			{"id":"3","type":"post","attributes":{
				"title":"","content":null,"teaser_text":"members only teaser",
				"content":null,"url":"https://www.patreon.com/chrisandjack/posts/locked-3",
				"published_at":"2026-07-19T00:00:00.000+00:00","is_paid":true,
				"min_cents_pledged_to_view":500}}
		],"links":{"next":"https://www.patreon.com/api/campaigns/793924/posts?page[count]=20&page[cursor]=2024-06-01T22:15:00"}}`))
	})
	return httptest.NewServer(mux)
}

func TestFetchPatreonProfile(t *testing.T) {
	srv := patreonTestServer(t)
	defer srv.Close()
	old := patreonAPIBase
	patreonAPIBase = srv.URL
	defer func() { patreonAPIBase = old }()

	res, err := fetchPatreon(context.Background(), "https://www.patreon.com/cw/chrisandjack", srv.Client())
	if err != nil {
		t.Fatalf("fetchPatreon: %v", err)
	}
	if res.Feed.Title != "Chris & Jack" {
		t.Errorf("feed title = %q", res.Feed.Title)
	}
	if res.Feed.HomeURL != "https://www.patreon.com/chrisandjack" {
		t.Errorf("feed home = %q", res.Feed.HomeURL)
	}
	if res.Feed.ImageURL != "https://c.test/avatar.jpg" {
		t.Errorf("feed avatar = %q", res.Feed.ImageURL)
	}
	if len(res.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(res.Items))
	}
	first := res.Items[0]
	if first.GUID != "patreon:2" || first.Title != "Newest" || first.Link != "https://www.patreon.com/chrisandjack/posts/newest-2" {
		t.Errorf("first item = %+v", first)
	}
	if first.Summary != "new body" || first.ImageURL != "https://c.test/post-large.jpg" {
		t.Errorf("first summary/image = %q / %q", first.Summary, first.ImageURL)
	}
	if first.PublishedAt != "2026-07-20 16:36:52" {
		t.Errorf("first published = %q", first.PublishedAt)
	}
	// A member-locked post keeps its title derived from the teaser and stores
	// the teaser as the summary; it still has a stable GUID and link.
	locked := res.Items[1]
	if locked.GUID != "patreon:3" || locked.Title != "members only teaser" || locked.Summary != "members only teaser" {
		t.Errorf("locked item = %+v", locked)
	}
	// The cursor URL rides along for "load older items".
	if !strings.Contains(res.NextPageURL, "page[cursor]=") {
		t.Errorf("next page url = %q", res.NextPageURL)
	}
}

func TestFetchPatreonPostsURL(t *testing.T) {
	srv := patreonTestServer(t)
	defer srv.Close()
	old := patreonAPIBase
	patreonAPIBase = srv.URL
	defer func() { patreonAPIBase = old }()

	// A stored posts URL (the pagination cursor) is fetched directly, without a
	// campaign lookup. patreonFetchPosts is the direct path fetchPatreon takes;
	// recognition of the real API host is covered by TestIsPatreonPostsURL.
	res, err := patreonFetchPosts(context.Background(), srv.URL+"/campaigns/793924/posts?page%5Bcursor%5D=2024-06-01T22%3A15%3A00", srv.Client())
	if err != nil {
		t.Fatalf("patreonFetchPosts: %v", err)
	}
	items, next := patreonItems(res)
	if len(items) != 1 || items[0].GUID != "patreon:1" {
		t.Fatalf("items = %+v", items)
	}
	if next != "" {
		t.Errorf("next = %q, want empty", next)
	}
}

// TestFetchDispatchesPatreon proves Fetch routes a Patreon creator page to the
// Patreon reader (the short-circuit X and Instagram use), so discovery, the
// poller, and the preview all work unchanged.
func TestFetchDispatchesPatreon(t *testing.T) {
	oldHosts := patreonProfileHosts
	patreonProfileHosts = append([]string(nil), oldHosts...)
	patreonProfileHosts = append(patreonProfileHosts, "127.0.0.1")
	defer func() { patreonProfileHosts = oldHosts }()

	srv := patreonTestServer(t)
	defer srv.Close()
	oldBase := patreonAPIBase
	patreonAPIBase = srv.URL
	defer func() { patreonAPIBase = oldBase }()

	res, err := Fetch(context.Background(), srv.URL+"/cw/chrisandjack", srv.Client(), "", "")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.Feed.Title != "Chris & Jack" || len(res.Items) != 2 {
		t.Fatalf("fetch dispatch: title=%q items=%d", res.Feed.Title, len(res.Items))
	}
}
