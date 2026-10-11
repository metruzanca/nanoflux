package httpapi

import (
	"testing"

	"github.com/metruzanca/nanoflux/internal/plugin"
	"github.com/metruzanca/nanoflux/internal/plugin/native/reddit"
	"github.com/metruzanca/nanoflux/internal/plugin/native/youtube"
)

// useRedditPlugin wires the native reddit plugin's URL policy and registry into
// a test server, so the add/discovery flows that used to rely on core reddit
// derivation now go through the plugin (matching production wiring in
// cmd/server/main.go).
func useRedditPlugin(t *testing.T, s *Server) {
	t.Helper()
	reg := plugin.NewRegistry()
	reg.RegisterNative(&reddit.Plugin{})
	s.SetPlugins(reg, plugin.NewHosts(s.client, plugin.NewCooldown()))
	s.store.SetURLPolicy(plugin.NewStoreURLPolicy(reg))
	s.store.SetItemDecorator(plugin.NewStoreDecorator(reg))
}

// useYouTubePlugin wires the native youtube plugin into a test server, so a
// youtube link's video card, thumbnail and embed come from the plugin rather
// than the core.
func useYouTubePlugin(t *testing.T, s *Server) {
	t.Helper()
	reg := plugin.NewRegistry()
	reg.RegisterNative(youtube.Plugin{})
	s.SetPlugins(reg, plugin.NewHosts(s.client, plugin.NewCooldown()))
	s.store.SetItemDecorator(plugin.NewStoreDecorator(reg))
}

// useRedditAndYouTube wires both native plugins for a test that mixes items from
// each site.
func useRedditAndYouTube(t *testing.T, s *Server) {
	t.Helper()
	reg := plugin.NewRegistry()
	reg.RegisterNative(&reddit.Plugin{})
	reg.RegisterNative(youtube.Plugin{})
	s.SetPlugins(reg, plugin.NewHosts(s.client, plugin.NewCooldown()))
	s.store.SetURLPolicy(plugin.NewStoreURLPolicy(reg))
	s.store.SetItemDecorator(plugin.NewStoreDecorator(reg))
}
