package plugin

import (
	"context"
	"net/url"

	"github.com/charmbracelet/log"

	"github.com/metruzanca/nanoflux/internal/store"
	"github.com/metruzanca/nanoflux/pluginapi"
)

// StoreDecorator adapts the plugin Registry to the store's ItemDecorator
// interface, so lists can render plugin-owned source attribution and card kinds.
// It groups a page's items by the plugin that matches each item's link (a page
// can mix sites) and calls each plugin once per group. Decorate performs no
// network I/O, so no Host is involved.
type StoreDecorator struct {
	reg *Registry
}

// NewStoreDecorator returns a store decorator over reg.
func NewStoreDecorator(reg *Registry) StoreDecorator {
	return StoreDecorator{reg: reg}
}

// Decorate decorates a page's items, returning a map keyed by item id.
func (d StoreDecorator) Decorate(items []store.ItemWithFeed) (map[int64]store.RawDecoration, error) {
	if d.reg == nil || d.reg.Empty() || len(items) == 0 {
		return nil, nil
	}
	// Group item indices by the plugin that matches their link. Items with no
	// matching plugin are left undecorated.
	type group struct {
		plugin pluginapi.Decoration
		name   string
		idx    []int
	}
	groups := map[string]*group{}
	for i := range items {
		u, err := url.Parse(items[i].Link)
		if err != nil || u.Host == "" {
			continue
		}
		pl, f := d.reg.MatchDecorator(u)
		if pl == nil {
			continue
		}
		key := f.Meta().Name
		g := groups[key]
		if g == nil {
			g = &group{plugin: pl, name: key}
			groups[key] = g
		}
		g.idx = append(g.idx, i)
	}

	out := map[int64]store.RawDecoration{}
	for _, g := range groups {
		reqItems := make([]pluginapi.Item, 0, len(g.idx))
		for _, i := range g.idx {
			reqItems = append(reqItems, toPluginItem(items[i].Item))
		}
		decs, err := g.plugin.Decorate(context.Background(), pluginapi.DecorateRequest{Items: reqItems})
		if err != nil {
			if err == pluginapi.ErrUnsupportedCapability {
				continue
			}
			log.Warn("plugin decorate failed", "plugin", g.name, "err", err)
			continue
		}
		for _, dec := range decs {
			if dec.Index < 0 || dec.Index >= len(g.idx) {
				continue
			}
			out[items[g.idx[dec.Index]].ID] = store.RawDecoration{
				Kind:        store.ItemKind(dec.Kind),
				Attribution: toRawAttribution(dec.Attribution),
				ThumbURL:    dec.ThumbURL,
			}
		}
	}
	return out, nil
}

func toRawAttribution(parts []pluginapi.SourcePart) []store.RawAttributionPart {
	out := make([]store.RawAttributionPart, 0, len(parts))
	for _, p := range parts {
		out = append(out, store.RawAttributionPart{Text: p.Text, Token: p.Token, URL: p.URL})
	}
	return out
}

// toPluginItem converts a stored item to the pluginapi model for decoration.
func toPluginItem(it store.Item) pluginapi.Item {
	return pluginapi.Item{
		GUID:        it.GUID,
		Title:       it.Title,
		Link:        it.Link,
		Summary:     it.Summary,
		ImageURL:    it.ImageURL,
		PublishedAt: it.PublishedAt,
		Categories:  it.Categories,
	}
}
