package pluginapi

import (
	goplugin "github.com/hashicorp/go-plugin"
)

// Handshake is the go-plugin handshake shared by the host and every plugin.
// ProtocolVersion is the go-plugin wire version (bump for a wire-breaking
// change); MagicCookie proves the process was launched by nanoflux and not run
// by a person.
var Handshake = goplugin.HandshakeConfig{
	ProtocolVersion:  1,
	MagicCookieKey:   "NANOFLUX_PLUGIN",
	MagicCookieValue: "nanoflux-plugin-v1",
}

// PluginKey is the key under which the single Fetcher plugin is served and
// dispensed.
const PluginKey = "fetcher"

// PluginSet wraps a Fetcher as the go-plugin PluginSet a host dispenses and a
// plugin serves. Both native (in-process) and external plugins use it.
func PluginSet(f Fetcher) goplugin.PluginSet {
	return goplugin.PluginSet{
		PluginKey: &fetcherPlugin{impl: f},
	}
}
