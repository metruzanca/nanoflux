package plugin

import (
	"context"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/log"

	"github.com/metruzanca/nanoflux/pluginapi"
)

// Registry holds the loaded plugins and routes discovery/fetch to the first
// matching one, in registration order.
type Registry struct {
	external []pluginapi.Fetcher
	// minPoll is each plugin's declared poll floor, aligned with external. It is
	// read once at registration so a per-feed floor lookup does not ask every
	// plugin for its Meta on every poll.
	minPoll []time.Duration

	// minMu guards minCache, which memoizes the per-URL poll floor. Match may
	// depend on plugin configuration, so Configure clears it.
	minMu    sync.Mutex
	minCache map[string]time.Duration
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{minCache: map[string]time.Duration{}}
}

// RegisterExternal adds a plugin loaded over gRPC.
func (r *Registry) RegisterExternal(f pluginapi.Fetcher) {
	m := f.Meta()
	name := m.Name
	for _, existing := range r.external {
		if existing.Meta().Name == name {
			log.Warn("duplicate plugin name; keeping the first", "plugin", name)
			return
		}
	}
	r.external = append(r.external, f)
	r.minPoll = append(r.minPoll, m.MinPollInterval)
	r.clearMinPollCache()
	log.Info("registered external plugin", "plugin", describe(f))
}

// Empty reports whether no plugin is registered (so callers can bypass the
// registry entirely).
func (r *Registry) Empty() bool { return len(r.external) == 0 }

// all returns the plugins in registration order.
func (r *Registry) all() []pluginapi.Fetcher { return r.external }

// Match returns the first plugin handling u for cap, or nil.
func (r *Registry) Match(u *url.URL, cap pluginapi.Capability) pluginapi.Fetcher {
	if u == nil {
		return nil
	}
	for _, f := range r.external {
		if f.Match(u, cap) {
			return f
		}
	}
	return nil
}

// MatchRenderer returns the first plugin that matches u for rendering and
// implements the Renderer interface, along with its Fetcher (for the host
// factory), or (nil, nil).
func (r *Registry) MatchRenderer(u *url.URL) (pluginapi.Renderer, pluginapi.Fetcher) {
	if u == nil {
		return nil, nil
	}
	for _, f := range r.all() {
		if !f.Match(u, pluginapi.CapRender) {
			continue
		}
		if rd, ok := f.(pluginapi.Renderer); ok {
			return rd, f
		}
	}
	return nil, nil
}

// MatchEnricher returns the first plugin that enriches u's item body (full
// text, translation, transcript), along with its Fetcher (for the host
// factory), or (nil, nil).
func (r *Registry) MatchEnricher(u *url.URL) (pluginapi.Enricher, pluginapi.Fetcher) {
	if u == nil {
		return nil, nil
	}
	for _, f := range r.all() {
		if !f.Match(u, pluginapi.CapEnrich) {
			continue
		}
		if en, ok := f.(pluginapi.Enricher); ok {
			return en, f
		}
	}
	return nil, nil
}

// MatchSharedKeyer returns the first plugin that assigns cross-feed keys to u's
// parsed items at ingest, along with its Fetcher (for the host factory), or
// (nil, nil).
func (r *Registry) MatchSharedKeyer(u *url.URL) (pluginapi.SharedKeyer, pluginapi.Fetcher) {
	if u == nil {
		return nil, nil
	}
	for _, f := range r.all() {
		if !f.Match(u, pluginapi.CapSharedKey) {
			continue
		}
		if sk, ok := f.(pluginapi.SharedKeyer); ok {
			return sk, f
		}
	}
	return nil, nil
}

// RenderItem runs the plugin that handles view-time rendering for the item's
// link and returns its media. When no plugin matches (or the match does not
// implement Renderer), it returns an empty Media so the caller falls back to
// the item's stored fields. Errors from the plugin are returned for the caller
// to log; ErrUnsupportedCapability is reported as (Media{}, nil).
func (r *Registry) RenderItem(ctx context.Context, req pluginapi.RenderRequest, hosts func(pluginapi.Fetcher) pluginapi.Host) (pluginapi.Media, error) {
	if r.Empty() {
		return pluginapi.Media{}, nil
	}
	u, err := url.Parse(req.Link)
	if err != nil {
		return pluginapi.Media{}, nil
	}
	rd, f := r.MatchRenderer(u)
	if rd == nil {
		return pluginapi.Media{}, nil
	}
	m, err := rd.Render(ctx, req, hosts(f))
	if err != nil {
		if err == pluginapi.ErrUnsupportedCapability {
			return pluginapi.Media{}, nil
		}
		return pluginapi.Media{}, err
	}
	return m, nil
}

