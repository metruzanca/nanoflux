package x

import (
	"context"
	"encoding/base64"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/metruzanca/nanoflux/pluginapi"
)

// hostFunc adapts a function to pluginapi.Host.
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
		{"https://x.com/sama", true},
		{"https://x.com/sama/", true},
		{"https://www.x.com/sama", true},
		{"https://twitter.com/sama", true},
		{"https://x.com/@sama", true},
		{"https://x.com/home", false},
		{"https://x.com/explore", false},
		{"https://x.com/i/flow/login", false},
		{"https://x.com/sama/status/123", false},
		{"https://example.com/sama", false},
		{"https://x.com", false},
	}
	for _, c := range cases {
		u, _ := url.Parse(c.raw)
		if got := isProfileURL(u); got != c.want {
			t.Errorf("isProfileURL(%q) = %v, want %v", c.raw, got, c.want)
		}
	}
}

func TestSnowflakeTime(t *testing.T) {
	got := snowflakeTime("2100351958167220547")
	want := time.Date(2026, 9, 16, 22, 31, 44, 136e6, time.UTC)
	if !got.Equal(want) {
		t.Errorf("snowflakeTime = %v, want %v", got, want)
	}
	if !snowflakeTime("notanumber").IsZero() {
		t.Error("invalid id should give zero time")
	}
}

func TestPostTitle(t *testing.T) {
	if got := PostTitle("first line\nsecond line"); got != "first line" {
		t.Errorf("got %q", got)
	}
	long := strings.Repeat("a", 200)
	if got := PostTitle(long); len([]rune(got)) != 100 || !strings.HasSuffix(got, "…") {
		t.Errorf("long title = %q", got)
	}
}

func fixture() string {
	tweets := []struct{ id, text string }{
		{"2100351958167220547", "the main thing i was excited about launching this week will be next week instead"},
		{"2099872600977760451", "big this week\n\nand then for devday"},
		{"2099352016988614852", "There are two ways AI progress could go very badly"},
	}
	var b strings.Builder
	b.WriteString(`<html><head><title>Sam Altman (@sama) / X</title></head><body>`)
	for i, tw := range tweets {
		key := base64.StdEncoding.EncodeToString([]byte("Tweet:" + tw.id))
		b.WriteString(`"TimelineTimelineEntry:tweet-` + tw.id + `":x`)
		b.WriteString(`"client:` + key + `:details":$R[` + strconv.Itoa(i) + `]={__id:"c",full_text:"` + jsonEscape(tw.text) + `"}`)
	}
	b.WriteString(`</body></html>`)
	return b.String()
}

func jsonEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func TestFetchProfile(t *testing.T) {
	h := hostFunc(func(_ context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		return pluginapi.HTTPResponse{Status: 200, Body: []byte(fixture())}, nil
	})
	res, err := Plugin{}.Fetch(context.Background(), pluginapi.FetchRequest{URL: "https://x.com/sama"}, h)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.Feed.Title != "Sam Altman" || res.Feed.HomeURL != "https://x.com/sama" {
		t.Errorf("feed = %+v", res.Feed)
	}
	if len(res.Items) != 3 {
		t.Fatalf("items = %d, want 3", len(res.Items))
	}
	first := res.Items[0]
	if first.GUID != "tweet:2100351958167220547" || first.Link != "https://x.com/sama/status/2100351958167220547" {
		t.Errorf("first = %+v", first)
	}
	if first.PublishedAt != "2026-09-16 22:31:44" {
		t.Errorf("published = %q", first.PublishedAt)
	}
}

func TestFetchNoPosts(t *testing.T) {
	h := hostFunc(func(_ context.Context, _ pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		return pluginapi.HTTPResponse{Status: 200, Body: []byte(`<html><title>someone</title></html>`)}, nil
	})
	if _, err := (Plugin{}).Fetch(context.Background(), pluginapi.FetchRequest{URL: "https://x.com/someone"}, h); err == nil {
		t.Fatal("expected error when no posts are embedded")
	}
}

func TestFetchRateLimited(t *testing.T) {
	h := hostFunc(func(_ context.Context, _ pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		return pluginapi.HTTPResponse{Status: 429, RateLimited: true, RetryAfter: time.Minute}, nil
	})
	_, err := (Plugin{}).Fetch(context.Background(), pluginapi.FetchRequest{URL: "https://x.com/sama"}, h)
	var rl *pluginapi.RateLimit
	if !asRateLimit(err, &rl) {
		t.Fatalf("expected RateLimit, got %v", err)
	}
}

func asRateLimit(err error, target **pluginapi.RateLimit) bool {
	if rl, ok := err.(*pluginapi.RateLimit); ok {
		*target = rl
		return true
	}
	return false
}
