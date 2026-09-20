package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/store"
)

const apiFeedXML = `<?xml version="1.0"?>
<rss version="2.0"><channel>
  <title>API Feed</title>
  <link>https://api.dev/</link>
  <item><guid>a1</guid><title>API Item</title><link>https://api.dev/1</link></item>
</channel></rss>`

func apiJSON(h http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func apiToken(t *testing.T, s *Server, h http.Handler, username, password string) string {
	t.Helper()
	rr := apiJSON(h, "POST", "/api/login", "", map[string]string{
		"username": username, "password": password,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("api login: %d %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Token == "" {
		t.Fatal("no token returned")
	}
	return resp.Token
}

func TestAPILoginAndUnread(t *testing.T) {
	s, h := newTestServer(t)
	token := apiToken(t, s, h, "alice", "secret")

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "A", "", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "B", "https://b.dev/rss.xml", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{GUID: "g", Title: "Item", FetchedAt: db.Now()})

	rr := apiJSON(h, "GET", "/api/unread-count", token, nil)
	if rr.Code != http.StatusOK || !bytes.Contains(rr.Body.Bytes(), []byte(`"count":1`)) {
		t.Fatalf("unread-count: %d %s", rr.Code, rr.Body.String())
	}

	rr = apiJSON(h, "GET", "/api/items?unread=1", token, nil)
	if rr.Code != http.StatusOK || !bytes.Contains(rr.Body.Bytes(), []byte("Item")) {
		t.Fatalf("items: %d %s", rr.Code, rr.Body.String())
	}

	// Mark read via API.
	items, _ := s.store.Items.List(u.ID, store.ItemFilter{})
	rr = apiJSON(h, "POST", "/api/items/"+itoa(items[0].ID)+"/read", token, map[string]bool{"read": true})
	if rr.Code != http.StatusOK {
		t.Fatalf("item read: %d %s", rr.Code, rr.Body.String())
	}
	if n, _ := s.store.Items.CountUnread(u.ID, 0); n != 0 {
		t.Fatalf("unread = %d, want 0", n)
	}
}

func TestAPIDiscover(t *testing.T) {
	s, h := newTestServer(t)
	token := apiToken(t, s, h, "alice", "secret")

	feedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(apiFeedXML))
	}))
	defer feedSrv.Close()
	pageSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><head><link rel="alternate" type="application/rss+xml" href="` + feedSrv.URL + `"></head></html>`))
	}))
	defer pageSrv.Close()

	rr := apiJSON(h, "POST", "/api/discover", token, map[string]string{"url": pageSrv.URL})
	if rr.Code != http.StatusOK {
		t.Fatalf("discover: %d %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Candidates []struct {
			FeedURL  string `json:"feed_url"`
			Strategy string `json:"strategy"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Candidates) != 1 || resp.Candidates[0].FeedURL != feedSrv.URL {
		t.Fatalf("candidates = %+v", resp.Candidates)
	}
}

func TestAPISave(t *testing.T) {
	s, h := newTestServer(t)
	token := apiToken(t, s, h, "alice", "secret")

	feedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(apiFeedXML))
	}))
	defer feedSrv.Close()

	// Save with a new author.
	rr := apiJSON(h, "POST", "/api/save", token, map[string]any{
		"feed_url": feedSrv.URL,
		"author":   map[string]string{"name": "Metru", "url": "https://metru.dev"},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rr.Code, rr.Body.String())
	}
	var feed apiFeed
	if err := json.Unmarshal(rr.Body.Bytes(), &feed); err != nil {
		t.Fatal(err)
	}
	if feed.AuthorName != "Metru" || feed.Title != "API Feed" {
		t.Fatalf("saved feed = %+v", feed)
	}

	// Save with an existing author + collection.
	c, _ := s.store.Collections.Create(1, "Dev")
	u, _ := s.store.Users.ByUsername("alice")
	authors, _ := s.store.Authors.List(u.ID)
	rr = apiJSON(h, "POST", "/api/save", token, map[string]any{
		"feed_url":      feedSrv.URL,
		"author_id":     authors[0].ID,
		"collection_id": c.ID,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("save existing: %d %s", rr.Code, rr.Body.String())
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &feed); err != nil {
		t.Fatal(err)
	}
	feeds, _ := s.store.Collections.Feeds(u.ID, c.ID)
	if len(feeds) != 1 || feeds[0].ID != feed.ID {
		t.Fatalf("collection membership wrong: %+v", feeds)
	}

	// A non-feed URL must be rejected.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>nope</html>"))
	}))
	defer bad.Close()
	rr = apiJSON(h, "POST", "/api/save", token, map[string]any{
		"feed_url": bad.URL,
		"author":   map[string]string{"name": "X"},
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("save bad url: %d", rr.Code)
	}
}

func TestAPICORS(t *testing.T) {
	_, h := newTestServer(t)
	req := httptest.NewRequest(http.MethodOptions, "/api/items", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("preflight: %d", rr.Code)
	}
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("allow-origin = %q", got)
	}
	if got := rr.Header().Get("Access-Control-Allow-Headers"); got == "" {
		t.Fatal("missing allow-headers")
	}
}

func TestAPIRequiresAuth(t *testing.T) {
	_, h := newTestServer(t)
	rr := apiJSON(h, "GET", "/api/items", "", nil)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("unauthed api: %d", rr.Code)
	}
	var resp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil || resp.Error == "" {
		t.Fatalf("expected json error body, got %s", rr.Body.String())
	}
}