// Discover runs every plugin that handles discovery for pageURL and merges the
// candidates, de-duplicating by FeedURL (first wins, so the first plugin's
// metadata sets the preview). Errors from individual plugins are logged and
// skipped.
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
	names := make([]string, 0, len(r.external))
	for _, f := range r.external {
		names = append(names, f.Meta().Name)
	}
	sort.Strings(names)
	return names
}

// Info describes one loaded plugin for display (admin page, debug output).
type Info struct {
	Name      string
	Version   string // plugin API version the plugin reported
	RawNet    bool   // plugin opted out of host-mediated HTTP
	UserAgent string // per-plugin User-Agent override, "" for the app default
	Summary   string // one-line description, "" when the plugin gave none
	HasDocs   bool   // plugin implements Docser (offers a readme)
	// ProvisionLabel is the create-menu entry when the plugin can create remote
	// feeds; "" when it cannot (or does not advertise it).
	ProvisionLabel string
	// CanManageFeeds reports whether the plugin manages already-subscribed
	// feeds' remote settings and lifecycle (implements FeedAdmin).
	CanManageFeeds bool
	// HasSettings reports whether the plugin implements Configurable (declares
	// settings the admin can edit).
	HasSettings bool
}

// Infos returns a description of every loaded plugin in registration order.
func (r *Registry) Infos() []Info {
	out := make([]Info, 0, len(r.external))
	for _, f := range r.external {
		out = append(out, info(f))
	}
	return out
}

func info(f pluginapi.Fetcher) Info {
	m := f.Meta()
	// Trust the capability flags reported over the gRPC boundary. The client
	// wrapper satisfies every optional interface unconditionally, so asserting
	// on f would report every capability for every plugin; the plugin's gRPC
	// server computes these flags from its real implementation instead.
	return Info{
		Name:           m.Name,
		Version:        m.APIVersion,
		RawNet:         m.RawNetwork,
		UserAgent:      m.UserAgent,
		Summary:        m.Summary,
		HasDocs:        m.HasDocs,
		ProvisionLabel: m.ProvisionLabel,
		CanManageFeeds: m.HasFeedAdmin,
		HasSettings:    m.HasSettings,
	}
}

// ByName returns the loaded plugin with the given name, or nil.
func (r *Registry) ByName(name string) pluginapi.Fetcher {
	if name == "" {
		return nil
	}
	for _, f := range r.all() {
		if f.Meta().Name == name {
			return f
		}
	}
	return nil
}

// URLPolicy returns the first plugin that owns URL rules for u, or nil. It is
// gated by CapURLPolicy and requires the URLPolicy interface.
func (r *Registry) URLPolicy(u *url.URL) pluginapi.URLPolicy {
	if u == nil {
		return nil
	}
	for _, f := range r.all() {
		if !f.Match(u, pluginapi.CapURLPolicy) {
			continue
		}
		if p, ok := f.(pluginapi.URLPolicy); ok {
			return p
		}
	}
	return nil
}

// CanonicalizeFeedURL returns the canonical shape of a feed URL under the
// owning plugin's URL policy, or the input unchanged when no plugin owns it.
func (r *Registry) CanonicalizeFeedURL(raw string) string {
	if p := r.URLPolicy(mustParse(raw)); p != nil {
		return p.CanonicalizeFeedURL(raw)
	}
	return raw
}

// FeedToken returns the site token a feed URL represents ("r/cats", "u/sam")
// under the owning plugin's URL policy, or "" when no plugin owns it.
func (r *Registry) FeedToken(feedURL string) string {
	if p := r.URLPolicy(mustParse(feedURL)); p != nil {
		return p.FeedToken(feedURL)
	}
	return ""
}

