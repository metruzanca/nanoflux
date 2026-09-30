package httpapi

import (
	"testing"

	"github.com/metruzanca/nanoflux/internal/plugin"
	"github.com/metruzanca/nanoflux/internal/plugin/native/reddit"
	"github.com/metruzanca/nanoflux/internal/web"
)

// TestSetPluginsInstallsProxyBypass proves the plugin layer's proxy bypass is
// wired end to end: once a registry containing the reddit plugin is attached,
// reddit's media hosts render directly while everything else is still proxied.
func TestSetPluginsInstallsProxyBypass(t *testing.T) {
	s, _ := newTestServer(t)
	reg := plugin.NewRegistry()
	reg.RegisterNative(&reddit.Plugin{})
	s.SetPlugins(reg, nil)
	defer web.SetProxyBypass(nil)

	for _, u := range []string{
		"https://i.redd.it/x.jpeg",
		"https://preview.redd.it/x.jpeg?s=1",
		"https://external-preview.redd.it/x.jpeg",
		"https://b.thumbs.redditmedia.com/x.jpg",
		"https://www.redditstatic.com/x.png",
	} {
		if got := web.ProxiedImageURL(u); got != u {
			t.Errorf("reddit media %q should bypass the proxy, got %q", u, got)
		}
	}
	if got := web.ProxiedImageURL("https://cdn.example/x.jpg"); got == "https://cdn.example/x.jpg" {
		t.Errorf("non-reddit image should still be proxied, got %q", got)
	}
}
