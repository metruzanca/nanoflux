package plugin

import (
	"context"
	"net/http"

	"github.com/charmbracelet/log"

	"github.com/metruzanca/nanoflux/internal/plugin/native/instagram"
	"github.com/metruzanca/nanoflux/internal/plugin/native/patreon"
	"github.com/metruzanca/nanoflux/internal/plugin/native/x"
	"github.com/metruzanca/nanoflux/internal/plugin/native/youtube"
	"github.com/metruzanca/nanoflux/internal/store"
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
// external plugins from pluginsDir, installs the fetch dispatcher hook, and
// reconciles stored feeds against the loaded plugins. The returned Runtime is
// always non-nil; a broken plugin is logged and skipped.
func Setup(ctx context.Context, st *store.Store, client *http.Client, pluginsDir string) *Runtime {
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
	ReconcileFeeds(st, reg)
	return &Runtime{Registry: reg, Hosts: hosts, cleanup: cleanup}
}

// ReconcileFeeds aligns stored feeds with the loaded plugins. For each feed it
// records which plugin owns its URL and auto-disables a feed whose owning plugin
// is no longer loaded (so it stops retrying a URL nothing can fetch). It also
// re-enables feeds that were disabled for exactly that reason when their plugin
// returns. A feed the user paused by hand is never resumed. An empty registry is
// handled too: if every plugin is gone, every plugin-owned feed is parked.
func ReconcileFeeds(st *store.Store, reg *Registry) {
	// Resume feeds parked because a now-loaded plugin was missing. Only feeds
	// carrying the automatic "plugin not loaded: <name>" reason are touched.
	reenabled := 0
	for _, name := range reg.Names() {
		if n, err := st.Feeds.ReenableForPlugin(name); err != nil {
			log.Error("plugin reconcile: re-enable", "plugin", name, "err", err)
		} else {
			reenabled += n
		}
	}

	feeds, err := st.Feeds.ListAll()
	if err != nil {
		log.Error("plugin reconcile: list feeds", "err", err)
		return
	}
	var adopted, disabled int
	for _, f := range feeds {
		owner := ""
		if p := reg.Match(mustParse(f.FeedURL), pluginapi.CapFetch); p != nil {
			owner = p.Meta().Name
		}
		switch {
		case owner != "" && owner != f.PluginName:
			// Adopt a feed that now has a plugin (or whose owner changed).
			if err := st.Feeds.SetPluginName(f.ID, owner); err != nil {
				log.Error("plugin reconcile: set owner", "feed_id", f.ID, "err", err)
				continue
			}
			adopted++
		case owner == "" && f.PluginName != "" && f.Enabled:
			// The owning plugin is gone: park the feed instead of retrying.
			if err := st.Feeds.DisableForMissingPlugin(f.ID, f.PluginName); err != nil {
				log.Error("plugin reconcile: disable", "feed_id", f.ID, "err", err)
				continue
			}
			disabled++
		}
	}
	if adopted > 0 || disabled > 0 || reenabled > 0 {
		log.Info("plugin reconcile", "adopted", adopted, "disabled", disabled, "reenabled", reenabled)
	}
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