// MatchFeedAdmin returns the first plugin that manages feeds like u (remote
// settings and lifecycle), along with its Fetcher (for the host factory), or
// (nil, nil). It is gated by CapFeedAdmin and requires a genuine FeedAdmin
// implementation.
//
// Feed management is opt-in. A plugin appears to the host as the gRPC client,
// which satisfies every optional interface unconditionally, so a type assertion
// cannot tell whether the remote plugin implements FeedAdmin. The registry
// trusts Meta.HasFeedAdmin, which the plugin's gRPC server computes from its
// real implementation, plus the plugin's Match for the URL.
func (r *Registry) MatchFeedAdmin(u *url.URL) (pluginapi.FeedAdmin, pluginapi.Fetcher) {
	if u == nil {
		return nil, nil
	}
	for _, f := range r.external {
		if !f.Meta().HasFeedAdmin || !f.Match(u, pluginapi.CapFeedAdmin) {
			continue
		}
		if fa, ok := f.(pluginapi.FeedAdmin); ok {
			return fa, f
		}
	}
	return nil, nil
}

// FeedAdminSettings returns the display-only fields a plugin publishes for a
// feed URL (the inbox address), or nil when no loaded plugin manages it. It is
// pure: the plugin implementing FeedAdmin must not perform network I/O.
func (r *Registry) FeedAdminSettings(feedURL string) []pluginapi.Field {
	fa, _ := r.MatchFeedAdmin(mustParse(feedURL))
	if fa == nil {
		return nil
	}
	return fa.FeedFields(feedURL)
}

// Provisioner is a plugin that can create a feed on a remote service, paired
// with its Fetcher (for the host factory). Label is the create-menu entry.
type Provisioner struct {
	Label string
	P     pluginapi.Provisioner
	F     pluginapi.Fetcher
}

// Provisioners returns the loaded plugins that advertise a create option, in
// registration order.
func (r *Registry) Provisioners() []Provisioner {
	var out []Provisioner
	for _, f := range r.all() {
		label := f.Meta().ProvisionLabel
		if label == "" {
			continue
		}
		p, ok := f.(pluginapi.Provisioner)
		if !ok {
			continue
		}
		out = append(out, Provisioner{Label: label, P: p, F: f})
	}
	return out
}

// SettingsSchema returns the configuration fields a plugin declares, or nil
// when it is not configurable. It is pure: the plugin must not do network I/O.
func (r *Registry) SettingsSchema(name string) []pluginapi.SettingField {
	f := r.ByName(name)
	if f == nil {
		return nil
	}
	c, ok := f.(pluginapi.Configurable)
	if !ok {
		return nil
	}
	return c.Settings()
}

// Configure pushes stored values to one configurable plugin, if it is loaded.
func (r *Registry) Configure(name string, values map[string]string) bool {
	f := r.ByName(name)
	if f == nil {
		return false
	}
	c, ok := f.(pluginapi.Configurable)
	if !ok {
		return false
	}
	c.Configure(values)
	r.clearMinPollCache()
	return true
}

// clearMinPollCache drops memoized per-URL poll floors. It runs when the plugin
// set changes or a plugin is reconfigured, since Match may depend on config.
func (r *Registry) clearMinPollCache() {
	r.minMu.Lock()
	r.minCache = map[string]time.Duration{}
	r.minMu.Unlock()
}

// MinPollInterval returns the largest poll-interval floor the loaded plugins
// require for rawURL, or 0 when none claims one. It is gated by
// CapMinPollInterval, matched on the feed URL, and pure: the result is memoized
// per URL because the poller asks on every successful poll.
func (r *Registry) MinPollInterval(rawURL string) time.Duration {
	if r.Empty() || rawURL == "" {
		return 0
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return 0
	}
	r.minMu.Lock()
	if d, ok := r.minCache[rawURL]; ok {
		r.minMu.Unlock()
		return d
	}
	var floor time.Duration
	for i, f := range r.external {
		d := r.minPoll[i]
		if d <= floor {
			continue
		}
		if f.Match(u, pluginapi.CapMinPollInterval) {
			floor = d
		}
	}
	if r.minCache == nil {
		r.minCache = map[string]time.Duration{}
	}
	r.minCache[rawURL] = floor
	r.minMu.Unlock()
	return floor
}

