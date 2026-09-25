package httpapi

import (
	"context"

	"github.com/charmbracelet/log"

	"github.com/metruzanca/nanoflux/pluginapi"
)

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
		Link:     it.Link,
		Summary:  it.Summary,
		ImageURL: it.ImageURL,
	}, s.pluginHosts.For)
	if err != nil {
		log.Error("item render", "link", it.Link, "err", err)
		return "", "", nil
	}
	return m.SourceURL, m.EmbedSrc, m.Gallery
}
