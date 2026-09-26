package plugin

import (
	"fmt"
	"sync"
	"time"

	"github.com/metruzanca/nanoflux/internal/store"
	"github.com/metruzanca/nanoflux/pluginapi"
)

// describe renders a plugin for logs.
func describe(f pluginapi.Fetcher) string {
	m := f.Meta()
	return fmt.Sprintf("%s (api %s)", m.Name, m.APIVersion)
}

// Cooldown tracks, per registrable host, a time before which the host must not
// be hit again — set when a fetch is rate limited. It is in-memory (reset on
// restart) and complements the persisted per-feed backoff. The plugin host and
// the poller share one instance (wired in cmd/server) so a limit seen by either
// paces both.
type Cooldown struct {
	mu    sync.Mutex
	until map[string]time.Time
}

// NewCooldown returns an empty Cooldown.
func NewCooldown() *Cooldown {
	return &Cooldown{until: map[string]time.Time{}}
}

// Cooling reports whether the host for rawURL is currently backed off.
func (c *Cooldown) Cooling(rawURL string) bool {
	_, ok := c.Until(rawURL)
	return ok
}

// Until returns the time until which the host for rawURL is backed off and
// whether it is currently cooling. An expired entry is pruned.
func (c *Cooldown) Until(rawURL string) (time.Time, bool) {
	host := store.RegistrableDomain(rawURL)
	if host == "" {
		return time.Time{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.until[host]
	if !ok {
		return time.Time{}, false
	}
	if time.Now().After(t) {
		delete(c.until, host)
		return time.Time{}, false
	}
	return t, true
}

// Cool records that the host for rawURL must not be hit again until t.
func (c *Cooldown) Cool(rawURL string, t time.Time) {
	host := store.RegistrableDomain(rawURL)
	if host == "" {
		return
	}
	c.mu.Lock()
	if existing, ok := c.until[host]; !ok || t.After(existing) {
		c.until[host] = t
	}
	c.mu.Unlock()
}
