package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/metruzanca/nanoflux/internal/feedparse"
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

// sameURLPlugin serves its feed from the very URL it matches (an X
// profile), so the feed URL equals the page URL. It is used to prove the plugin
// candidate's preview metadata survives the direct-fetch shortcut.
type sameURLPlugin struct{}

func (sameURLPlugin) Meta() pluginapi.Meta {
	return pluginapi.Meta{Name: "sameurl", APIVersion: pluginapi.APIVersion}
}
func (sameURLPlugin) Match(u *url.URL, _ pluginapi.Capability) bool {
	return u != nil && u.Hostname() == "sameurl.example"
}
func (sameURLPlugin) Discover(context.Context, string, pluginapi.Host) ([]pluginapi.Candidate, error) {
	return []pluginapi.Candidate{{
		FeedURL: "https://sameurl.example/profile/jane",
		Title:   "jane",
		IconURL: "https://sameurl.example/avatar.png",
		HomeURL: "https://sameurl.example/profile/jane",
	}}, nil
}
func (sameURLPlugin) Fetch(context.Context, pluginapi.FetchRequest, pluginapi.Host) (pluginapi.Result, error) {
	return pluginapi.Result{
		Feed:  pluginapi.Feed{Title: "page SEO title", HomeURL: "https://sameurl.example/profile/jane"},
		Items: []pluginapi.Item{{GUID: "sameurl:1", Title: "post"}},
	}, nil
}

// TestPluginMetadataWinsOverDirectFetch proves that when a plugin both serves a
// URL as a feed and describes it (its feed URL is the page URL), the plugin's
// author name/avatar win over the page's generic SEO metadata — not the
// direct-fetch fallback.
func TestPluginMetadataWinsOverDirectFetch(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	reg := plugin.NewRegistry()
	reg.RegisterNative(sameURLPlugin{})
	s.SetPlugins(reg, plugin.NewHosts(s.client, plugin.NewCooldown()))

	// Install the plugin dispatcher process-wide for this test so the direct
	// fetch of the URL actually succeeds through the plugin, exercising the
	// shortcut the bug lived in. Restore it afterwards.
	plugin.NewDispatcher(reg, plugin.NewHosts(s.client, plugin.NewCooldown()).For).Install()
	defer feedparse.SetPlugin(nil)

	// The URL is both the plugin's feed and the page. The plugin's Discover
	// metadata must win over the generic <title> the direct fetch would fall
	// back to.
	rr := doForm(h, "POST", "/fragments/feed-preview", url.Values{
		"url": {"https://sameurl.example/profile/jane"},
	}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `value="jane"`) {
		t.Fatalf("author name should be the plugin title, not the page SEO title: %s", body)
	}
	if strings.Contains(body, "page SEO title") {
		t.Fatalf("page SEO title leaked into the add form: %s", body)
	}
	if !strings.Contains(body, `value="https://sameurl.example/avatar.png"`) {
		t.Fatalf("avatar should come from the plugin candidate: %s", body)
	}
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
