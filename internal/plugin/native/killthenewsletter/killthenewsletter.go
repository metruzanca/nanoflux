// Package killthenewsletter is the native plugin for Kill the Newsletter, a
// service that turns email newsletters into Atom feeds.
//
// The service's own API is plain HTTP forms with no accounts: creating a feed
// returns an inbox address ({publicId}@{host}) and an Atom URL
// ({host}/feeds/{publicId}.xml); that URL is the only credential. The Atom feeds
// are standard and are read by nanoflux's generic parser, so this plugin does
// NOT claim CapFetch (feeds.plugin_name stays empty, exactly like reddit).
//
// Instead it owns the service-specific behavior through URL-matched
// capabilities:
//
//   - URL policy: the redirect-free canonical feed shape and the publicId token
//     (urlpolicy.go), plus a derived feed from a feed's own web page.
//   - Provisioning: create a new remote feed server-side (a POST /feeds), so a
//     user never leaves nanoflux to get an inbox address.
//   - Feed administration: surface the subscribe address and manage the remote
//     feed (sync its title, delete it) from the feed's page.
//
// The instance is configurable: the admin sets a base URL for the official
// service or a self-hosted one, and Configure caches it so Match (which runs
// without a Host) can recognize the instance's URLs.
package killthenewsletter

import (
	"context"
	_ "embed"
	"errors"
	"net/url"
	"strings"
	"sync"

	"github.com/metruzanca/nanoflux/pluginapi"
)

// Name is the plugin's stable identifier.
const Name = "killthenewsletter"

// defaultBaseURL is the public Kill the Newsletter origin, used until the admin
// configures a different one.
const defaultBaseURL = "https://kill-the-newsletter.com"

// readme is the plugin's Markdown documentation, shown from the admin plugin
// card and from a feed's edit page.
//
//go:embed readme.md
var readme string

// Plugin implements the Kill the Newsletter integration. It holds the configured
// instance base URL, since Match runs without a Host and must recognize the
// instance's URLs from configuration alone.
type Plugin struct {
	mu   sync.RWMutex
	base string // canonical origin, e.g. "https://kill-the-newsletter.com"
}

var (
	_ pluginapi.Fetcher      = (*Plugin)(nil)
	_ pluginapi.Provisioner  = (*Plugin)(nil)
	_ pluginapi.FeedAdmin    = (*Plugin)(nil)
	_ pluginapi.URLPolicy    = (*Plugin)(nil)
	_ pluginapi.Configurable = (*Plugin)(nil)
)

// New returns a plugin pointed at the public instance. The host replaces the
// base URL through Configure when an admin sets one.
func New() *Plugin { return &Plugin{base: defaultBaseURL} }

func (*Plugin) Meta() pluginapi.Meta {
	return pluginapi.Meta{
		Name:           Name,
		APIVersion:     pluginapi.APIVersion,
		Summary:        "Kill the Newsletter: create newsletter inboxes and manage their feeds without leaving nanoflux",
		ProvisionLabel: "newsletter",
	}
}

// Docs returns this plugin's Markdown documentation.
func (*Plugin) Docs() string { return readme }

// Settings declares the instance this plugin talks to. An empty base URL means
// the public instance, so the default works without configuration.
func (*Plugin) Settings() []pluginapi.SettingField {
	return []pluginapi.SettingField{{
		Name:        "base_url",
		Label:       "instance url",
		Kind:        "url",
		Placeholder: defaultBaseURL,
		Help:        "Base URL of the Kill the Newsletter instance to use. Leave empty for the public instance, or set your own self-hosted instance.",
	}}
}

// Configure caches the configured instance base URL. An empty or unparseable
// value falls back to the public instance.
func (p *Plugin) Configure(values map[string]string) {
	raw := strings.TrimSpace(values["base_url"])
	p.mu.Lock()
	defer p.mu.Unlock()
	p.base = normalizeBase(raw)
}

