package feedparse

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestIsXProfileURL(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"https://x.com/sama", true},
		{"https://x.com/sama/", true},
		{"https://www.x.com/sama", true},
		{"https://twitter.com/sama", true},
		{"https://twitter.com/sama/", true},
		{"https://x.com/@sama", true},
		{"https://x.com/home", false},
		{"https://x.com/explore", false},
		{"https://x.com/search", false},
		{"https://x.com/i/flow/login", false},
		{"https://x.com/sama/status/123", false},
		{"https://x.com/sama/photo", false},
		{"https://x.com/hashtag/go", false},
		{"https://example.com/sama", false},
		{"https://x.com", false},
	}
	for _, c := range cases {
		u, err := url.Parse(c.raw)
		if err != nil {
			t.Fatalf("parse %q: %v", c.raw, err)
		}
		if got := isXProfileURL(u); got != c.want {
			t.Errorf("isXProfileURL(%q) = %v, want %v", c.raw, got, c.want)
		}
	}
}

func TestSnowflakeTime(t *testing.T) {
	// Tweet 2100351958167220547 -> 2026-09-16 22:31:44.136Z.
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
	if got := postTitle("hello world"); got != "hello world" {
		t.Errorf("got %q", got)
	}
	if got := postTitle("first line\nsecond line"); got != "first line" {
		t.Errorf("got %q", got)
	}
	long := strings.Repeat("a", 200)
	if got := postTitle(long); len([]rune(got)) != 100 || !strings.HasSuffix(got, "…") {
		t.Errorf("long title = %q (runes %d)", got, len([]rune(got)))
	}
}

func xFixture() string {
	tweets := []struct{ id, text string }{
		{"2100351958167220547", "the main thing i was excited about launching this week will be next week instead"},
		{"2099872600977760451", "big \U0001F6A2 this week\n\nand then for devday\n\n\U0001F6A2\U0001F6A2\U0001F6A2\U0001F6A2\U0001F6A2"},
		{"2099352016988614852", "There are two ways AI progress could go very badly"},
	}
	var b strings.Builder
	b.WriteString(`<html><head><title>Sam Altman (@sama) / X</title></head><body>`)
	for i, tw := range tweets {
		key := base64.StdEncoding.EncodeToString([]byte("Tweet:" + tw.id))
		// Two timeline entries per tweet id to exercise ordering dedup.
		b.WriteString(`"urt:server:TimelineTimelineEntry:tweet-` + tw.id + `":x`)
		b.WriteString(`"urt:server:TimelineTimelineEntry:tweet-` + tw.id + `":y`)
		b.WriteString(`"client:` + key + `:details":$R[` + strconv.Itoa(i) + `]={__id:"c",__typename:"TBirdData",display_text_range:$R[0]=[0,10],full_text:"` + jsonEscape(tw.text) + `",hashtag_entities:$R[1]={__refs:$R[2]=[]}}`)
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
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func TestFetchXProfile(t *testing.T) {
	old := xProfileHosts
	xProfileHosts = append([]string(nil), old...)
	xProfileHosts = append(xProfileHosts, "127.0.0.1")
	defer func() { xProfileHosts = old }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(xFixture()))
	}))
	defer srv.Close()

	res, err := Fetch(context.Background(), srv.URL+"/sama", srv.Client(), "", "")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.Feed.Title != "Sam Altman" {
		t.Errorf("title = %q, want Sam Altman", res.Feed.Title)
	}
	if res.Feed.HomeURL != srv.URL+"/sama" {
		t.Errorf("home = %q", res.Feed.HomeURL)
	}
	if len(res.Items) != 3 {
		t.Fatalf("items = %d, want 3", len(res.Items))
	}
	first := res.Items[0]
	if first.GUID != "tweet:2100351958167220547" {
		t.Errorf("guid = %q", first.GUID)
	}
	if first.Link != "https://x.com/sama/status/2100351958167220547" {
		t.Errorf("link = %q", first.Link)
	}
	if first.Title != "the main thing i was excited about launching this week will be next week instead" {
		t.Errorf("title = %q", first.Title)
	}
	if !strings.Contains(first.Summary, "next week instead") {
		t.Errorf("summary = %q", first.Summary)
	}
	if first.PublishedAt != "2026-09-16 22:31:44" {
		t.Errorf("published = %q", first.PublishedAt)
	}
	// Multi-line post keeps newlines in the summary and truncates the title.
	second := res.Items[1]
	if !strings.Contains(second.Summary, "\n") {
		t.Errorf("multi-line summary lost newlines: %q", second.Summary)
	}
	if strings.Contains(second.Title, "\n") {
		t.Errorf("title should be the first line only: %q", second.Title)
	}
}

func TestFetchXProfileNoPosts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><head><title>someone</title></head><body>nothing</body></html>`))
	}))
	defer srv.Close()

	if _, err := fetchXProfile(context.Background(), srv.URL+"/someone", srv.Client()); err == nil {
		t.Fatal("expected error when no posts are embedded")
	}
}
