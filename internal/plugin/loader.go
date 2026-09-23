package plugin

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/charmbracelet/log"
	goplugin "github.com/hashicorp/go-plugin"

	"github.com/metruzanca/nanoflux/pluginapi"
)

// LoadExternal scans dir for plugin executables and loads each over gRPC,
// registering the ones whose handshake and API version succeed. It returns a
// cleanup func that kills every loaded plugin subprocess.
//
// A plugin that fails to start, handshakes wrong, reports an incompatible
// APIVersion, or does not implement Fetcher is logged and skipped — a broken
// plugin never prevents nanoflux from starting.
func LoadExternal(ctx context.Context, dir string, reg *Registry, hosts func(pluginapi.Fetcher) pluginapi.Host) func() {
	if dir == "" {
		return func() {}
	}
	_ = ctx
	_ = hosts
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Warn("plugins dir unreadable; skipping", "dir", dir, "err", err)
		}
		return func() {}
	}

	var clients []*goplugin.Client
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		info, err := e.Info()
		if err != nil || info.Mode()&0o111 == 0 {
			continue // not executable
		}
		f, client, err := loadOne(ctx, path)
		if err != nil {
			log.Warn("plugin load failed", "path", path, "err", err)
			continue
		}
		if client != nil {
			clients = append(clients, client)
		}
		reg.RegisterExternal(f)
	}

	return func() {
		for _, c := range clients {
			c.Kill()
		}
	}
}

// loadOne starts one plugin subprocess and dispenses its Fetcher.
func loadOne(ctx context.Context, path string) (pluginapi.Fetcher, *goplugin.Client, error) {
	// go-plugin's handshake cookie proves this process was launched by the host
	// and keeps a plugin from doing work if run by hand.
	cmd := exec.Command(path)
	cmd.Env = os.Environ()
	client := goplugin.NewClient(&goplugin.ClientConfig{
		HandshakeConfig:  pluginapi.Handshake,
		Plugins:          pluginapi.PluginSet(nil),
		Cmd:              cmd,
		AllowedProtocols: []goplugin.Protocol{goplugin.ProtocolGRPC},
		Managed:          true,
		StartTimeout:     10 * time.Second,
		// The plugin's own logs are forwarded through the host; suppress
		// go-plugin's mirrored stderr to avoid double-printing.
		SyncStderr: nil,
	})
	proto, err := client.Client()
	if err != nil {
		client.Kill()
		return nil, nil, err
	}
	raw, err := proto.Dispense(pluginapi.PluginKey)
	if err != nil {
		client.Kill()
		return nil, nil, err
	}
	f, ok := raw.(pluginapi.Fetcher)
	if !ok {
		client.Kill()
		return nil, nil, errNotAFetcher
	}
	m := f.Meta()
	if m.Name == "" {
		client.Kill()
		return nil, nil, errNoName
	}
	if m.APIVersion != pluginapi.APIVersion {
		client.Kill()
		return nil, nil, errIncompatibleAPI(m.APIVersion)
	}
	return f, client, nil
}
