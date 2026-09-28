package plugin

import (
	"context"
	"net/url"

	"github.com/charmbracelet/log"

	"github.com/metruzanca/nanoflux/internal/store"
	"github.com/metruzanca/nanoflux/pluginapi"
)

// Enrich resolves plugin-enriched bodies for freshly stored items and returns
// them keyed by item id. It groups items by the plugin that matches each link
// (a poll can carry several sites) and calls each plugin once per group. It is
// best-effort: an item no plugin matches, or whose plugin errors, is simply
// absent from the result, so enrichment never blocks a poll.
func (d *Dispatcher) Enrich(ctx context.Context, items []store.Item) (map[int64]string, error) {
	if d.reg.Empty() || len(items) == 0 {
		return nil, nil
	}
	type group struct {
		enricher pluginapi.Enricher
		name     string
		fetch    pluginapi.Fetcher
		idx      []int
	}
	groups := map[string]*group{}
	for i := range items {
		u, err := url.Parse(items[i].Link)
		if err != nil || u.Host == "" {
			continue
		}
		en, f := d.reg.MatchEnricher(u)
		if en == nil {
			continue
		}
		key := f.Meta().Name
		g := groups[key]
		if g == nil {
			g = &group{enricher: en, name: key, fetch: f}
			groups[key] = g
		}
		g.idx = append(g.idx, i)
	}

	out := map[int64]string{}
	for _, g := range groups {
		reqItems := make([]pluginapi.Item, 0, len(g.idx))
		for _, i := range g.idx {
			reqItems = append(reqItems, toPluginItem(items[i]))
		}
		enriched, err := g.enricher.Enrich(ctx, pluginapi.EnrichRequest{Items: reqItems}, d.hosts(g.fetch))
		if err != nil {
			if err == pluginapi.ErrUnsupportedCapability {
				continue
			}
			log.Warn("plugin enrich failed", "plugin", g.name, "err", err)
			continue
		}
		for _, e := range enriched {
			if e.Index < 0 || e.Index >= len(g.idx) || e.Content == "" {
				continue
			}
			out[items[g.idx[e.Index]].ID] = e.Content
		}
	}
	return out, nil
}
