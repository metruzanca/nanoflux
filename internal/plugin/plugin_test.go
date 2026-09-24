package plugin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/store"
	"github.com/metruzanca/nanoflux/pluginapi"
)

// fakeFetcher is an in-process plugin for registry tests.
type fakeFetcher struct {
	name     string
	match    func(*url.URL, pluginapi.Capability) bool
	discover func() []pluginapi.Candidate
	result   pluginapi.Result
}

func (f fakeFetcher) Meta() pluginapi.Meta {
	return pluginapi.Meta{Name: f.name, APIVersion: pluginapi.APIVersion}
}
func (f fakeFetcher) Match(u *url.URL, cap pluginapi.Capability) bool {
	if f.match == nil {
		return true
	}
	return f.match(u, cap)
}
func (f fakeFetcher) Discover(context.Context, string, pluginapi.Host) ([]pluginapi.Candidate, error) {
	if f.discover == nil {
		return nil, pluginapi.ErrUnsupportedCapability
	}
	return f.discover(), nil
}
func (f fakeFetcher) Fetch(context.Context, pluginapi.FetchRequest, pluginapi.Host) (pluginapi.Result, error) {
	return f.result, nil
}

func TestRegistryMatchPrecedence(t *testing.T) {
	reg := NewRegistry()
	native := fakeFetcher{name: "native", match: func(*url.URL, pluginapi.Capability) bool { return true }}
	external := fakeFetcher{name: "external", match: func(*url.URL, pluginapi.Capability) bool { return true }}
	reg.RegisterExternal(external)
	reg.RegisterNative(native)

	u, _ := url.Parse("https://example.com")
	if got := reg.Match(u, pluginapi.CapFetch); got == nil || got.Meta().Name != "native" {
		t.Fatalf("native should win, got %v", got)
	}
}

func TestRegistryDiscoverDedup(t *testing.T) {
	reg := NewRegistry()
	reg.RegisterNative(fakeFetcher{name: "a", discover: func() []pluginapi.Candidate {
		return []pluginapi.Candidate{{FeedURL: "https://x/feed", Title: "From A"}}
	}})
	reg.RegisterNative(fakeFetcher{name: "b", discover: func() []pluginapi.Candidate {
		return []pluginapi.Candidate{{FeedURL: "https://x/feed", Title: "From B"}, {FeedURL: "https://y/feed"}}
	}})
	cs := reg.Discover(context.Background(), "https://x", func(pluginapi.Fetcher) pluginapi.Host { return nil })
	if len(cs) != 2 {
		t.Fatalf("expected 2 deduped candidates, got %+v", cs)
	}
	if cs[0].FeedURL != "https://x/feed" || cs[0].Title != "From A" {
		t.Fatalf("first plugin's candidate should win: %+v", cs[0])
	}
}

// TestReconcileFeeds covers auto-disable/re-enable when a feed's plugin comes
// and goes. A plugin that matches example.org owns its feeds; when absent those
// feeds are auto-disabled with a reason, and re-enabled when it returns.
func TestReconcileFeeds(t *testing.T) {
	st, u, a := newPluginStore(t)

	// A feed owned by the "zorg" plugin, and a user-paused feed under it.
	zorgFeed, _ := st.Feeds.CreateWithPlugin(u.ID, a.ID, "z", "https://example.org/z", "", "", "zorg", 900)
	paused, _ := st.Feeds.CreateWithPlugin(u.ID, a.ID, "u", "https://example.org/u", "", "", "zorg", 900)
	st.Feeds.SetEnabled(u.ID, paused.ID, false)

	// Plugin absent: the enabled feed is auto-disabled with a reason; the
	// user-paused one keeps no reason.
	ReconcileFeeds(st, NewRegistry())
	got, _ := st.Feeds.ByID(u.ID, zorgFeed.ID)
	if got.Enabled || got.DisabledReason == "" {
		t.Fatalf("feed should be auto-disabled with a reason: %+v", got)
	}
	if got.PluginName != "zorg" {
		t.Fatalf("plugin name should be preserved: %q", got.PluginName)
	}
	gotPaused, _ := st.Feeds.ByID(u.ID, paused.ID)
	if gotPaused.DisabledReason != "" {
		t.Fatalf("user-paused feed should not gain a reason: %q", gotPaused.DisabledReason)
	}

	// Plugin returns: the auto-disabled feed is re-enabled; the user-paused one
	// stays disabled.
	reg := NewRegistry()
	reg.RegisterNative(fakeFetcher{name: "zorg", match: func(u *url.URL, _ pluginapi.Capability) bool {
		return u != nil && u.Hostname() == "example.org"
	}})
	ReconcileFeeds(st, reg)

	got, _ = st.Feeds.ByID(u.ID, zorgFeed.ID)
	if !got.Enabled || got.DisabledReason != "" {
		t.Fatalf("feed should be auto-re-enabled: %+v", got)
	}
	gotPaused, _ = st.Feeds.ByID(u.ID, paused.ID)
	if gotPaused.Enabled {
		t.Fatal("user-paused feed must stay disabled after reconcile")
	}
}

