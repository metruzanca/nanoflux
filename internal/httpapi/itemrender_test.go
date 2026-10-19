package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/store"
)

func TestItemViewMediaEmbed(t *testing.T) {
	// An item from a media plugin carries a video enclosure and links to a
	// watch page that publishes oEmbed. The modal should render the provider's
	// iframe player, not a bare <video>.
	oembedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/watch/abc":
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(`<html><head><link rel="alternate" type="application/json+oembed" href="/oembed"></head></html>`))
		case "/oembed":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"html":"<iframe src='https://embed.example/ifr/abc' width='1080' height='1920' allowfullscreen></iframe>"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer oembedSrv.Close()

	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Someone", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Someone", oembedSrv.URL+"/feed", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{
		GUID: "media:1", Title: "A clip",
		Link:      oembedSrv.URL + "/watch/abc",
		ImageURL:  "https://media.example/abc-poster.jpg",
		Summary:   `<img src="https://media.example/abc-poster.jpg"/>`,
		FetchedAt: db.Now(),
	})
	items, _ := s.store.Items.List(u.ID, store.ItemFilter{})
	if err := s.store.Items.ReplaceEnclosures(items[0].ID, []store.Enclosure{
		{URL: "https://media.example/abc.mp4", MIMEType: "video/mp4"},
	}); err != nil {
		t.Fatal(err)
	}

	rr := doGet(h, "/items/"+itoa(items[0].ID)+"/view", cookie)
	body := rr.Body.String()
	if rr.Code != http.StatusOK {
		t.Fatalf("item view: %d %s", rr.Code, body)
	}
	if !strings.Contains(body, `src="https://embed.example/ifr/abc"`) {
		t.Fatalf("item with a playable enclosure should embed its link's oEmbed player: %s", body)
	}
	if strings.Contains(body, "<video") {
		t.Fatalf("embedded player should replace the bare video element: %s", body)
	}
}
