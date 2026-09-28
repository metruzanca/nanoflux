package reddit

import (
	"net/url"
	"strings"

	"golang.org/x/net/html"

	"github.com/metruzanca/nanoflux/pluginapi"
)

// The classifiers below are the plugin-owned replacements for the core's former
// reddit URL-sniffing (web.IsLinkPost/IsImagePost/IsGallery/GalleryThumb). They
// read only the stored item fields, so they run at view time with no network.

// isLinkThumb reports whether an item is a reddit "link post": its primary
// content is an external site, previewed by reddit's external-preview thumbnail.
func isLinkThumb(imageURL string) bool {
	if imageURL == "" {
		return false
	}
	u, err := url.Parse(imageURL)
	if err != nil {
		return false
	}
	return u.Hostname() == "external-preview.redd.it"
}

// isGalleryThumb reports whether an item is a reddit gallery post, from its
// stored thumbnail alone: galleries use a small square cover in the feed
// (crop=1:1,smart) rather than the natural-aspect image-post crop.
func isGalleryThumb(imageURL string) bool {
	u, err := url.Parse(imageURL)
	if err != nil || u.Hostname() != "preview.redd.it" {
		return false
	}
	return u.Query().Get("crop") == "1:1,smart"
}

// isImagePost reports whether an item's primary content is a single image: the
// summary is exactly one <img> (optionally wrapped in a link) with no real text
// beyond the image's alt text. imageURL is required so items whose thumbnail
// comes from feed metadata but whose body is text are not treated as images.
// Reddit's external-preview.redd.it thumbnails are excluded: they preview a link
// post's external destination, not an in-post image.
func isImagePost(summary, imageURL, title string) bool {
	if imageURL == "" {
		return false
	}
	u, err := url.Parse(imageURL)
	if err != nil {
		return false
	}
	if u.Hostname() == "external-preview.redd.it" {
		return false
	}
	doc, err := html.Parse(strings.NewReader(summary))
	if err != nil {
		return false
	}
	imgs := 0
	var text strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			text.WriteString(n.Data)
		}
		if n.Type == html.ElementNode && n.Data == "img" {
			imgs++
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	for c := doc.FirstChild; c != nil; c = c.NextSibling {
		walk(c)
	}
	if imgs != 1 {
		return false
	}
	t := strings.TrimSpace(text.String())
	return t == "" || t == strings.TrimSpace(title)
}

// GalleryThumb returns the full-res first image of a reddit gallery post from
// its stored thumbnail (a tiny square cover), or "" when the item is not a
// gallery. It is exported for the host's row rendering via the decoration's
// ThumbURL, set in Decorate.
func GalleryThumb(imageURL string) string {
	if !isGalleryThumb(imageURL) {
		return ""
	}
	u, err := url.Parse(imageURL)
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 1 {
		return ""
	}
	if id, ext, ok := strings.Cut(parts[0], "."); ok && id != "" && ext != "" {
		if ext == "jpeg" {
			ext = "jpg"
		}
		return "https://i.redd.it/" + id + "." + ext
	}
	return ""
}

// kindThumb returns the row thumbnail override for an item: a gallery's stored
// cover is replaced by its full-res first image. Other kinds keep the stored
// thumbnail.
func kindThumb(kind pluginapi.ItemKind, imageURL string) string {
	if kind == pluginapi.KindGallery {
		return GalleryThumb(imageURL)
	}
	return ""
}