// baseURL returns the cached instance origin.
func (p *Plugin) baseURL() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.base
}

// normalizeBase turns a configured value into a canonical origin, defaulting to
// the public instance when empty or invalid.
func normalizeBase(raw string) string {
	if raw == "" {
		return defaultBaseURL
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return defaultBaseURL
	}
	return u.Scheme + "://" + u.Host
}

// host is the instance hostname without a scheme.
func (p *Plugin) host() string {
	u, err := url.Parse(p.baseURL())
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// Match handles the configured instance for provisioning, feed administration,
// discovery, URL policy and docs. It does not claim fetch: the service's Atom
// feeds are standard feeds read by the generic parser.
func (p *Plugin) Match(u *url.URL, cap pluginapi.Capability) bool {
	if u != nil && !p.isHost(u.Hostname()) {
		return false
	}
	switch cap {
	case pluginapi.CapProvision, pluginapi.CapFeedAdmin, pluginapi.CapDiscover,
		pluginapi.CapURLPolicy, pluginapi.CapDocs:
		return true
	default:
		return false
	}
}

// Discover derives a feed URL from a feed page URL ({base}/feeds/{publicId})
// with no request. The service rate-limits and the feed URL follows directly
// from the page, so the candidate is marked Derived.
func (p *Plugin) Discover(_ context.Context, pageURL string, _ pluginapi.Host) ([]pluginapi.Candidate, error) {
	c, ok := p.deriveFeed(pageURL)
	if !ok {
		return nil, pluginapi.ErrUnsupportedCapability
	}
	return []pluginapi.Candidate{c}, nil
}

// Fetch is unsupported: the service's Atom feeds are read by the generic parser.
func (*Plugin) Fetch(context.Context, pluginapi.FetchRequest, pluginapi.Host) (pluginapi.Result, error) {
	return pluginapi.Result{}, pluginapi.ErrUnsupportedCapability
}

// CanonicalizeFeedURL rewrites a configured-instance URL to the shape it serves
// without a redirect: https://{host}/feeds/{publicId}.xml.
func (p *Plugin) CanonicalizeFeedURL(raw string) string { return p.canonicalFeedURL(raw) }

// FeedToken returns the publicId a feed URL represents, which is also the local
// part of the feed's inbox address.
func (p *Plugin) FeedToken(feedURL string) string { return p.feedToken(feedURL) }

// FeedFields returns the feed's subscribe address as a display-only field,
// derived from the URL. It performs no network I/O.
func (p *Plugin) FeedFields(feedURL string) []pluginapi.Field {
	id, ok := p.publicIDFromFeedURL(feedURL)
	if !ok {
		return nil
	}
	return []pluginapi.Field{{
		Name:  "email",
		Label: "subscribe this address to a newsletter",
		Value: id + "@" + p.host(),
		Kind:  "email",
	}}
}

// Provision creates a new remote feed (a POST /feeds) and returns its Atom URL,
// web page and inbox address. The request carries the csrf-protection header the
// service requires for a non-GET; there is no account, so any caller may create
// a feed.
func (p *Plugin) Provision(ctx context.Context, req pluginapi.ProvisionRequest, h pluginapi.Host) (pluginapi.Provisioned, error) {
	title := strings.TrimSpace(req.Title)
	if title == "" {
		return pluginapi.Provisioned{}, errors.New("a title is required")
	}
	createURL := p.baseURL() + "/feeds"
	resp, err := h.Do(ctx, pluginapi.HTTPRequest{
		Method: "POST",
		URL:    createURL,
		Headers: map[string]string{
			"Content-Type":    "application/x-www-form-urlencoded",
			"Accept":          "application/json",
			"csrf-protection": "true",
		},
		Body: []byte("title=" + url.QueryEscape(title)),
	})
	if err != nil {
		return pluginapi.Provisioned{}, err
	}
	if err := actionError(resp, createURL); err != nil {
		return pluginapi.Provisioned{}, err
	}
	id, email, feedURL := parseProvisionResponse(resp.Body)
	if id == "" {
		return pluginapi.Provisioned{}, errors.New("unexpected response from kill the newsletter")
	}
	if email == "" {
		email = id + "@" + p.host()
	}
	if feedURL == "" {
		feedURL = p.feedURLFor(id)
	}
	return pluginapi.Provisioned{
		FeedURL: feedURL,
		Title:   title,
		HomeURL: p.homeURLFor(id),
		Fields: []pluginapi.Field{{
			Name: "email", Label: "subscribe this address to a newsletter", Value: email, Kind: "email",
		}},
	}, nil
}

// Action performs a remote management action on a feed.
//
// "save" updates the remote feed's title and icon from the submitted fields
// (the service requires both on a PATCH); "delete" removes the remote feed.
func (p *Plugin) Action(ctx context.Context, req pluginapi.FeedActionRequest, h pluginapi.Host) (pluginapi.FeedActionResult, error) {
	id, ok := p.publicIDFromFeedURL(req.FeedURL)
	if !ok {
		return pluginapi.FeedActionResult{}, errors.New("not a kill the newsletter feed")
	}
	switch req.Action {
	case "save":
		form := url.Values{}
		form.Set("title", strings.TrimSpace(req.Fields["title"]))
		form.Set("icon", strings.TrimSpace(req.Fields["icon"]))
		u := p.feedSettingsURL(id)
		resp, err := h.Do(ctx, pluginapi.HTTPRequest{
			Method: "PATCH",
			URL:    u,
			Headers: map[string]string{
				"Content-Type":    "application/x-www-form-urlencoded",
				"csrf-protection": "true",
			},
			Body: []byte(form.Encode()),
		})
		if err != nil {
			return pluginapi.FeedActionResult{}, err
		}
		return pluginapi.FeedActionResult{}, actionError(resp, u)
	case "delete":
		u := p.feedSettingsURL(id)
		resp, err := h.Do(ctx, pluginapi.HTTPRequest{
			Method:  "DELETE",
			URL:     u,
			Headers: map[string]string{"csrf-protection": "true"},
		})
		if err != nil {
			return pluginapi.FeedActionResult{}, err
		}
		if err := actionError(resp, u); err != nil {
			return pluginapi.FeedActionResult{}, err
		}
		return pluginapi.FeedActionResult{
			Message: "feed deleted on Kill the Newsletter",
			Deleted: true,
		}, nil
	default:
		return pluginapi.FeedActionResult{}, errors.New("unsupported action")
	}
}

// actionError maps a management response to a typed error, or nil on success.
// A rate limit is surfaced so the host can park the interaction.
func actionError(resp pluginapi.HTTPResponse, url string) error {
	if resp.RateLimited {
		return &pluginapi.RateLimit{URL: url, Status: resp.Status, RetryAfter: resp.RetryAfter}
	}
	if resp.Status >= 400 {
		return &pluginapi.StatusError{Code: resp.Status, URL: url}
	}
	return nil
}

// feedSettingsURL is the web page that also accepts PATCH/DELETE.
func (p *Plugin) feedSettingsURL(id string) string { return p.baseURL() + "/feeds/" + id }

// feedURLFor is the Atom feed URL for a publicId.
func (p *Plugin) feedURLFor(id string) string { return p.baseURL() + "/feeds/" + id + ".xml" }

// homeURLFor is the feed's web page for a publicId.
func (p *Plugin) homeURLFor(id string) string { return p.baseURL() + "/feeds/" + id }

// isHost reports whether host is the configured instance host (with or without
// a www prefix).
func (p *Plugin) isHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	want := strings.ToLower(p.host())
	if want == "" {
		return false
	}
	return host == want || host == "www."+want
}

// isPublicID reports whether s is a Kill the Newsletter publicId: a non-empty
// run of ASCII letters and digits (the service mints 20-char lowercase strings).
func isPublicID(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return false
		}
	}
	return true
}
