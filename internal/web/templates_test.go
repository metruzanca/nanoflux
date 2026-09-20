package web

import (
	"strings"
	"testing"
)

func TestSourceIcon(t *testing.T) {
	cases := []struct {
		url  string
		icon string
	}{
		{"https://www.youtube.com/feeds/videos.xml?channel_id=UCx", "src-icon"},
		{"https://youtu.be/abc", "src-icon"},
		{"https://x.com/sama", "src-icon"},
		{"https://twitter.com/sama", "src-icon"},
		{"https://www.example.com/feed.xml", "src-icon"},
	}
	for _, c := range cases {
		got := string(sourceIcon(c.url))
		if !strings.Contains(got, `class="src-icon"`) {
			t.Errorf("sourceIcon(%q) = %q", c.url, got)
		}
	}
	// Distinct icons for the three kinds.
	x := string(sourceIcon("https://x.com/sama"))
	yt := string(sourceIcon("https://www.youtube.com/feeds/videos.xml?channel_id=UCx"))
	web := string(sourceIcon("https://example.com/feed.xml"))
	if x == yt || yt == web || x == web {
		t.Fatalf("expected three distinct icons: x=%q yt=%q web=%q", x, yt, web)
	}
}
