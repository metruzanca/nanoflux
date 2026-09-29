package httpapi

import (
	"context"

	"github.com/charmbracelet/log"

	"github.com/metruzanca/nanoflux/internal/store"
	"github.com/metruzanca/nanoflux/pluginapi"
)

// enclosuresToPlugin adapts stored enclosures to the plugin API model so a
// view-time renderer can see the item's media.
func enclosuresToPlugin(encs []store.Enclosure) []pluginapi.Enclosure {
	if len(encs) == 0 {
		return nil
	}
	out := make([]pluginapi.Enclosure, 0, len(encs))
	for _, e := range encs {
		out = append(out, pluginapi.Enclosure{
			URL: e.URL, MIMEType: e.MIMEType, Length: e.Size,
			Kind: e.Kind, Poster: e.Poster, Title: e.Title,
		})
	}
	return out
}

// resolveItemPlugins asks a view-time rendering plugin to resolve an item's
// media: an external destination, an embeddable player, or a multi-image
// gallery — content reddit marks only in the item's HTML that a stored Item
// cannot carry. When no plugin handles the item's link, every field is left
// empty and the item's stored content renders as-is.
func (s *Server) resolveItemPlugins(ctx context.Context, it itemViewData) (source, embedSrc string, gallery []string) {
	if s.plugins == nil || s.plugins.Empty() {
		return "", "", nil
	}
	m, err := s.plugins.RenderItem(ctx, pluginapi.RenderRequest{
		GUID:       it.GUID,
		Link:       it.Link,
		Summary:    it.Summary,
		ImageURL:   it.ImageURL,
		Kind:       pluginapi.ItemKind(it.Kind),
		Categories: it.Categories,
		Enclosures: enclosuresToPlugin(it.Enclosures),
	}, s.pluginHosts.For)
	if err != nil {
		log.Error("item render", "link", it.Link, "err", err)
		return "", "", nil
	}
	return m.SourceURL, m.EmbedSrc, m.Gallery
}
