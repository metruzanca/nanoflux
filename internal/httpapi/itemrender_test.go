package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/plugin"
	"github.com/metruzanca/nanoflux/internal/store"
	"github.com/metruzanca/nanoflux/pluginapi"
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

// sandboxStub is a plugin that asks the host to sandbox the embed for one watch
// URL shape, mirroring the RedGIFs player's click-through behavior.
type sandboxStub struct{}

func (sandboxStub) Meta() pluginapi.Meta {
	return pluginapi.Meta{Name: "sandboxstub", APIVersion: pluginapi.APIVersion}
}

func (sandboxStub) Match(u *url.URL, cap pluginapi.Capability) bool {
	return u != nil && cap == pluginapi.CapSandboxEmbed && u.Path == "/watch/abc"
}

func (sandboxStub) Discover(context.Context, string, pluginapi.Host) ([]pluginapi.Candidate, error) {
	return nil, pluginapi.ErrUnsupportedCapability
}

func (sandboxStub) Fetch(context.Context, pluginapi.FetchRequest, pluginapi.Host) (pluginapi.Result, error) {
	return pluginapi.Result{}, pluginapi.ErrUnsupportedCapability
}

const sandboxAttr = `sandbox="allow-scripts allow-same-origin allow-presentation"`

// TestItemViewSandboxedEmbed proves a plugin-claimed embed renders inside a
// restrictive sandbox in both the authenticated modal and the public share page,
// while an unclaimed embed keeps its current rendering and "open live" stays the
// only way out.
func TestItemViewSandboxedEmbed(t *testing.T) {
	oembedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/watch/abc", "/watch/other":
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
	reg := plugin.NewRegistry()
	reg.RegisterExternal(sandboxStub{})
	s.SetPlugins(reg, plugin.NewHosts(s.client, plugin.NewCooldown()))

	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Someone", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Someone", oembedSrv.URL+"/feed", "", "", 900)

	add := func(guid, path string) int64 {
		t.Helper()
		if _, err := s.store.Items.Upsert(f.ID, store.Item{
			GUID: guid, Title: "A clip",
			Link:      oembedSrv.URL + path,
			ImageURL:  "https://media.example/abc-poster.jpg",
			Summary:   `<img src="https://media.example/abc-poster.jpg"/>`,
			FetchedAt: db.Now(),
		}); err != nil {
			t.Fatal(err)
		}
		id, err := s.store.Items.ByFeedIdentity(f.ID, guid)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.store.Items.ReplaceEnclosures(id, []store.Enclosure{
			{URL: "https://media.example/abc.mp4", MIMEType: "video/mp4"},
		}); err != nil {
			t.Fatal(err)
		}
		return id
	}

	// Claimed link: sandboxed in the modal, still offers "open live".
	claimed := add("sandbox:1", "/watch/abc")
	body := doGet(h, "/items/"+itoa(claimed)+"/view", cookie).Body.String()
	if !strings.Contains(body, sandboxAttr) {
		t.Fatalf("claimed embed should be sandboxed: %s", body)
	}
	if !strings.Contains(body, `>open live</a>`) || !strings.Contains(body, `target="_blank"`) {
		t.Fatalf("sandboxed item should still offer its open live link: %s", body)
	}
	if strings.Contains(body, "<video") {
		t.Fatalf("sandboxed item should render the player, not a bare video: %s", body)
	}

	// Unclaimed link: no sandbox attribute.
	other := add("sandbox:2", "/watch/other")
	body = doGet(h, "/items/"+itoa(other)+"/view", cookie).Body.String()
	if strings.Contains(body, sandboxAttr) {
		t.Fatalf("unclaimed embed should not be sandboxed: %s", body)
	}

	// The public share page gets the same sandbox.
	sh, err := s.store.Shares.Create(u.ID, claimed)
	if err != nil {
		t.Fatal(err)
	}
	pub := httptest.NewRequest(http.MethodGet, "/shared/"+sh.Token, nil)
	prr := httptest.NewRecorder()
	h.ServeHTTP(prr, pub)
	if prr.Code != http.StatusOK {
		t.Fatalf("public share page: %d %s", prr.Code, prr.Body.String())
	}
	if !strings.Contains(prr.Body.String(), sandboxAttr) {
		t.Fatalf("public share page should sandbox the claimed embed: %s", prr.Body.String())
	}
}