// TestReconcileAdoptsFeed asserts a feed present before its plugin loaded is
// adopted (plugin_name recorded) when the plugin appears.
func TestReconcileAdoptsFeed(t *testing.T) {
	st, u, a := newPluginStore(t)
	f, _ := st.Feeds.Create(u.ID, a.ID, "z", "https://example.org/z", "", "", 900)
	reg := NewRegistry()
	reg.RegisterNative(fakeFetcher{name: "zorg", match: func(u *url.URL, _ pluginapi.Capability) bool {
		return u != nil && u.Hostname() == "example.org"
	}})
	ReconcileFeeds(st, reg)
	got, _ := st.Feeds.ByID(u.ID, f.ID)
	if got.PluginName != "zorg" {
		t.Fatalf("feed should be adopted by the plugin: %q", got.PluginName)
	}
}

// newPluginStore builds an in-memory store with one user/author.
func newPluginStore(t *testing.T) (*store.Store, store.User, store.Author) {
	t.Helper()
	sqldb, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqldb.Close() })
	if err := db.Migrate(sqldb); err != nil {
		t.Fatal(err)
	}
	st := store.New(sqldb)
	u, _ := st.Users.Create("alice", "h")
	a, _ := st.Authors.Create(u.ID, "A", "", "")
	return st, u, a
}

func TestCooldown(t *testing.T) {
	c := NewCooldown()
	if c.Cooling("https://api.example.com/x") {
		t.Fatal("fresh host should not be cooling")
	}
	c.Cool("https://api.example.com/x", time.Now().Add(time.Hour))
	if !c.Cooling("https://other.example.com/y") {
		t.Fatal("cooldown is per registrable domain")
	}
	c.Cool("https://expired.example.org/x", time.Now().Add(-time.Hour))
	if c.Cooling("https://expired.example.org/x") {
		t.Fatal("expired cooldown should clear")
	}
}

// buildExamplePlugin builds one of the example plugin modules into a temp dir
// and returns the binary path.
func buildExamplePlugin(t *testing.T, dir, name string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = filepath.Join("..", "..", "examples", dir)
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build example plugin %s: %v\n%s", dir, err, out)
	}
	return bin
}

// TestLoadExternalEndToEnd builds the example plugin and loads it over gRPC,
// exercising handshake, version check, dispense, Match, and Fetch through the
// broker, including the host-mediated HTTP path and the item image the feed
// carries (media:thumbnail) surviving the gRPC boundary.
func TestLoadExternalEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a plugin binary")
	}
	// A local upstream serving a YouTube channel feed the plugin fetches
	// through Host.Do.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<?xml version="1.0"?>
