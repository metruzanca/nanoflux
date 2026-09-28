// Package killthenewsletter is the native plugin for kill-the-newsletter.com,
// a service that turns email newsletters into Atom feeds.
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
// Host is configurable (NF_KTN_HOST) so a self-hosted Kill the Newsletter works
// the same way.
package killthenewsletter

import (
	"context"
	_ "embed"
	"errors"
	"net/url"
	"os"
	"strings"

	"github.com/metruzanca/nanoflux/pluginapi"
)

// Name is the plugin's stable identifier.
const Name = "killthenewsletter"

// readme is the plugin's Markdown documentation, shown from the admin plugin
// card and from a feed's edit page.
//
//go:embed readme.md
var readme string

// Host is the Kill the Newsletter host this plugin talks to, without a scheme.
// It defaults to the public service and can be pointed at a self-hosted instance
// with NF_KTN_HOST. A var so tests can point it at a mock host.
var Host = defaultHost()

func defaultHost() string {
	if h := strings.TrimSpace(os.Getenv("NF_KTN_HOST")); h != "" {
		return strings.TrimPrefix(strings.TrimPrefix(h, "https://"), "http://")
	}
	return "kill-the-newsletter.com"
}

// Plugin implements the Kill the Newsletter integration.
type Plugin struct{}

var (
	_ pluginapi.Fetcher     = Plugin{}
	_ pluginapi.Provisioner = Plugin{}
	_ pluginapi.FeedAdmin   = Plugin{}
	_ pluginapi.URLPolicy   = Plugin{}
)

func (Plugin) Meta() pluginapi.Meta {
	return pluginapi.Meta{
		Name:           Name,
		APIVersion:     pluginapi.APIVersion,
		Summary:        "Kill the Newsletter: create newsletter inboxes and manage their feeds without leaving nanoflux",
		ProvisionLabel: "newsletter (Kill the Newsletter)",
	}
}

// Docs returns this plugin's Markdown documentation.
func (Plugin) Docs() string { return readme }

