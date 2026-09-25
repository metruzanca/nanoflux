// Command nanoflux-plugin-render is a minimal external plugin that implements
// only the view-time Render capability. It exists to prove that Render crosses
// the gRPC boundary: build and drop it in the plugins directory, and items
// whose link host is render.example get a source link, an embed, and a gallery.
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

type Renderer struct{}

var (
	_ pluginapi.Fetcher  = Renderer{}
	_ pluginapi.Renderer = Renderer{}
)

func (Renderer) Meta() pluginapi.Meta {
	return pluginapi.Meta{Name: "render-example", APIVersion: pluginapi.APIVersion}
}

func (Renderer) Match(u *url.URL, cap pluginapi.Capability) bool {
	return cap == pluginapi.CapRender && u != nil && strings.HasSuffix(u.Hostname(), "render.example")
}

func (Renderer) Discover(context.Context, string, pluginapi.Host) ([]pluginapi.Candidate, error) {
	return nil, pluginapi.ErrUnsupportedCapability
}

func (Renderer) Fetch(context.Context, pluginapi.FetchRequest, pluginapi.Host) (pluginapi.Result, error) {
	return pluginapi.Result{}, pluginapi.ErrUnsupportedCapability
}

func (Renderer) Render(context.Context, pluginapi.RenderRequest, pluginapi.Host) (pluginapi.Media, error) {
	return pluginapi.Media{
		SourceURL: "https://external.example/page",
		EmbedSrc:  "https://player.example/embed/1",
		Gallery:   []string{"https://cdn.example/1.jpg", "https://cdn.example/2.jpg"},
	}, nil
}

func main() {
	goplugin.Serve(&goplugin.ServeConfig{
		HandshakeConfig: pluginapi.Handshake,
		Plugins:         pluginapi.PluginSet(Renderer{}),
		GRPCServer:      goplugin.DefaultGRPCServer,
	})
}