<feed xmlns="http://www.w3.org/2005/Atom" xmlns:media="http://search.yahoo.com/mrss/">
<title>External Channel</title><link href="https://www.youtube.com/channel/UCx"/>
<entry><id>yt:video:abc</id><title>Video One</title><link href="https://www.youtube.com/watch?v=abc"/>
<media:group><media:thumbnail url="https://i.ytimg.com/vi/abc/hqdefault.jpg"/></media:group>
<published>2026-01-01T00:00:00Z</published></entry></feed>`))
	}))
	defer upstream.Close()

	bin := buildExamplePlugin(t, "plugin-youtube", "nanoflux-plugin-youtube")

	reg := NewRegistry()
	hosts := NewHosts(upstream.Client(), NewCooldown())
	cleanup := LoadExternal(context.Background(), filepath.Dir(bin), reg, hosts.For)
	defer cleanup()

	if len(reg.Names()) != 1 || reg.Names()[0] != "youtube" {
		t.Fatalf("loaded plugins = %v, want [youtube]", reg.Names())
	}

	u, _ := url.Parse("https://www.youtube.com/feeds/videos.xml?channel_id=UCx")
	f := reg.Match(u, pluginapi.CapFetch)
	if f == nil {
		t.Fatal("youtube plugin should match the channel feed URL")
	}
	// Fetch goes: plugin -> gRPC -> host.Do -> upstream -> back to the plugin.
	res, err := f.Fetch(context.Background(), pluginapi.FetchRequest{
		URL: upstream.URL + "/feeds/videos.xml?channel_id=UCx",
	}, hosts.For(f))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if res.Feed.Title != "External Channel" || len(res.Items) != 1 || res.Items[0].GUID != "yt:video:abc" {
		t.Fatalf("fetch result = %+v", res)
	}
	if got := res.Items[0].ImageURL; got != "https://i.ytimg.com/vi/abc/hqdefault.jpg" {
		t.Fatalf("image = %q, want media:thumbnail to survive gRPC", got)
	}
}

// TestLoadExternalYouTubeExample loads the example that serves the native
// YouTube plugin over gRPC, proving the two forms are the same behaviour: the
// external plugin matches the same URLs as the native one.
func TestLoadExternalYouTubeExample(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a plugin binary")
	}
	bin := buildExamplePlugin(t, "plugin-youtube", "nanoflux-plugin-youtube")

	reg := NewRegistry()
	hosts := NewHosts(http.DefaultClient, NewCooldown())
	cleanup := LoadExternal(context.Background(), filepath.Dir(bin), reg, hosts.For)
	defer cleanup()

	if len(reg.Names()) != 1 || reg.Names()[0] != "youtube" {
		t.Fatalf("loaded plugins = %v, want [youtube]", reg.Names())
	}
	// The external YouTube plugin must match discover on a channel page and
	// fetch on the channel RSS URL, exactly like the native plugin.
	discoverURL, _ := url.Parse("https://www.youtube.com/@SomeChannel")
	if f := reg.Match(discoverURL, pluginapi.CapDiscover); f == nil {
		t.Fatal("external youtube plugin should match discover on a channel page")
	}
	feedURL, _ := url.Parse("https://www.youtube.com/feeds/videos.xml?channel_id=UC5--wS0Ljbin1TjWQX6eafA")
	if f := reg.Match(feedURL, pluginapi.CapFetch); f == nil {
		t.Fatal("external youtube plugin should match the channel feed URL")
	}
}

// TestLoadExternalVersionMismatch is covered by the in-process pluginapi tests;
// here we assert a non-executable file in the dir is ignored.
func TestLoadExternalIgnoresNonExecutable(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hi"), 0o644)
	reg := NewRegistry()
	LoadExternal(context.Background(), dir, reg, NewHosts(http.DefaultClient, NewCooldown()).For)
	if !reg.Empty() {
		t.Fatalf("expected no plugins, got %v", reg.Names())
	}
}

// TestExternalRateLimitParity proves an external plugin sees a rate limit the
// same way a native one does: the host returns it inline (not as a gRPC error),
// so the plugin can turn it into a RateLimit that the poller understands.
func TestExternalRateLimitParity(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a plugin binary")
	}
	limited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer limited.Close()

	bin := buildExamplePlugin(t, "plugin-youtube", "nanoflux-plugin-youtube")

	reg := NewRegistry()
	hosts := NewHosts(limited.Client(), NewCooldown())
	cleanup := LoadExternal(context.Background(), filepath.Dir(bin), reg, hosts.For)
	defer cleanup()

	u, _ := url.Parse("https://www.youtube.com/feeds/videos.xml?channel_id=UCx")
	f := reg.Match(u, pluginapi.CapFetch)
	if f == nil {
		t.Fatal("plugin not matched")
	}
	_, err := f.Fetch(context.Background(), pluginapi.FetchRequest{
		URL: limited.URL + "/feeds/videos.xml?channel_id=UCx",
	}, hosts.For(f))
	var rl *pluginapi.RateLimit
	if !errors.As(err, &rl) {
		t.Fatalf("external plugin should surface a typed RateLimit, got %v", err)
	}
	if rl.RetryAfter <= 0 {
		t.Fatalf("retry-after should be positive: %+v", rl)
	}
}
