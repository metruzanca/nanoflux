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

func TestIsImagePost(t *testing.T) {
	const img = "https://example.com/pic.jpg"
	cases := []struct {
		name     string
		summary  string
		imageURL string
		title    string
		want     bool
	}{
		{"linked image with alt", `<a href="https://example.com/p"><img src="` + img + `" alt="Your weakness has ears" title="Your weakness has ears" /></a>`, img, "Your weakness has ears", true},
		{"bare image", `<img src="` + img + `">`, img, "Pic", true},
		{"image plus caption", `<img src="` + img + `"> caption text`, img, "Pic", false},
		{"image with real body text", `<p>lots of article text here</p><img src="` + img + `">`, img, "Pic", false},
		{"text article with feed thumbnail", "<p>article body</p>", img, "Pic", false},
		{"no image url", `<img src="` + img + `">`, "", "Pic", false},
		{"no image in summary", "plain text summary", img, "Pic", false},
		{"empty summary", "", img, "Pic", false},
	}
	for _, c := range cases {
		if got := isImagePost(c.summary, c.imageURL, c.title); got != c.want {
			t.Errorf("%s: isImagePost = %v, want %v", c.name, got, c.want)
		}
	}
}
