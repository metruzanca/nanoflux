// Command nanoflux-plugin-youtube serves nanoflux's native YouTube plugin as an
// external (out-of-process) plugin.
//
// It is, by design, *identical* to the native YouTube plugin that is compiled
// into nanoflux (`internal/plugin/native/youtube`): the whole implementation is
// the `youtube.Plugin` value served below. The only difference between the two
// is packaging — the native form is registered in-process (`registerNative` in
// `internal/plugin/setup.go`), while this form is a standalone executable that
// nanoflux loads over gRPC from NF_PLUGINS_DIR.
//
// At the time of writing the two are the same code, and they must stay that way:
// this example imports the native package directly rather than copying it, so
// there is no second implementation to drift. A plugin authored in a *separate
// repo* cannot import `internal/...`; it would vendor/move the implementation and
// keep importing only `pluginapi`.
//
// Build and drop it into the plugins directory:
//
//	go build -o plugins/nanoflux-plugin-youtube .
//
// See docs/writing-plugins.md.
package main

import (
	goplugin "github.com/hashicorp/go-plugin"

	"github.com/metruzanca/nanoflux/internal/plugin/native/youtube"
	"github.com/metruzanca/nanoflux/pluginapi"
)

func main() {
	goplugin.Serve(&goplugin.ServeConfig{
		HandshakeConfig: pluginapi.Handshake,
		Plugins:         pluginapi.PluginSet(youtube.Plugin{}),
		GRPCServer:      goplugin.DefaultGRPCServer,
	})
}
