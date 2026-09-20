package web

import (
	"strings"
	"testing"
	"time"
)

func TestFormatRel(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, loc)
	parse := func(s string) time.Time {
		tm, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
		if err != nil {
			t.Fatal(err)
		}
		return tm
	}
	cases := []struct {
		in, want string
	}{
		{"2026-09-20 23:05", "Today at 11:05pm"},
		{"2026-09-19 23:05", "Yesterday at 11:05pm"},
		{"2026-09-18 09:15", "2 days ago"},
		{"2026-09-13 09:15", "1 week ago"},
		{"2026-09-01 09:15", "2 weeks ago"},
		{"2026-06-20 09:15", "3 months ago"},
		{"2025-09-20 09:15", "Sep 20, 2025 at 9:15am"},
		{"2026-09-21 09:15", "Sep 21, 2026 at 9:15am"}, // future
	}
	for _, c := range cases {
		if got := formatRel(parse(c.in), loc, now); got != c.want {
			t.Errorf("formatRel(%s) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTimeFmt(t *testing.T) {
	// Unparseable input is returned unchanged.
	if got := timeFmt("", "not a time"); got != "not a time" {
		t.Fatalf("invalid input: %q", got)
	}
	// Empty timezone falls back to server local (UTC here), so the output is
	// a relative label rather than the raw stamp.
	if got := timeFmt("", "2026-09-20 12:00:00"); !strings.Contains(got, "Today at") && !strings.Contains(got, "Yesterday at") {
		t.Fatalf("expected a relative label, got %q", got)
	}
}

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