// Match handles the Kill the Newsletter host for provisioning, feed
// administration, discovery, URL policy and docs. It does not claim fetch: the
// service's Atom feeds are standard feeds read by the generic parser.
func (Plugin) Match(u *url.URL, cap pluginapi.Capability) bool {
	if u != nil && !isKTNHost(u.Hostname()) {
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

// Discover derives a feed URL from a Kill the Newsletter feed page URL
// ({host}/feeds/{publicId}) with no request. The public service rate-limits and
// the feed URL follows directly from the page, so the candidate is marked
// Derived and the host does not fetch the page.
func (Plugin) Discover(_ context.Context, pageURL string, _ pluginapi.Host) ([]pluginapi.Candidate, error) {
	c, ok := deriveFeed(pageURL)
	if !ok {
		return nil, pluginapi.ErrUnsupportedCapability
	}
	return []pluginapi.Candidate{c}, nil
}

// Fetch is unsupported: the service's Atom feeds are read by the generic parser.
func (Plugin) Fetch(context.Context, pluginapi.FetchRequest, pluginapi.Host) (pluginapi.Result, error) {
	return pluginapi.Result{}, pluginapi.ErrUnsupportedCapability
}

// CanonicalizeFeedURL rewrites a Kill the Newsletter URL to the shape the
// service serves without a redirect: https://{host}/feeds/{publicId}.xml.
func (Plugin) CanonicalizeFeedURL(raw string) string { return canonicalFeedURL(raw) }

// FeedToken returns the publicId a feed URL represents, which is also the local
// part of the feed's inbox address.
func (Plugin) FeedToken(feedURL string) string { return feedToken(feedURL) }

// Settings returns the feed's subscribe address as a display-only field, derived
// from the URL. It performs no network I/O.
func (Plugin) Settings(feedURL string) []pluginapi.Field {
	id, ok := publicIDFromFeedURL(feedURL)
	if !ok {
		return nil
	}
	return []pluginapi.Field{{
		Name:  "email",
		Label: "subscribe this address to a newsletter",
		Value: id + "@" + Host,
		Kind:  "email",
	}}
}

// Provision creates a new remote feed (a POST /feeds) and returns its Atom URL,
// web page and inbox address. The request carries the csrf-protection header the
// service requires for a non-GET; there is no account, so any caller may create
// a feed.
func (Plugin) Provision(ctx context.Context, req pluginapi.ProvisionRequest, h pluginapi.Host) (pluginapi.Provisioned, error) {
	title := strings.TrimSpace(req.Title)
	if title == "" {
		return pluginapi.Provisioned{}, errors.New("a title is required")
	}
	body := "title=" + url.QueryEscape(title)
	resp, err := h.Do(ctx, pluginapi.HTTPRequest{
		Method: "POST",
		URL:    baseURL() + "/feeds",
		Headers: map[string]string{
			"Content-Type":    "application/x-www-form-urlencoded",
			"Accept":          "application/json",
			"csrf-protection": "true",
		},
		Body: []byte(body),
	})
	if err != nil {
		return pluginapi.Provisioned{}, err
	}
	if resp.RateLimited {
		return pluginapi.Provisioned{}, &pluginapi.RateLimit{URL: baseURL() + "/feeds", Status: resp.Status, RetryAfter: resp.RetryAfter}
	}
	if resp.Status >= 400 {
		return pluginapi.Provisioned{}, &pluginapi.StatusError{Code: resp.Status, URL: baseURL() + "/feeds"}
	}
	id, email, feedURL := parseProvisionResponse(resp.Body)
	if id == "" {
		return pluginapi.Provisioned{}, errors.New("unexpected response from kill the newsletter")
	}
	if email == "" {
		email = id + "@" + Host
	}
	if feedURL == "" {
		feedURL = feedURLFor(id)
	}
	return pluginapi.Provisioned{
		FeedURL: feedURL,
		Title:   title,
		HomeURL: homeURLFor(id),
		Fields: []pluginapi.Field{{
			Name: "email", Label: "subscribe this address to a newsletter", Value: email, Kind: "email",
		}},
	}, nil
}

// Action performs a remote management action on a feed.
//
// "save" updates the remote feed's title and icon from the submitted fields
// (the service requires both on a PATCH); "delete" removes the remote feed.
func (Plugin) Action(ctx context.Context, req pluginapi.FeedActionRequest, h pluginapi.Host) (pluginapi.FeedActionResult, error) {
	id, ok := publicIDFromFeedURL(req.FeedURL)
	if !ok {
		return pluginapi.FeedActionResult{}, errors.New("not a kill the newsletter feed")
	}
	switch req.Action {
	case "save":
		form := url.Values{}
		form.Set("title", strings.TrimSpace(req.Fields["title"]))
		form.Set("icon", strings.TrimSpace(req.Fields["icon"]))
		resp, err := h.Do(ctx, pluginapi.HTTPRequest{
			Method: "PATCH",
			URL:    feedSettingsURL(id),
			Headers: map[string]string{
				"Content-Type":    "application/x-www-form-urlencoded",
				"csrf-protection": "true",
			},
			Body: []byte(form.Encode()),
		})
		if err != nil {
			return pluginapi.FeedActionResult{}, err
		}
		return pluginapi.FeedActionResult{}, actionError(resp, feedSettingsURL(id))
	case "delete":
		resp, err := h.Do(ctx, pluginapi.HTTPRequest{
			Method:  "DELETE",
			URL:     feedSettingsURL(id),
			Headers: map[string]string{"csrf-protection": "true"},
		})
		if err != nil {
			return pluginapi.FeedActionResult{}, err
		}
		if err := actionError(resp, feedSettingsURL(id)); err != nil {
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

// baseURL is the https origin for the configured host.
func baseURL() string { return "https://" + Host }

// feedSettingsURL is the web page that also accepts PATCH/DELETE.
func feedSettingsURL(id string) string { return baseURL() + "/feeds/" + id }

// feedURLFor is the Atom feed URL for a publicId.
func feedURLFor(id string) string { return baseURL() + "/feeds/" + id + ".xml" }

// homeURLFor is the feed's web page for a publicId.
func homeURLFor(id string) string { return baseURL() + "/feeds/" + id }

// isKTNHost reports whether host is the configured Kill the Newsletter host
// (with or without a www prefix).
func isKTNHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	want := strings.ToLower(Host)
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