// ProvisionerByName returns the create-capable plugin with the given name, or
// (nil, nil) when no loaded plugin advertises that name. Names are unique in
// the registry, so this is the form used to honor a create-menu selection.
func (r *Registry) ProvisionerByName(name string) (pluginapi.Provisioner, pluginapi.Fetcher) {
	for _, p := range r.Provisioners() {
		if p.F.Meta().Name == name {
			return p.P, p.F
		}
	}
	return nil, nil
}

// MatchDecorator returns the first plugin that decorates u's items for display,
// along with its Fetcher (for the host factory), or (nil, nil).
func (r *Registry) MatchDecorator(u *url.URL) (pluginapi.Decoration, pluginapi.Fetcher) {
	if u == nil {
		return nil, nil
	}
	for _, f := range r.all() {
		if !f.Match(u, pluginapi.CapDecorate) {
			continue
		}
		if d, ok := f.(pluginapi.Decoration); ok {
			return d, f
		}
	}
	return nil, nil
}

// Docs returns the Markdown documentation a plugin publishes. It returns
// ErrNotFound for an unknown plugin and ErrUnsupportedCapability when the
// plugin has no docs, so a caller can distinguish "no such plugin" from
// "nothing to show".
func (r *Registry) Docs(name string) (string, error) {
	f := r.ByName(name)
	if f == nil {
		return "", ErrNotFound
	}
	d, ok := f.(pluginapi.Docser)
	if !ok {
		return "", pluginapi.ErrUnsupportedCapability
	}
	return d.Docs(), nil
}

// MatchDocs returns the name of the first plugin that documents u, or "" when
// none does. It is how a docs affordance is found for a URL a plugin does not
// own the fetch of.
func (r *Registry) MatchDocs(u *url.URL) string {
	if f := r.Match(u, pluginapi.CapDocs); f != nil {
		return f.Meta().Name
	}
	return ""
}

// CachesImages reports whether any loaded plugin claims host-side image caching
// for feedURL (its images are served with short-lived signed URLs). A feed whose
// plugin claims it is forced on: the user cannot disable caching for that feed.
func (r *Registry) CachesImages(feedURL string) bool {
	if r.Empty() {
		return false
	}
	return r.Match(mustParse(feedURL), pluginapi.CapImageCache) != nil
}

// BypassesProxy reports whether any loaded plugin wants an image URL loaded
// directly instead of through the host's /img proxy. A plugin claims
// CapBypassProxy for a site whose images hotlink freely but block the proxy's
// server-side requests. The default is to proxy.
func (r *Registry) BypassesProxy(rawurl string) bool {
	if rawurl == "" {
		return false
	}
	u, err := url.Parse(rawurl)
	if err != nil || u.Host == "" {
		return false
	}
	return r.Match(u, pluginapi.CapBypassProxy) != nil
}

// Icon returns the built-in source icon a plugin supplies for rawURL, or
// ok=false when no plugin provides one. It is matched on CapIcon.
func (r *Registry) Icon(rawURL string) (mimeType string, data []byte, ok bool) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return "", nil, false
	}
	f := r.Match(u, pluginapi.CapIcon)
	if f == nil {
		return "", nil, false
	}
	ip, ok := f.(pluginapi.IconProvider)
	if !ok {
		return "", nil, false
	}
	mimeType, data = ip.Icon(rawURL)
	if len(data) == 0 {
		return "", nil, false
	}
	return mimeType, data, true
}

// ImageCacheFolder returns the storage folder a feed's cached images live under:
// the owning plugin's name, or "feeds" for a feed with no plugin (a user-enabled
// cache on a generic feed). Admins remove a plugin's cache by dropping
// cache/<folder>/.
func (r *Registry) ImageCacheFolder(feedURL string) string {
	if f := r.Match(mustParse(feedURL), pluginapi.CapImageCache); f != nil {
		if name := f.Meta().Name; name != "" {
			return sanitizeFolder(name)
		}
	}
	return "feeds"
}

// sanitizeFolder keeps a plugin name usable as a single storage path segment,
// mapping anything outside [a-z0-9._-] to '-'.
func sanitizeFolder(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return "feeds"
	}
	return b.String()
}
