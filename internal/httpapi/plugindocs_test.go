package httpapi

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/metruzanca/nanoflux/internal/plugin"
	"github.com/metruzanca/nanoflux/pluginapi"
)

// docsStub is a native plugin that documents itself and matches docs on a host,
// mirroring how the reddit plugin reaches a feed the generic parser fetches.
type docsStub struct{}

func (docsStub) Meta() pluginapi.Meta {
	return pluginapi.Meta{
		Name:       "docsstub",
		APIVersion: pluginapi.APIVersion,
		Summary:    "a stub that documents itself",
	}
}
func (docsStub) Match(u *url.URL, cap pluginapi.Capability) bool {
	return u != nil && u.Hostname() == "docs.example" && (cap == pluginapi.CapDocs || cap == pluginapi.CapFetch)
}
func (docsStub) Discover(context.Context, string, pluginapi.Host) ([]pluginapi.Candidate, error) {
	return nil, pluginapi.ErrUnsupportedCapability
}
func (docsStub) Fetch(context.Context, pluginapi.FetchRequest, pluginapi.Host) (pluginapi.Result, error) {
	return pluginapi.Result{}, nil
}
func (docsStub) Docs() string { return "# docsstub\n\nUse the **category** field." }

// TestPluginDocsFragment proves the docs fragment renders a plugin's Markdown
// and falls back to a friendly note for unknown or undocumented plugins. It is
// reachable by a non-admin, since the feed edit page uses it.
func TestPluginDocsFragment(t *testing.T) {
	s, h := newTestServer(t)
	reg := plugin.NewRegistry()
	reg.RegisterNative(docsStub{})
	s.SetPlugins(reg, plugin.NewHosts(s.client, plugin.NewCooldown()))

	cookie := sessionCookie(t, h) // alice, not an admin

	body := doGet(h, "/fragments/plugin-docs?plugin=docsstub", cookie).Body.String()
	for _, want := range []string{"docsstub", "<h1", "category", "<strong>"} {
		if !strings.Contains(body, want) {
			t.Fatalf("docs fragment missing %q: %s", want, body)
		}
	}

	unknown := doGet(h, "/fragments/plugin-docs?plugin=nope", cookie).Body.String()
	if !strings.Contains(unknown, "no documentation") {
		t.Fatalf("unknown plugin should note no docs: %s", unknown)
	}

	none := doGet(h, "/fragments/plugin-docs?plugin=stub", cookie).Body.String()
	if !strings.Contains(none, "no plugin selected") && !strings.Contains(none, "no documentation") {
		t.Fatalf("missing plugin should note nothing to show: %s", none)
	}
}

// TestAdminPluginCardShowsSummaryAndDocs proves the admin plugin card shows a
// plugin's one-line summary and a docs button when it has documentation.
func TestAdminPluginCardShowsSummaryAndDocs(t *testing.T) {
	s, h := newTestServer(t)
	root := createUser(t, s, "root")
	if err := s.store.Users.SetAdmin(root.ID, true); err != nil {
		t.Fatal(err)
	}
	cookie := adminSession(t, s, "root")

	reg := plugin.NewRegistry()
	reg.RegisterNative(docsStub{})
	s.SetPlugins(reg, plugin.NewHosts(s.client, plugin.NewCooldown()))

	body := doGet(h, "/admin", cookie).Body.String()
	for _, want := range []string{"a stub that documents itself", "data-plugin-docs", `data-plugin="docsstub"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("admin plugins card missing %q: %s", want, body)
		}
	}
}

// TestFeedEditShowsDocsButton proves the feed edit page offers the documenting
// plugin's docs next to the filter rules — including for a feed the plugin does
// not own the fetch of (matched on CapDocs, the reddit case).
func TestFeedEditShowsDocsButton(t *testing.T) {
	s, h := newTestServer(t)
	u := createUser(t, s, "feeduser")
	cookie := adminSession(t, s, "feeduser")

	reg := plugin.NewRegistry()
	reg.RegisterNative(docsStub{})
	s.SetPlugins(reg, plugin.NewHosts(s.client, plugin.NewCooldown()))

	author, _ := s.store.Authors.Create(u.ID, "A", "", "")
	// A feed the generic parser fetches (plugin_name empty), like reddit's .rss.
	feed, _ := s.store.Feeds.CreateWithPlugin(u.ID, author.ID, "d",
		"https://docs.example/feed.xml", "", "", "", 900)

	body := doGet(h, "/authors/"+itoa(author.ID)+"/edit?feed="+itoa(feed.ID), cookie).Body.String()
	if !strings.Contains(body, "data-plugin-docs") || !strings.Contains(body, `data-plugin="docsstub"`) {
		t.Fatalf("feed edit should show the docs button: %s", body)
	}
}
