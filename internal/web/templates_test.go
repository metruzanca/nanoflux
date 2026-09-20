package web

import (
	"strings"
	"testing"
)

func TestSourceIcon(t *testing.T) {
	cases := []struct {
		url  string
		want string
	}{
		{"https://www.youtube.com/feeds/videos.xml?channel_id=UCx", `src="/icons/www.youtube.com"`},
		{"https://youtu.be/abc", `src="/icons/youtu.be"`},
		{"https://x.com/sama", `src="/icons/x.com"`},
		{"https://twitter.com/sama", `src="/icons/twitter.com"`},
		{"https://www.example.com/feed.xml", `src="/icons/www.example.com"`},
	}
	for _, c := range cases {
		got := string(sourceIcon(c.url))
		if !strings.Contains(got, `class="src-icon"`) || !strings.Contains(got, c.want) {
			t.Errorf("sourceIcon(%q) = %q, want src %q", c.url, got, c.want)
		}
	}
	// Empty/invalid URLs fall back to an inline globe.
	if got := string(sourceIcon("")); !strings.Contains(got, "svg") {
		t.Errorf("sourceIcon(\"\") = %q, want inline svg", got)
	}
}
