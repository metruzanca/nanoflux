package web

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/a-h/templ"

	"github.com/metruzanca/nanoflux/internal/store"
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
	if got := TimeFmt("", "not a time"); got != "not a time" {
		t.Fatalf("invalid input: %q", got)
	}
	// Empty timezone falls back to server local (UTC here), so the output is
	// a relative label rather than the raw stamp.
	if got := TimeFmt("", "2026-09-20 12:00:00"); !strings.Contains(got, "Today at") && !strings.Contains(got, "Yesterday at") {
		t.Fatalf("expected a relative label, got %q", got)
	}
}

// renderComponent executes a templ component and returns its HTML output.
func renderComponent(t *testing.T, c templ.Component) string {
	t.Helper()
	var buf bytes.Buffer
	if err := c.Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestLucide(t *testing.T) {
	// A known glyph renders its path inside a 24x24 stroked svg.
	got := renderComponent(t, Lucide("check"))
	if !strings.Contains(got, `<path d="M20 6 9 17l-5-5"/>`) ||
		!strings.Contains(got, `viewBox="0 0 24 24"`) ||
		!strings.Contains(got, `stroke="currentColor"`) {
		t.Fatalf("Lucide(check) = %q", got)
	}
	// An unknown name renders nothing rather than a broken svg.
	if got := renderComponent(t, Lucide("definitely-not-an-icon")); strings.Contains(got, "<svg") {
		t.Fatalf("unknown icon should render nothing, got %q", got)
	}
	// The edit glyph and the favorite star are wired to the same set.
	if got := renderComponent(t, EditIcon()); !strings.Contains(got, `class="ic"`) || !strings.Contains(got, "<path") {
		t.Fatalf("EditIcon = %q", got)
	}
	if got := renderComponent(t, FavIcon(true)); !strings.Contains(got, `class="star on"`) {
		t.Fatalf("favored FavIcon = %q", got)
	}
	if got := renderComponent(t, FavIcon(false)); strings.Contains(got, "star on") {
		t.Fatalf("unfavored FavIcon should not be filled: %q", got)
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
		got := renderComponent(t, SourceIcon(c.url))
		if !strings.Contains(got, `class="src-icon"`) || !strings.Contains(got, c.want) {
			t.Errorf("SourceIcon(%q) = %q, want src %q", c.url, got, c.want)
		}
	}
	// Empty/invalid URLs fall back to an inline globe.
	if got := renderComponent(t, SourceIcon("")); !strings.Contains(got, "svg") {
		t.Errorf("SourceIcon(\"\") = %q, want inline svg", got)
	}
}

func TestIsGallery(t *testing.T) {
	gallery := "https://preview.redd.it/5t6y7u8i.jpg?width=140&height=140&crop=1:1,smart&auto=webp&s=x"
	if !IsGallery(gallery) {
		t.Fatal("square gallery cover should be detected")
	}
	image := "https://preview.redd.it/87u0k8i05brf1.jpeg?width=640&crop=smart&auto=webp&s=y"
	if IsGallery(image) {
		t.Fatal("natural-aspect image post should not be a gallery")
	}
	if IsGallery("https://external-preview.redd.it/x.jpeg?width=320") {
		t.Fatal("external-preview should not be a gallery")
	}
}

func TestGalleryThumb(t *testing.T) {
	gallery := "https://preview.redd.it/5t6y7u8i.jpg?width=140&height=140&crop=1:1,smart&auto=webp&s=x"
	if got := GalleryThumb(gallery); got != "https://i.redd.it/5t6y7u8i.jpg" {
		t.Fatalf("GalleryThumb = %q", got)
	}
	if got := GalleryThumb("https://preview.redd.it/x.jpeg?width=640&crop=smart&s=y"); got != "" {
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
		{"multi image post, no text", `<div><a href="https://example.com/full1.jpg"><img src="https://example.com/t1.jpg"></a></div><div><a href="https://example.com/full2.jpg"><img src="https://example.com/t2.jpg"></a></div>`, "https://example.com/t1.jpg", "Pic", false},
		{"text article with feed thumbnail", "<p>article body</p>", img, "Pic", false},
		{"no image url", `<img src="` + img + `">`, "", "Pic", false},
		{"no image in summary", "plain text summary", img, "Pic", false},
		{"empty summary", "", img, "Pic", false},
	}
	for _, c := range cases {
		if got := IsImagePost(c.summary, c.imageURL, c.title); got != c.want {
			t.Errorf("%s: IsImagePost = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestEnclosureKindAndImages(t *testing.T) {
	cases := []struct {
		enc  store.Enclosure
		want string
	}{
		{store.Enclosure{URL: "https://p.dev/ep1.mp3", MIMEType: "audio/mpeg"}, "audio"},
		{store.Enclosure{URL: "https://p.dev/clip.mp4", MIMEType: "video/mp4"}, "video"},
		{store.Enclosure{URL: "https://p.dev/photo.jpg?e=1790070194&t=signed", MIMEType: "image/jpeg"}, "image"},
		// Signed URLs match on the parsed path even with no MIME type.
		{store.Enclosure{URL: "https://p.dev/photo.jpg?e=1&t=2", MIMEType: ""}, "image"},
		{store.Enclosure{URL: "https://p.dev/demo.webp"}, "image"},
		{store.Enclosure{URL: "https://p.dev/notes.txt", MIMEType: "text/plain"}, ""},
		{store.Enclosure{URL: "https://p.dev/ep1.mp3"}, "audio"},
	}
	for _, c := range cases {
		if got := EnclosureKind(c.enc); got != c.want {
			t.Errorf("EnclosureKind(%+v) = %q, want %q", c.enc, got, c.want)
		}
	}
	imgs := ImageEnclosures([]store.Enclosure{
		{URL: "https://p.dev/photo.jpg?e=1&t=2", MIMEType: "image/jpeg"},
		{URL: "https://p.dev/ep1.mp3", MIMEType: "audio/mpeg"},
		{URL: "https://p.dev/other.png"},
	})
	if len(imgs) != 2 {
		t.Fatalf("ImageEnclosures = %d, want 2", len(imgs))
	}
}

func TestBodyHasImage(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		{"", false},
		{"plain text only", false},
		{"<p>a caption</p>", false},
		{`<img src="https://p.dev/photo.jpg">`, true},
		{`<p>a caption</p><img src="https://p.dev/photo.jpg">`, true},
		{`<a href="https://p.dev"><img src="https://p.dev/photo.jpg"></a>`, true},
		{"<p>a <b>bold</b> caption</p>", false},
	}
	for _, c := range cases {
		if got := BodyHasImage(c.body); got != c.want {
			t.Errorf("BodyHasImage(%q) = %v, want %v", c.body, got, c.want)
		}
	}
}

func TestUpgradeImageSrcs(t *testing.T) {
	// Blogger-style: img thumbnails inside <a> links to the full-size image.
	body := `<div class="separator"><a href="https://p.dev/full/1.jpg"><img src="https://p.dev/320/1.jpg" width="320"/></a></div><div><a href="https://p.dev/full/2.jpg"><img src="https://p.dev/320/2.jpg"/></a></div>`
	got := UpgradeImageSrcs(body)
	if !strings.Contains(got, `src="https://p.dev/full/1.jpg"`) || !strings.Contains(got, `src="https://p.dev/full/2.jpg"`) {
		t.Fatalf("body img srcs should be upgraded to the full-size links: %s", got)
	}
	if strings.Contains(got, `src="https://p.dev/320/1.jpg"`) {
		t.Fatalf("thumbnail src should be replaced: %s", got)
	}
	for _, stray := range []string{"<html", "<head", "<body"} {
		if strings.Contains(got, stray) {
			t.Fatalf("output must be a clean fragment, got stray %q: %s", stray, got)
		}
	}
	// A link to a non-image leaves the img alone.
	keep := `<a href="https://p.dev/article.html"><img src="https://p.dev/hero.jpg"></a>`
	if got := UpgradeImageSrcs(keep); !strings.Contains(got, `src="https://p.dev/hero.jpg"`) {
		t.Fatalf("non-image link must not upgrade the img: %s", got)
	}
	// A bare img is untouched.
	if got := UpgradeImageSrcs(`<img src="https://p.dev/plain.jpg">`); !strings.Contains(got, `src="https://p.dev/plain.jpg"`) {
		t.Fatalf("bare img must be untouched: %s", got)
	}
}

func TestBestImageURL(t *testing.T) {
	summary := `<a href="https://p.dev/full/1.jpg"><img src="https://p.dev/320/1.jpg"></a>`
	if got := BestImageURL(summary, "https://p.dev/320/1.jpg"); got != "https://p.dev/full/1.jpg" {
		t.Errorf("BestImageURL = %q, want the linked full-size image", got)
	}
	// No linked image: keep the given url.
	if got := BestImageURL(`<img src="https://p.dev/plain.jpg">`, "https://p.dev/plain.jpg"); got != "https://p.dev/plain.jpg" {
		t.Errorf("BestImageURL = %q, want the given url", got)
	}
}

func TestFormatBytes(t *testing.T) {
	for _, tc := range []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{500, "500 B"},
		{1536, "1.5 KB"},
		{5 << 20, "5.0 MB"},
		{3 << 30, "3.0 GB"},
	} {
		if got := FormatBytes(tc.in); got != tc.want {
			t.Errorf("FormatBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestStaleFeedFor(t *testing.T) {
	now := time.Now()
	d := func(days int) string {
		return time.Now().Add(-time.Duration(days) * 24 * time.Hour).Format("2006-01-02 15:04:05")
	}
	cases := []struct {
		last        string
		wantDays    int
		wantAbandon bool
		wantOK      bool
	}{
		{"", 0, false, false},
		{d(1), 0, false, false}, // active
		{d(6), 0, false, false},
		{d(8), 8, false, true},
		{d(14), 14, false, true},
		{d(30), 30, true, true},
		{d(45), 45, true, true},
		{"garbage", 0, false, false},
	}
	_ = now
	for _, c := range cases {
		sf, ok := staleFeedFor(c.last, time.Now().Format("2006-01-02 15:04:05"))
		if ok != c.wantOK || (ok && (sf.Days != c.wantDays || sf.Abandoned != c.wantAbandon)) {
			t.Errorf("staleFeedFor(%q) = %+v, %v; want days=%d aband=%v ok=%v", c.last, sf, ok, c.wantDays, c.wantAbandon, c.wantOK)
		}
	}
}
