package httpapi

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/store"
)

func TestAPIEntities(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")
	coll, _ := s.store.Collections.Create(u.ID, "Dev")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://b.dev/rss.xml", "", "", 900)
	// One unread item so the author, collection, and feed all report unread=1.
	s.store.Items.Upsert(f.ID, store.Item{GUID: "g1", Title: "Post", Link: "https://b.dev/1", FetchedAt: db.Now()})
	if err := s.store.Collections.AddFeed(u.ID, coll.ID, f.ID); err != nil {
		t.Fatal(err)
	}

	rr := doGet(h, "/api/entities", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("entities: %d %s", rr.Code, rr.Body.String())
	}
	var body struct {
		Entities []struct {
			Kind   string `json:"kind"`
			ID     int64  `json:"id"`
			Name   string `json:"name"`
			URL    string `json:"url"`
			Unread int    `json:"unread"`
		} `json:"entities"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	byKey := map[string]string{}
	unread := map[string]int{}
	for _, e := range body.Entities {
		byKey[e.Kind+":"+e.Name] = e.URL
		unread[e.Kind+":"+e.Name] = e.Unread
	}
	for want, url := range map[string]string{
		"author:Metru":   "/authors/" + itoa(a.ID),
		"collection:Dev": "/collections/" + itoa(coll.ID),
		"feed:Blog":      "/authors/" + itoa(a.ID) + "?feed=" + itoa(f.ID),
	} {
		if byKey[want] != url {
			t.Errorf("entity %q url = %q, want %q (all: %+v)", want, byKey[want], url, body.Entities)
		}
		if unread[want] != 1 {
			t.Errorf("entity %q unread = %d, want 1 (all: %+v)", want, unread[want], body.Entities)
		}
	}
}

func TestAPIEntitiesAuthorAvatar(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Picture", "https://cdn.example/avatar.png", "")
	if _, err := s.store.Collections.Create(u.ID, "Dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://b.example/rss.xml", "", "", 900); err != nil {
		t.Fatal(err)
	}

	decode := func() map[string]string {
		t.Helper()
		rr := doGet(h, "/api/entities", cookie)
		if rr.Code != http.StatusOK {
			t.Fatalf("entities: %d %s", rr.Code, rr.Body.String())
		}
		var body struct {
			Entities []struct {
				Kind   string `json:"kind"`
				Avatar string `json:"avatar"`
			} `json:"entities"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		avatars := map[string]string{}
		for _, e := range body.Entities {
			avatars[e.Kind] = e.Avatar
		}
		return avatars
	}

	// Only a remote avatar: the image proxy serves it.
	if got, want := decode()["author"], "/img?u="+url.QueryEscape("https://cdn.example/avatar.png"); got != want {
		t.Fatalf("remote avatar = %q, want %q", got, want)
	}

	// A cached avatar wins over the remote URL and is cache-busted by the
	// fetch time.
	if err := s.store.Authors.SetAvatarKey(u.ID, a.ID, "author-avatars/1/2", "2026-01-02T03:04:05Z"); err != nil {
		t.Fatal(err)
	}
	want := "/authors/" + itoa(a.ID) + "/avatar?v=" + url.QueryEscape("2026-01-02T03:04:05Z")
	avatars := decode()
	if got := avatars["author"]; got != want {
		t.Fatalf("cached avatar = %q, want %q", got, want)
	}
	// Only authors carry an avatar.
	for _, kind := range []string{"collection", "feed"} {
		if got := avatars[kind]; got != "" {
			t.Fatalf("%s entity avatar = %q, want empty", kind, got)
		}
	}
}

func TestAPIEntitiesScopedToUser(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	// A second user's entities must not appear.
	other, _ := s.store.Users.Create("bob", "hash")
	oa, _ := s.store.Authors.Create(other.ID, "BobAuthor", "", "")
	s.store.Collections.Create(other.ID, "BobCollection")
	s.store.Feeds.Create(other.ID, oa.ID, "BobFeed", "https://bob.dev/rss.xml", "", "", 900)

	rr := doGet(h, "/api/entities", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("entities: %d", rr.Code)
	}
	for _, name := range []string{"BobAuthor", "BobCollection", "BobFeed"} {
		if strings.Contains(rr.Body.String(), name) {
			t.Fatalf("another user's %q leaked into entities: %s", name, rr.Body.String())
		}
	}
}

func TestAPIEntitiesRequiresAuth(t *testing.T) {
	_, h := newTestServer(t)
	// No session cookie: the API auth path rejects with 401.
	if rr := doGet(h, "/api/entities", nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated entities should be 401, got %d", rr.Code)
	}
}

func TestCommandPaletteRendered(t *testing.T) {
	_, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	body := doGet(h, "/", cookie).Body.String()
	for _, want := range []string{
		`id="command-palette"`,
		`id="command-input"`,
		`id="command-results"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("page missing palette markup %q", want)
		}
	}
	if strings.Contains(body, `id="entity-palette"`) {
		t.Fatal("page still renders the removed entity palette")
	}
}

func TestNavUnreadCounts(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	u, _ := s.store.Users.ByUsername("alice")
	a1, _ := s.store.Authors.Create(u.ID, "Metru", "", "")
	a2, _ := s.store.Authors.Create(u.ID, "Other", "", "")
	f1, _ := s.store.Feeds.Create(u.ID, a1.ID, "Blog", "https://b.dev/rss.xml", "", "", 900)
	f2, _ := s.store.Feeds.Create(u.ID, a2.ID, "News", "https://n.dev/rss.xml", "", "", 900)
	s.store.Items.Upsert(f1.ID, store.Item{GUID: "g1", Title: "p1", Link: "https://b.dev/1", FetchedAt: db.Now()})
	s.store.Items.Upsert(f1.ID, store.Item{GUID: "g2", Title: "p2", Link: "https://b.dev/2", FetchedAt: db.Now()})
	s.store.Items.Upsert(f2.ID, store.Item{GUID: "g3", Title: "p3", Link: "https://n.dev/1", FetchedAt: db.Now()})

	body := doGet(h, "/", cookie).Body.String()
	for _, want := range []string{
		`id="nav-unread-count" class="nav-count">(3)`,
		`id="nav-authors-count" class="nav-count">(2)`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("topbar missing unread badge %q: %s", want, body)
		}
	}

	// With every item read, the badges are not rendered at all.
	s.store.Items.MarkAllRead(u.ID, 0)
	body = doGet(h, "/", cookie).Body.String()
	for _, unwanted := range []string{`id="nav-unread-count"`, `id="nav-authors-count"`} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("topbar should omit zero-count badge %q: %s", unwanted, body)
		}
	}
}

func TestAPINavCounts(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://b.dev/rss.xml", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{GUID: "g1", Title: "p1", Link: "https://b.dev/1", FetchedAt: db.Now()})

	rr := doGet(h, "/api/nav-counts", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("nav-counts: %d %s", rr.Code, rr.Body.String())
	}
	var got map[string]int
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["unread"] != 1 || got["authors"] != 1 {
		t.Fatalf("nav-counts = %+v, want unread=1 authors=1", got)
	}
}
