package plugin

import (
	"net/http"

	"github.com/charmbracelet/log"

	"github.com/metruzanca/nanoflux/internal/store"
	"github.com/metruzanca/nanoflux/pluginapi"
)

// Hosts builds the Host a plugin uses for brokered calls back to the host.
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

// Setup builds the plugin system: it loads the external plugins from
// pluginsDir, installs the fetch dispatcher hook, and reconciles stored feeds
// against the loaded plugins. The returned Runtime is
// always non-nil; a broken plugin is logged and skipped. cool is the shared
// rate-limit cooldown (the poller holds the same one, so a limit seen by either
// paces both); a nil cool creates a fresh one.
func Setup(st *store.Store, client *http.Client, pluginsDir string, cool *Cooldown) *Runtime {
	if cool == nil {
		cool = NewCooldown()
	}
	reg := NewRegistry()
	hosts := NewHosts(client, cool)

	cleanup := LoadExternal(pluginsDir, reg)
	d := NewDispatcher(reg, hosts.For)
	d.Install()

	if reg.Empty() {
		log.Info("plugin system: no plugins loaded")
	} else {
		log.Info("plugin system ready", "plugins", reg.Names())
	}
	ConfigureAll(st, reg)
	ReconcileFeeds(st, reg)
	return &Runtime{Registry: reg, Hosts: hosts, cleanup: cleanup}
}

// ConfigureAll pushes every configurable plugin its stored values. It runs at
// startup after the plugins load and before ReconcileFeeds, so a plugin that
// matches URLs against its configuration (a service host) is configured before
// its feeds are adopted. A plugin with no stored values still receives an empty
// map, so it can report itself unconfigured.
func ConfigureAll(st *store.Store, reg *Registry) {
	for _, info := range reg.Infos() {
		if !info.HasSettings {
			continue
		}
		schema := reg.SettingsSchema(info.Name)
		names := make([]string, 0, len(schema))
		for _, f := range schema {
			names = append(names, f.Name)
		}
		values := st.Settings.PluginSettings(info.Name, names)
		reg.Configure(info.Name, values)
	}
}

// ReconcileFeeds re-derives each stored feed's owning plugin from the loaded
// registry. Ownership is not sticky: every startup matches the feed's URL
// against the current plugins, recording the winner (or clearing the owner when
// none matches, so the generic parser takes over). A feed left parked by an
// older build for a missing plugin is re-enabled; a feed the user paused by hand
// is never resumed.
func ReconcileFeeds(st *store.Store, reg *Registry) {
	feeds, err := st.Feeds.ListAll()
	if err != nil {
		log.Error("plugin reconcile: list feeds", "err", err)
		return
	}
	var adopted, cleared, reenabled int
	for _, f := range feeds {
		owner := ""
		if p := reg.Match(mustParse(f.FeedURL), pluginapi.CapFetch); p != nil {
			owner = p.Meta().Name
		}
		if owner != f.PluginName {
			if err := st.Feeds.SetPluginName(f.ID, owner); err != nil {
				log.Error("plugin reconcile: set owner", "feed_id", f.ID, "err", err)
				continue
			}
			if owner == "" {
				cleared++
			} else {
				adopted++
			}
		}
		// Legacy cleanup: builds before ownership became purely derived parked a
		// feed when its plugin was missing. Resume those now that a missing
		// plugin no longer means a dead feed (the generic parser can try it).
		if store.IsPluginMissingReason(f.DisabledReason) {
			if n, err := st.Feeds.ReenableAutoDisabled(f.ID); err != nil {
				log.Error("plugin reconcile: re-enable", "feed_id", f.ID, "err", err)
			} else {
				reenabled += n
			}
		}
	}
	if adopted > 0 || cleared > 0 || reenabled > 0 {
		log.Info("plugin reconcile", "adopted", adopted, "cleared", cleared, "reenabled", reenabled)
	}
}

// Close kills any loaded plugin subprocesses.
func (r *Runtime) Close() {
	if r != nil && r.cleanup != nil {
		r.cleanup()
	}
}
