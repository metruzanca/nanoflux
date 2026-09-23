// Command nanoflux-plugin-hello is a minimal example feed plugin. It serves a
// fixed feed for URLs on example.com, demonstrating discovery, fetch, and
// host-mediated HTTP. Build it and drop the binary into NF_PLUGINS_DIR:
//
//	go build -o plugins/nanoflux-plugin-hello .
//
// See docs/writing-plugins.md.
package main

import (
	"context"
	"net/url"

	goplugin "github.com/hashicorp/go-plugin"
	"github.com/metruzanca/nanoflux/pluginapi"
)

// Plugin implements pluginapi.Fetcher for example.com.
type Plugin struct{}

var _ pluginapi.Fetcher = Plugin{}

func (Plugin) Meta() pluginapi.Meta {
	return pluginapi.Meta{Name: "hello", APIVersion: pluginapi.APIVersion}
}

func (Plugin) Match(u *url.URL, cap pluginapi.Capability) bool {
	return u != nil && u.Hostname() == "example.com"
}

// Discover advertises one feed for example.com, with preview metadata.
func (Plugin) Discover(_ context.Context, pageURL string, _ pluginapi.Host) ([]pluginapi.Candidate, error) {
	return []pluginapi.Candidate{{
		FeedURL: "https://example.com/feed.xml",
		Title:   "Example",
		IconURL: "https://example.com/favicon.png",
		HomeURL: pageURL,
	}}, nil
}

// Fetch makes a host-mediated request to the feed URL and turns the response
// into one item. It demonstrates the two things a real plugin must do: go
// through h.Do for every request (so the host applies User-Agent, timeouts, and
// rate limiting) and propagate a rate limit so nanoflux can park the feed.
func (Plugin) Fetch(ctx context.Context, req pluginapi.FetchRequest, h pluginapi.Host) (pluginapi.Result, error) {
	h.Logf("fetching %s", req.URL)
	resp, err := h.Do(ctx, pluginapi.HTTPRequest{Method: "GET", URL: req.URL})
	if err != nil {
		return pluginapi.Result{}, err
	}
	if resp.RateLimited {
		return pluginapi.Result{}, &pluginapi.RateLimit{URL: req.URL, Status: resp.Status, RetryAfter: resp.RetryAfter}
	}
	if resp.Status >= 400 {
		return pluginapi.Result{}, &pluginapi.StatusError{Code: resp.Status, URL: req.URL}
	}
	return pluginapi.Result{
		Feed: pluginapi.Feed{Title: "Example", HomeURL: "https://example.com"},
		Items: []pluginapi.Item{{
			GUID:        "hello:1",
			Title:       "Hello from a plugin",
			Link:        "https://example.com/hello",
			Summary:     "This item came from the nanoflux example plugin.",
			PublishedAt: h.Now().Format("2006-01-02 15:04:05"),
		}},
	}, nil
}

func main() {
	goplugin.Serve(&goplugin.ServeConfig{
		HandshakeConfig: pluginapi.Handshake,
		Plugins:         pluginapi.PluginSet(Plugin{}),
		GRPCServer:      goplugin.DefaultGRPCServer,
	})
}
