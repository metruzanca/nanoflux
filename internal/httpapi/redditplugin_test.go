package httpapi

import (
	"testing"

	"github.com/metruzanca/nanoflux/internal/plugin"
	"github.com/metruzanca/nanoflux/internal/plugin/native/reddit"
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
