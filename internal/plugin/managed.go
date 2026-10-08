package plugin

import (
	"context"
	"net/url"
	"sync"
	"time"

	goplugin "github.com/hashicorp/go-plugin"

	"github.com/metruzanca/nanoflux/pluginapi"
)

// idleTimeout is how long an external plugin subprocess may sit unused before
// it is killed; the next call respawns it. 0 keeps every process resident.
// Set before Setup.
var idleTimeout = 5 * time.Minute

// SetIdleTimeout sets how long an idle external plugin process is kept. 0
// disables reaping (every loaded plugin stays resident, the old behavior).
func SetIdleTimeout(d time.Duration) {
	if d < 0 {
		d = 0
	}
	idleTimeout = d
}

// matchCacheLimit bounds a plugin's URL-match cache; on overflow it is cleared.
const matchCacheLimit = 8192

type matchKey struct {
	cap pluginapi.Capability
	url string
}

// managedExternal is an external plugin whose subprocess is started at load and
// killed after a period of disuse, respawned on the next real call. It forwards
// every interface the gRPC client implements, so the rest of the host is
// unaware of the lifecycle.
//
// A Match is answered from a cache and does not count as use; otherwise the
// per-render Match sweep would keep every plugin alive forever. Only real work
// (Fetch, Render, Enrich, ...) extends the process's life.
type managedExternal struct {
	path string

	mu       sync.Mutex
	client   *goplugin.Client
	inner    pluginapi.Fetcher
	meta     pluginapi.Meta
	users    int
	lastUsed time.Time

	matchMu sync.Mutex
	matches map[matchKey]bool

	stop     chan struct{}
	stopOnce sync.Once
}

func newManagedExternal(path string) (*managedExternal, error) {
	m := &managedExternal{
		path:    path,
		matches: map[matchKey]bool{},
		stop:    make(chan struct{}),
	}
	// Describe once up front so Meta is available without a later spawn.
	if err := m.spawnLocked(); err != nil {
		return nil, err
	}
	if idleTimeout > 0 {
		go m.reapLoop()
	}
	return m, nil
}

// spawnLocked starts the subprocess and dispenses its Fetcher. Caller holds mu
// (or is the constructor before the value is shared).
func (m *managedExternal) spawnLocked() error {
	f, client, err := loadOne(context.Background(), m.path)
	if err != nil {
		return err
	}
	m.client = client
	m.inner = f
	if m.meta.Name == "" {
		m.meta = f.Meta()
	}
	m.lastUsed = time.Now()
	return nil
}

// call ensures the process is running, invokes fn with the live Fetcher, and
// releases it. touch extends the idle deadline (real work does; Match does not).
func (m *managedExternal) call(touch bool, fn func(f pluginapi.Fetcher) error) error {
	m.mu.Lock()
	if m.client == nil {
		if err := m.spawnLocked(); err != nil {
			m.mu.Unlock()
			return err
		}
	}
	m.users++
	f := m.inner
	m.mu.Unlock()

	err := fn(f)

	m.mu.Lock()
	m.users--
	if touch {
		m.lastUsed = time.Now()
	}
	m.mu.Unlock()
	return err
}

func (m *managedExternal) reapLoop() {
	interval := idleTimeout / 2
	if interval < time.Second {
		interval = time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
			m.mu.Lock()
			if m.client != nil && m.users == 0 && time.Since(m.lastUsed) > idleTimeout {
				m.client.Kill()
				m.client = nil
				m.inner = nil
			}
			m.mu.Unlock()
		}
	}
}

// Close stops the reaper and kills the subprocess.
func (m *managedExternal) Close() {
	m.stopOnce.Do(func() { close(m.stop) })
	m.mu.Lock()
	if m.client != nil {
		m.client.Kill()
		m.client = nil
		m.inner = nil
	}
	m.mu.Unlock()
}

// Meta returns the cached plugin description (no process needed).
func (m *managedExternal) Meta() pluginapi.Meta {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.meta
}

// Match answers from the cache when possible, otherwise asks the plugin once and
// caches the answer. It does not count as use.
func (m *managedExternal) Match(u *url.URL, cap pluginapi.Capability) bool {
	key := matchKey{cap: cap, url: u.String()}
	m.matchMu.Lock()
	if v, ok := m.matches[key]; ok {
		m.matchMu.Unlock()
		return v
	}
	m.matchMu.Unlock()

	var res bool
	_ = m.call(false, func(f pluginapi.Fetcher) error {
		res = f.Match(u, cap)
		return nil
	})

	m.matchMu.Lock()
	if len(m.matches) >= matchCacheLimit {
		m.matches = map[matchKey]bool{}
	}
	m.matches[key] = res
	m.matchMu.Unlock()
	return res
}

func (m *managedExternal) Discover(ctx context.Context, pageURL string, h pluginapi.Host) ([]pluginapi.Candidate, error) {
	var out []pluginapi.Candidate
	err := m.call(true, func(f pluginapi.Fetcher) error {
		var e error
		out, e = f.Discover(ctx, pageURL, h)
		return e
	})
	return out, err
}

func (m *managedExternal) Fetch(ctx context.Context, req pluginapi.FetchRequest, h pluginapi.Host) (pluginapi.Result, error) {
	var out pluginapi.Result
	err := m.call(true, func(f pluginapi.Fetcher) error {
		var e error
		out, e = f.Fetch(ctx, req, h)
		return e
	})
	return out, err
}

