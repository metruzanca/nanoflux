package reddit

import "testing"

func TestIsGalleryThumb(t *testing.T) {
	gallery := "https://preview.redd.it/5t6y7u8i.jpg?width=140&height=140&crop=1:1,smart&auto=webp&s=x"
	if !isGalleryThumb(gallery) {
		t.Fatal("square gallery cover should be detected")
	}
	image := "https://preview.redd.it/87u0k8i05brf1.jpeg?width=640&crop=smart&auto=webp&s=y"
	if isGalleryThumb(image) {
		t.Fatal("natural-aspect image post should not be a gallery")
	}
	if isGalleryThumb("https://external-preview.redd.it/x.jpeg?width=320") {
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
		if got := isImagePost(c.summary, c.imageURL, c.title); got != c.want {
			t.Errorf("%s: isImagePost = %v, want %v", c.name, got, c.want)
		}
	}
}
