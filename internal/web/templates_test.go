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

func TestIsGallery(t *testing.T) {
	gallery := "https://preview.redd.it/5t6y7u8i.jpg?width=140&height=140&crop=1:1,smart&auto=webp&s=x"
	if !isGallery(gallery) {
		t.Fatal("square gallery cover should be detected")
	}
	image := "https://preview.redd.it/87u0k8i05brf1.jpeg?width=640&crop=smart&auto=webp&s=y"
	if isGallery(image) {
		t.Fatal("natural-aspect image post should not be a gallery")
	}
	if isGallery("https://external-preview.redd.it/x.jpeg?width=320") {
		t.Fatal("external-preview should not be a gallery")
	}
}

func TestGalleryThumb(t *testing.T) {
	gallery := "https://preview.redd.it/5t6y7u8i.jpg?width=140&height=140&crop=1:1,smart&auto=webp&s=x"
	if got := galleryThumb(gallery); got != "https://i.redd.it/5t6y7u8i.jpg" {
		t.Fatalf("galleryThumb = %q", got)
	}
	if got := galleryThumb("https://preview.redd.it/x.jpeg?width=640&crop=smart&s=y"); got != "" {
		t.Fatalf("image post should not get a gallery thumb: %q", got)
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
		{"reddit link post preview", `<a href="https://www.reddit.com/r/x/comments/1a/"><img src="https://external-preview.redd.it/1q2w3e4r.jpeg?width=320" alt="Mittens enjoys a sunny nap" title="Mittens enjoys a sunny nap"></a>`, "https://external-preview.redd.it/1q2w3e4r.jpeg?width=320", "Mittens enjoys a sunny nap", false},
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
