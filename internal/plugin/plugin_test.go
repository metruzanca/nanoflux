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

// TestLoadExternalEndToEnd builds the example plugin and loads it over gRPC,
// exercising handshake, version check, dispense, Discover, Fetch, and the
// host-mediated HTTP path (including rate-limit propagation) through the broker.
func TestLoadExternalEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a plugin binary")
	}
	// A local upstream the plugin fetches through Host.Do.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html><body>ok</body></html>"))
	}))
	defer upstream.Close()

	// Build the example plugin into a temp dir.
	bin := filepath.Join(t.TempDir(), "nanoflux-plugin-hello")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = filepath.Join("..", "..", "examples", "plugin-hello")
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build example plugin: %v\n%s", err, out)
	}

	reg := NewRegistry()
	hosts := NewHosts(upstream.Client(), NewCooldown())
	cleanup := LoadExternal(context.Background(), filepath.Dir(bin), reg, hosts.For)
	defer cleanup()

	if len(reg.Names()) != 1 || reg.Names()[0] != "hello" {
		t.Fatalf("loaded plugins = %v, want [hello]", reg.Names())
	}

	u, _ := url.Parse("https://example.com/profile")
	f := reg.Match(u, pluginapi.CapFetch)
	if f == nil {
		t.Fatal("hello plugin should match example.com")
	}
	// Fetch goes: plugin -> gRPC -> host.Do -> upstream -> back to the plugin.
	res, err := f.Fetch(context.Background(), pluginapi.FetchRequest{URL: upstream.URL}, hosts.For(f))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].GUID != "hello:1" {
		t.Fatalf("fetch result = %+v", res)
	}

	cs, err := f.Discover(context.Background(), u.String(), hosts.For(f))
	if err != nil || len(cs) != 1 || cs[0].FeedURL != "https://example.com/feed.xml" {
		t.Fatalf("discover = %+v, err=%v", cs, err)
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

	bin := filepath.Join(t.TempDir(), "nanoflux-plugin-hello")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = filepath.Join("..", "..", "examples", "plugin-hello")
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build example plugin: %v\n%s", err, out)
	}

	reg := NewRegistry()
	hosts := NewHosts(limited.Client(), NewCooldown())
	cleanup := LoadExternal(context.Background(), filepath.Dir(bin), reg, hosts.For)
	defer cleanup()

	u, _ := url.Parse("https://example.com/profile")
	f := reg.Match(u, pluginapi.CapFetch)
	if f == nil {
		t.Fatal("plugin not matched")
	}
	_, err := f.Fetch(context.Background(), pluginapi.FetchRequest{URL: limited.URL}, hosts.For(f))
	var rl *pluginapi.RateLimit
	if !errors.As(err, &rl) {
		t.Fatalf("external plugin should surface a typed RateLimit, got %v", err)
	}
	if rl.RetryAfter <= 0 {
		t.Fatalf("retry-after should be positive: %+v", rl)
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
