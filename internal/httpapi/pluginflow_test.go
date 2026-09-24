package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/metruzanca/nanoflux/internal/plugin"
	"github.com/metruzanca/nanoflux/pluginapi"
)

// stubPlugin is a native Fetcher used to prove plugin discovery flows into the
// add-feed preview with its own metadata.
type stubPlugin struct{}

func (stubPlugin) Meta() pluginapi.Meta {
	return pluginapi.Meta{Name: "stub", APIVersion: pluginapi.APIVersion}
}
func (stubPlugin) Match(u *url.URL, cap pluginapi.Capability) bool {
	return u != nil && u.Hostname() == "plugin.example"
}
func (stubPlugin) Discover(context.Context, string, pluginapi.Host) ([]pluginapi.Candidate, error) {
	return []pluginapi.Candidate{{
		FeedURL: "https://plugin.example/feed.xml",
		Title:   "Plugin Author",
		IconURL: "https://plugin.example/avatar.png",
		HomeURL: "https://plugin.example",
	}}, nil
}
func (stubPlugin) Fetch(context.Context, pluginapi.FetchRequest, pluginapi.Host) (pluginapi.Result, error) {
	return pluginapi.Result{}, nil
}

func TestPluginDiscoveryInAddFlow(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	reg := plugin.NewRegistry()
	reg.RegisterNative(stubPlugin{})
	s.SetPlugins(reg, plugin.NewHosts(s.client, plugin.NewCooldown()))

	// The stub matches any host name "plugin.example"; the preview must not need
	// a real page fetch because the plugin supplies the candidate.
	rr := doForm(h, "POST", "/fragments/feed-preview", url.Values{
		"url": {"https://plugin.example/some/page"},
	}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	// The plugin's preview metadata must win the add form.
	if !strings.Contains(body, `value="Plugin Author"`) {
		t.Fatalf("author name should come from the plugin: %s", body)
	}
	if !strings.Contains(body, `value="https://plugin.example/avatar.png"`) {
		t.Fatalf("avatar should come from the plugin: %s", body)
	}
	if !strings.Contains(body, `value="https://plugin.example/feed.xml"`) {
		t.Fatalf("feed url should be the plugin candidate: %s", body)
	}
}

// TestAdminPluginsCard proves the admin page lists the loaded plugins with
// their kind and API version, and explains the empty state when none load.
func TestAdminPluginsCard(t *testing.T) {
	s, h := newTestServer(t)
	root := createUser(t, s, "root")
	if err := s.store.Users.SetAdmin(root.ID, true); err != nil {
		t.Fatal(err)
	}
	cookie := adminSession(t, s, "root")

	// No plugin system attached: the card explains the empty state.
	body := doGet(h, "/admin", cookie).Body.String()
	if !strings.Contains(body, "no plugins loaded") {
		t.Fatalf("admin page should show the empty plugin state: %s", body)
	}

	reg := plugin.NewRegistry()
	reg.RegisterNative(stubPlugin{})
	reg.RegisterExternal(stubPlugin{})
	s.SetPlugins(reg, plugin.NewHosts(s.client, plugin.NewCooldown()))

	body = doGet(h, "/admin", cookie).Body.String()
	for _, want := range []string{"plugins", "stub", "native", "external", pluginapi.APIVersion} {
		if !strings.Contains(body, want) {
			t.Fatalf("plugins card missing %q", want)
		}
	}
}
