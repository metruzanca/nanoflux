package plugin

import (
	"context"
	"net/url"
	"sort"

	"github.com/charmbracelet/log"

	"github.com/metruzanca/nanoflux/pluginapi"
)

// Registry holds the loaded plugins and routes discovery/fetch to the first
// matching one. Native plugins are registered first (they take precedence over
// external ones), then name order is stable within each group.
type Registry struct {
	native   []pluginapi.Fetcher
	external []pluginapi.Fetcher
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry { return &Registry{} }

// RegisterNative adds an in-process plugin. Native plugins win over external
// ones when several match the same URL.
func (r *Registry) RegisterNative(f pluginapi.Fetcher) {
	name := f.Meta().Name
	for _, existing := range r.native {
		if existing.Meta().Name == name {
			log.Warn("duplicate native plugin name; keeping the first", "plugin", name)
			return
		}
	}
	r.native = append(r.native, f)
	log.Info("registered native plugin", "plugin", describe(f))
}

// RegisterExternal adds a plugin loaded over gRPC.
func (r *Registry) RegisterExternal(f pluginapi.Fetcher) {
	r.external = append(r.external, f)
	log.Info("registered external plugin", "plugin", describe(f))
}

// Empty reports whether no plugin is registered (so callers can bypass the
// registry entirely).
func (r *Registry) Empty() bool { return len(r.native) == 0 && len(r.external) == 0 }

// all returns native then external plugins, each group in registration order.
func (r *Registry) all() []pluginapi.Fetcher {
	out := make([]pluginapi.Fetcher, 0, len(r.native)+len(r.external))
	out = append(out, r.native...)
	out = append(out, r.external...)
	return out
}

// Match returns the first plugin handling u for cap, or nil.
func (r *Registry) Match(u *url.URL, cap pluginapi.Capability) pluginapi.Fetcher {
	if u == nil {
		return nil
	}
	for _, f := range r.all() {
		if f.Match(u, cap) {
			return f
		}
	}
	return nil
}

// Discover runs every plugin that handles discovery for pageURL and merges the
// candidates, de-duplicating by FeedURL (first wins, so native metadata sets
// the preview). Errors from individual plugins are logged and skipped.
func (r *Registry) Discover(ctx context.Context, pageURL string, hosts func(pluginapi.Fetcher) pluginapi.Host) []pluginapi.Candidate {
	u, err := url.Parse(pageURL)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []pluginapi.Candidate
	for _, f := range r.all() {
		if !f.Match(u, pluginapi.CapDiscover) {
			continue
		}
		cs, err := f.Discover(ctx, pageURL, hosts(f))
		if err != nil {
			if err == pluginapi.ErrUnsupportedCapability {
				continue
			}
			log.Warn("plugin discover failed", "plugin", f.Meta().Name, "err", err)
			continue
		}
		for _, c := range cs {
			if c.FeedURL == "" || seen[c.FeedURL] {
				continue
			}
			seen[c.FeedURL] = true
			out = append(out, c)
		}
	}
	return out
}

// Names returns the registered plugin names, sorted (for admin/debug output).
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.native)+len(r.external))
	for _, f := range r.all() {
		names = append(names, f.Meta().Name)
	}
	sort.Strings(names)
	return names
}
