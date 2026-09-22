package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestAPIEntities(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "", "")
	coll, _ := s.store.Collections.Create(u.ID, "Dev")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://b.dev/rss.xml", "", "", 900)

	rr := doGet(h, "/api/entities", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("entities: %d %s", rr.Code, rr.Body.String())
	}
	var body struct {
		Entities []struct {
			Kind string `json:"kind"`
			ID   int64  `json:"id"`
			Name string `json:"name"`
			URL  string `json:"url"`
		} `json:"entities"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	byKey := map[string]string{}
	for _, e := range body.Entities {
		byKey[e.Kind+":"+e.Name] = e.URL
	}
	for want, url := range map[string]string{
		"author:Metru":   "/authors/" + itoa(a.ID),
		"collection:Dev": "/collections/" + itoa(coll.ID),
		"feed:Blog":      "/feeds/" + itoa(f.ID),
	} {
		if byKey[want] != url {
			t.Errorf("entity %q url = %q, want %q (all: %+v)", want, byKey[want], url, body.Entities)
		}
	}
}

func TestAPIEntitiesScopedToUser(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	// A second user's entities must not appear.
	other, _ := s.store.Users.Create("bob", "hash")
	oa, _ := s.store.Authors.Create(other.ID, "BobAuthor", "", "", "")
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

func TestPaletteRendered(t *testing.T) {
	_, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	body := doGet(h, "/", cookie).Body.String()
	for _, want := range []string{
		`id="command-palette"`,
		`id="command-input"`,
		`id="entity-palette"`,
		`id="entity-input"`,
		`ctrl/⌘ + p`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("page missing palette markup %q", want)
		}
	}
}
