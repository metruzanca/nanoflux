package httpapi

import (
	"strings"
	"testing"

	"github.com/metruzanca/nanoflux/internal/config"
)

func TestAnalyticsDisabledByDefault(t *testing.T) {
	_, h := newTestServer(t)
	body := doGetRaw(h, "/login").Body.String()
	if strings.Contains(body, "umami") || strings.Contains(body, "analytics") {
		t.Fatalf("login page should have no tracker: %s", body)
	}
}

func TestAnalyticsScriptRenderedWhenEnabled(t *testing.T) {
	s, h := newTestServer(t)
	s.cfg.Analytics = config.AnalyticsConfig{
		On:        true,
		ScriptURL: "https://umami.example.com/script.js",
		WebsiteID: "abc-123",
	}
	body := doGetRaw(h, "/login").Body.String()
	for _, want := range []string{
		`src="/static/umami.js"`,
		`src="https://umami.example.com/script.js"`,
		`data-website-id="abc-123"`,
		`data-before-send="nfBeforeSend"`,
		`data-do-not-track="true"`,
		`data-exclude-hash="true"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("login page missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, "data-host-url") || strings.Contains(body, "data-tag") {
		t.Fatalf("optional attrs should be omitted when empty: %s", body)
	}
}

func TestAnalyticsOptionalAttrs(t *testing.T) {
	s, h := newTestServer(t)
	s.cfg.Analytics = config.AnalyticsConfig{
		On:        true,
		ScriptURL: "https://umami.example.com/script.js",
		WebsiteID: "abc-123",
		HostURL:   "https://stats.example.com",
		Tag:       "demo",
	}
	body := doGetRaw(h, "/login").Body.String()
	if !strings.Contains(body, `data-host-url="https://stats.example.com"`) {
		t.Fatalf("missing data-host-url: %s", body)
	}
	if !strings.Contains(body, `data-tag="demo"`) {
		t.Fatalf("missing data-tag: %s", body)
	}
}