func (m *managedExternal) Render(ctx context.Context, req pluginapi.RenderRequest, h pluginapi.Host) (pluginapi.Media, error) {
	var out pluginapi.Media
	err := m.call(true, func(f pluginapi.Fetcher) error {
		r, ok := f.(pluginapi.Renderer)
		if !ok {
			return pluginapi.ErrUnsupportedCapability
		}
		var e error
		out, e = r.Render(ctx, req, h)
		return e
	})
	return out, err
}

func (m *managedExternal) Docs() string {
	var out string
	_ = m.call(true, func(f pluginapi.Fetcher) error {
		if d, ok := f.(pluginapi.Docser); ok {
			out = d.Docs()
		}
		return nil
	})
	return out
}

func (m *managedExternal) SharedKeys(ctx context.Context, req pluginapi.SharedKeyRequest, h pluginapi.Host) ([]pluginapi.ItemSharedKey, error) {
	var out []pluginapi.ItemSharedKey
	err := m.call(true, func(f pluginapi.Fetcher) error {
		sk, ok := f.(pluginapi.SharedKeyer)
		if !ok {
			return pluginapi.ErrUnsupportedCapability
		}
		var e error
		out, e = sk.SharedKeys(ctx, req, h)
		return e
	})
	return out, err
}

func (m *managedExternal) Enrich(ctx context.Context, req pluginapi.EnrichRequest, h pluginapi.Host) ([]pluginapi.Enriched, error) {
	var out []pluginapi.Enriched
	err := m.call(true, func(f pluginapi.Fetcher) error {
		en, ok := f.(pluginapi.Enricher)
		if !ok {
			return pluginapi.ErrUnsupportedCapability
		}
		var e error
		out, e = en.Enrich(ctx, req, h)
		return e
	})
	return out, err
}

func (m *managedExternal) Decorate(ctx context.Context, req pluginapi.DecorateRequest) ([]pluginapi.Decorated, error) {
	var out []pluginapi.Decorated
	err := m.call(true, func(f pluginapi.Fetcher) error {
		dec, ok := f.(pluginapi.Decoration)
		if !ok {
			return pluginapi.ErrUnsupportedCapability
		}
		var e error
		out, e = dec.Decorate(ctx, req)
		return e
	})
	return out, err
}

func (m *managedExternal) CanonicalizeFeedURL(raw string) string {
	out := raw
	_ = m.call(false, func(f pluginapi.Fetcher) error {
		if p, ok := f.(pluginapi.URLPolicy); ok {
			out = p.CanonicalizeFeedURL(raw)
		}
		return nil
	})
	return out
}

func (m *managedExternal) FeedToken(feedURL string) string {
	var out string
	_ = m.call(false, func(f pluginapi.Fetcher) error {
		if p, ok := f.(pluginapi.URLPolicy); ok {
			out = p.FeedToken(feedURL)
		}
		return nil
	})
	return out
}

func (m *managedExternal) Provision(ctx context.Context, req pluginapi.ProvisionRequest, h pluginapi.Host) (pluginapi.Provisioned, error) {
	var out pluginapi.Provisioned
	err := m.call(true, func(f pluginapi.Fetcher) error {
		p, ok := f.(pluginapi.Provisioner)
		if !ok {
			return pluginapi.ErrUnsupportedCapability
		}
		var e error
		out, e = p.Provision(ctx, req, h)
		return e
	})
	return out, err
}

func (m *managedExternal) FeedFields(feedURL string) []pluginapi.Field {
	var out []pluginapi.Field
	_ = m.call(false, func(f pluginapi.Fetcher) error {
		if fa, ok := f.(pluginapi.FeedAdmin); ok {
			out = fa.FeedFields(feedURL)
		}
		return nil
	})
	return out
}

func (m *managedExternal) Action(ctx context.Context, req pluginapi.FeedActionRequest, h pluginapi.Host) (pluginapi.FeedActionResult, error) {
	var out pluginapi.FeedActionResult
	err := m.call(true, func(f pluginapi.Fetcher) error {
		fa, ok := f.(pluginapi.FeedAdmin)
		if !ok {
			return pluginapi.ErrUnsupportedCapability
		}
		var e error
		out, e = fa.Action(ctx, req, h)
		return e
	})
	return out, err
}

func (m *managedExternal) Settings() []pluginapi.SettingField {
	var out []pluginapi.SettingField
	_ = m.call(false, func(f pluginapi.Fetcher) error {
		if c, ok := f.(pluginapi.Configurable); ok {
			out = c.Settings()
		}
		return nil
	})
	return out
}

func (m *managedExternal) Configure(values map[string]string) {
	_ = m.call(true, func(f pluginapi.Fetcher) error {
		if c, ok := f.(pluginapi.Configurable); ok {
			c.Configure(values)
		}
		return nil
	})
}

// Compile-time proof that the wrapper satisfies every interface the gRPC client
// does, so the registry's type assertions behave identically.
var (
	_ pluginapi.Fetcher      = (*managedExternal)(nil)
	_ pluginapi.Renderer     = (*managedExternal)(nil)
	_ pluginapi.Docser       = (*managedExternal)(nil)
	_ pluginapi.SharedKeyer  = (*managedExternal)(nil)
	_ pluginapi.Enricher     = (*managedExternal)(nil)
	_ pluginapi.Decoration   = (*managedExternal)(nil)
	_ pluginapi.URLPolicy    = (*managedExternal)(nil)
	_ pluginapi.Provisioner  = (*managedExternal)(nil)
	_ pluginapi.FeedAdmin    = (*managedExternal)(nil)
	_ pluginapi.Configurable = (*managedExternal)(nil)
)
