// Command nanoflux-plugin-enrich is a minimal external plugin that implements
// only the ingest-time Enrich capability. It exists to prove that Enrich crosses
// the gRPC boundary: build and drop it in the plugins directory, and a newly
// stored item whose link host is enrich.example gets a body fetched from that
// page, stored as items.content and rendered in the item modal.
//
// See docs/writing-plugins.md.
package main

import (
	"context"
	"net/url"
	"strings"

	goplugin "github.com/hashicorp/go-plugin"

	"github.com/metruzanca/nanoflux/pluginapi"
)

type Enricher struct{}

var (
	_ pluginapi.Fetcher  = Enricher{}
	_ pluginapi.Enricher = Enricher{}
)

func (Enricher) Meta() pluginapi.Meta {
	return pluginapi.Meta{Name: "enrich-example", APIVersion: pluginapi.APIVersion}
}

func (Enricher) Match(u *url.URL, cap pluginapi.Capability) bool {
	return cap == pluginapi.CapEnrich && u != nil && strings.HasSuffix(u.Hostname(), "enrich.example")
}

func (Enricher) Discover(context.Context, string, pluginapi.Host) ([]pluginapi.Candidate, error) {
	return nil, pluginapi.ErrUnsupportedCapability
}

func (Enricher) Fetch(context.Context, pluginapi.FetchRequest, pluginapi.Host) (pluginapi.Result, error) {
	return pluginapi.Result{}, pluginapi.ErrUnsupportedCapability
}

// Enrich fetches each item's page through the host (so the host still owns the
// User-Agent, timeout, and rate limiting) and returns the raw body as the
// enriched content. A real plugin would extract the article text; this one
// proves the plumbing.
func (Enricher) Enrich(ctx context.Context, req pluginapi.EnrichRequest, h pluginapi.Host) ([]pluginapi.Enriched, error) {
	out := make([]pluginapi.Enriched, 0, len(req.Items))
	for i, it := range req.Items {
		if it.Link == "" {
			continue
		}
		resp, err := h.Do(ctx, pluginapi.HTTPRequest{Method: "GET", URL: it.Link})
		if err != nil || resp.Status >= 400 {
			continue
		}
		out = append(out, pluginapi.Enriched{Index: i, Content: string(resp.Body)})
	}
	return out, nil
}

func main() {
	goplugin.Serve(&goplugin.ServeConfig{
		HandshakeConfig: pluginapi.Handshake,
		Plugins:         pluginapi.PluginSet(Enricher{}),
		GRPCServer:      goplugin.DefaultGRPCServer,
	})
}
