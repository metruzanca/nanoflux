package httpapi

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/metruzanca/nanoflux/internal/config"
	"github.com/metruzanca/nanoflux/internal/plugin"
	"github.com/metruzanca/nanoflux/pluginapi"
)

// pollFloorStub is the minimal plugin surface for the interval-floor tests: it
// claims CapMinPollInterval for reddit hosts and declares a one-hour floor.
type pollFloorStub struct{}

func (pollFloorStub) Meta() pluginapi.Meta {
	return pluginapi.Meta{
		Name:            "reddit",
		APIVersion:      pluginapi.APIVersion,
		MinPollInterval: time.Hour,
	}
}

func (pollFloorStub) Match(u *url.URL, cap pluginapi.Capability) bool {
	return cap == pluginapi.CapMinPollInterval && u != nil && strings.HasSuffix(u.Hostname(), "reddit.com")
}

func (pollFloorStub) Discover(context.Context, string, pluginapi.Host) ([]pluginapi.Candidate, error) {
	return nil, pluginapi.ErrUnsupportedCapability
}

func (pollFloorStub) Fetch(context.Context, pluginapi.FetchRequest, pluginapi.Host) (pluginapi.Result, error) {
	return pluginapi.Result{}, pluginapi.ErrUnsupportedCapability
}

// TestPollFloorSec covers the composition the feed editor and create paths use:
// the configured minimum is the base, a plugin floor raises it for its hosts,
// and a value below the floor is clamped on save.
func TestPollFloorSec(t *testing.T) {
	reg := plugin.NewRegistry()
	reg.RegisterExternal(pollFloorStub{})
	s := &Server{cfg: config.Config{PollMinInterval: 30 * time.Minute}, plugins: reg}

	if got := s.pollFloorSec("https://www.reddit.com/r/cats.rss"); got != 3600 {
		t.Fatalf("reddit floor = %d, want 3600 (plugin floor wins)", got)
	}
	if got := s.pollFloorSec("https://example.com/feed"); got != 1800 {
		t.Fatalf("other floor = %d, want 1800 (configured minimum)", got)
	}
	if got := s.clampPollIntervalSec("https://www.reddit.com/r/cats.rss", 900); got != 3600 {
		t.Fatalf("clamped interval = %d, want 3600", got)
	}
	if got := s.clampPollIntervalSec("https://example.com/feed", 900); got != 1800 {
		t.Fatalf("clamped interval = %d, want 1800", got)
	}
	if got := s.defaultPollIntervalSec("https://example.com/feed"); got != 1800 {
		t.Fatalf("default interval = %d, want 1800", got)
	}
}

// With no configured floor, a new feed keeps the historical 15-minute default
// and a user-supplied value is left alone (except for the 60s sanity floor in
// the poller).
func TestPollFloorSecDisabled(t *testing.T) {
	s := &Server{}
	if got := s.defaultPollIntervalSec("https://example.com/feed"); got != 900 {
		t.Fatalf("default interval = %d, want 900", got)
	}
	if got := s.clampPollIntervalSec("https://example.com/feed", 300); got != 300 {
		t.Fatalf("clamped interval = %d, want 300", got)
	}
}
