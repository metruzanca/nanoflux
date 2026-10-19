// Command plugin-fetch is the minimal external plugin used by the host's gRPC
// tests. It fetches its own feed URL through Host.Do and returns one fixed
// item, so the tests cover the transport without depending on a real site.
package main

import (
	"context"
	"errors"
	"net/url"
	"sync"

	goplugin "github.com/hashicorp/go-plugin"

	"github.com/metruzanca/nanoflux/pluginapi"
)

type fetchPlugin struct {
	mu sync.RWMutex
	ua string
}

func (p *fetchPlugin) Meta() pluginapi.Meta {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return pluginapi.Meta{
		Name:       "fetch-example",
		APIVersion: pluginapi.APIVersion,
		UserAgent:  p.ua,
		Summary:    "minimal external fetch plugin for tests",
	}
}

func (*fetchPlugin) Docs() string { return "# fetch-example\n\nA test fixture plugin.\n" }

func (*fetchPlugin) Settings() []pluginapi.SettingField {
	return []pluginapi.SettingField{{Name: "user_agent", Label: "user agent", Kind: "text"}}
}

func (p *fetchPlugin) Configure(values map[string]string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ua = values["user_agent"]
}

func (*fetchPlugin) Match(u *url.URL, cap pluginapi.Capability) bool {
	if u == nil {
		return false
	}
	switch cap {
	case pluginapi.CapFetch, pluginapi.CapDocs:
		return u.Path == "/feed"
	default:
		return false
	}
}

func (*fetchPlugin) Discover(context.Context, string, pluginapi.Host) ([]pluginapi.Candidate, error) {
	return nil, pluginapi.ErrUnsupportedCapability
}

func (*fetchPlugin) Fetch(ctx context.Context, req pluginapi.FetchRequest, h pluginapi.Host) (pluginapi.Result, error) {
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
	if len(resp.Body) == 0 {
		return pluginapi.Result{}, errors.New("empty response")
	}
	return pluginapi.Result{
		Feed: pluginapi.Feed{Title: "Fetch Example", HomeURL: req.URL},
		Items: []pluginapi.Item{{
			GUID:        "fetch:1",
			Title:       "Hello",
			Link:        "https://example.test/post/1",
			ImageURL:    "https://cdn.example/pic.jpg",
			DurationSec: 42,
		}},
	}, nil
}

func main() {
	goplugin.Serve(&goplugin.ServeConfig{
		HandshakeConfig: pluginapi.Handshake,
		Plugins:         pluginapi.PluginSet(&fetchPlugin{}),
		GRPCServer:      goplugin.DefaultGRPCServer,
	})
}
