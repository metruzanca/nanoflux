package instagram

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/metruzanca/nanoflux/pluginapi"
)

type hostFunc func(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error)

func (h hostFunc) Do(ctx context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	return h(ctx, req)
}
func (hostFunc) Now() time.Time      { return time.Now().UTC() }
func (hostFunc) Logf(string, ...any) {}

func TestIsProfileURL(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"https://www.instagram.com/metru.dev", true},
		{"https://instagram.com/metru.dev/", true},
		{"https://instagram.com/p/abc", false},
		{"https://instagram.com/reel/abc", false},
		{"https://instagram.com/explore", false},
		{"https://instagram.com/", false},
		{"https://example.com/metru.dev", false},
	}
	for _, c := range cases {
		u, _ := url.Parse(c.raw)
		if got := isProfileURL(u); got != c.want {
			t.Errorf("isProfileURL(%q) = %v, want %v", c.raw, got, c.want)
		}
	}
}

func TestShortcode(t *testing.T) {
	// 3 is "D" in the shortcode alphabet.
	if got := shortcode("3"); got != "D" {
		t.Errorf("shortcode(3) = %q, want D", got)
	}
	if got := shortcode("0"); got != "" {
		t.Errorf("shortcode(0) = %q, want empty", got)
	}
	if got := shortcode("x"); got != "" {
		t.Errorf("invalid should be empty, got %q", got)
	}
}

func TestMediaTime(t *testing.T) {
	// Media id 2100351958167220547 decodes via (id>>23)+epoch.
	got := mediaTime("2100351958167220547")
	if got.IsZero() {
		t.Fatal("expected a time")
	}
	if !mediaTime("bad").IsZero() {
		t.Error("invalid id should give zero time")
	}
}

func TestDisplayName(t *testing.T) {
	body := []byte(`<html><head><title>Wyatt Cheng (@wc) • Instagram photos and videos</title></head></html>`)
	if got := displayName(body, "wc"); got != "Wyatt Cheng" {
		t.Errorf("displayName = %q", got)
	}
	body2 := []byte(`{"full_name":"Wyatt Cheng"}`)
	if got := displayName(body2, "wc"); got != "Wyatt Cheng" {
		t.Errorf("displayName from full_name = %q", got)
	}
}

func TestFetchProfile(t *testing.T) {
	body := `{"__typename":"XIGPolarisMedia","pk":"2100351958167220547","product_type":"clips","caption":{"text":"my caption"}}`
	h := hostFunc(func(_ context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		return pluginapi.HTTPResponse{Status: 200, Body: []byte(body)}, nil
	})
	res, err := Plugin{}.Fetch(context.Background(), pluginapi.FetchRequest{URL: "https://www.instagram.com/metru.dev"}, h)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(res.Items))
	}
	it := res.Items[0]
	if it.GUID != "instagram:2100351958167220547" {
		t.Errorf("guid = %q", it.GUID)
	}
	if it.Title != "my caption" {
		t.Errorf("title = %q", it.Title)
	}
	if !hasPrefix(it.Summary, `<img src="https://www.instagram.com/p/`) ||
		!strings.Contains(it.Summary, "my caption") {
		t.Errorf("summary should carry the image and caption, got %q", it.Summary)
	}
	if !hasPrefix(it.Link, "https://www.instagram.com/reel/") {
		t.Errorf("clip link = %q", it.Link)
	}
	if it.ImageURL == "" {
		t.Error("image should be the stable media endpoint")
	}
}

func TestSummaryHTMLEscapesCaption(t *testing.T) {
	got := summaryHTML("https://example.com/i.jpg", "a <b> & c")
	if !strings.Contains(got, `src="https://example.com/i.jpg"`) {
		t.Errorf("summary should hold the image: %q", got)
	}
	if !strings.Contains(got, "a &lt;b&gt; &amp; c") || strings.Contains(got, "<b>") {
		t.Errorf("caption should be escaped: %q", got)
	}
	if noImg := summaryHTML("", "just text"); noImg != "<p>just text</p>" {
		t.Errorf("summary without an image = %q", noImg)
	}
}

func hasPrefix(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }
