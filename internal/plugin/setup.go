package plugin

import (
	"context"
	"net/http"

	"github.com/charmbracelet/log"

	"github.com/metruzanca/nanoflux/internal/plugin/native/instagram"
	"github.com/metruzanca/nanoflux/internal/plugin/native/patreon"
	"github.com/metruzanca/nanoflux/internal/plugin/native/x"
	"github.com/metruzanca/nanoflux/internal/plugin/native/youtube"
	"github.com/metruzanca/nanoflux/pluginapi"
)

// Hosts builds the in-process Host for a plugin. The same factory serves native
// plugins (called directly) and external ones (serialized over the broker).
type Hosts struct {
	client *http.Client
	cool   *Cooldown
}

// NewHosts builds a Hosts over the given HTTP client and cooldown.
func NewHosts(client *http.Client, cool *Cooldown) *Hosts {
	return &Hosts{client: client, cool: cool}
}

// For returns the Host for one plugin, carrying the plugin's declared
// User-Agent (if any).
func (h *Hosts) For(f pluginapi.Fetcher) pluginapi.Host {
	return NewHost(h.client, h.cool, f.Meta().Name, f.Meta().UserAgent)
}

// Runtime is the wired plugin system: the registry, the host factory, and the
// cleanup for loaded subprocesses.
type Runtime struct {
	Registry *Registry
	Hosts    *Hosts
	cleanup  func()
}

// Setup builds the plugin system: it registers the native plugins, loads any
// external plugins from pluginsDir, and installs the fetch dispatcher hook.
// The returned Runtime is always non-nil; a broken plugin is logged and skipped.
func Setup(ctx context.Context, client *http.Client, pluginsDir string) *Runtime {
	reg := NewRegistry()
	hosts := NewHosts(client, NewCooldown())

	registerNative(reg)

	cleanup := LoadExternal(ctx, pluginsDir, reg, hosts.For)
	d := NewDispatcher(reg, hosts.For)
	d.Install()

	if reg.Empty() {
		log.Info("plugin system: no plugins loaded")
	} else {
		log.Info("plugin system ready", "plugins", reg.Names())
	}
	return &Runtime{Registry: reg, Hosts: hosts, cleanup: cleanup}
}

// Close kills any loaded plugin subprocesses.
func (r *Runtime) Close() {
	if r != nil && r.cleanup != nil {
		r.cleanup()
	}
}

// registerNative adds the plugins compiled into nanoflux. A native plugin wins
// over an external one when both match the same URL.
func registerNative(reg *Registry) {
	reg.RegisterNative(youtube.Plugin{})
	reg.RegisterNative(instagram.Plugin{})
	reg.RegisterNative(patreon.Plugin{})
	reg.RegisterNative(x.Plugin{})
}
