package feedparse

import (
	"strings"

	"github.com/metruzanca/nanoflux/internal/tagnorm"
	"golang.org/x/net/html"
)

// Reserved media-type category labels. They are the item-level counterpart of
// the reblog/self-reblog convention: a tag a user can filter on to find (or
// hide) a kind of post. A post may carry more than one (a video with a long
// write-up is type:video and type:text).
//
// pluginapi mirrors these strings (it cannot import this package), so keep the
// two in sync.
const (
	CategoryTypeImage = "type:image"
	CategoryTypeVideo = "type:video"
	CategoryTypeAudio = "type:audio"
	CategoryTypeText  = "type:text"
)

// mediaTextMinRunes is how much visible text a post needs before it is also
// tagged type:text. It separates a real article, blog post or short story from
// a one-liner, without turning every toot into a text post.
const mediaTextMinRunes = 1200

// applyMediaTypeCategories adds the reserved type:* category convention to every
// parsed item. It runs for both the generic parser and plugin-fetched feeds, so
// the labels exist without any site-specific code. Plugin categories are merged
// afterwards, which dedupes a type:* label the plugin also supplied.
func applyMediaTypeCategories(res *Result) {
	for i := range res.Items {
		it := &res.Items[i]
		extra := mediaTypeCategories(it)
		if len(extra) == 0 {
			continue
		}
		it.Categories = tagnorm.NormalizeAll(append(it.Categories, extra...))
	}
}

// mediaTypeCategories reports what media an item carries. Enclosures are the
// authoritative source (a plugin may declare a render kind); the summary HTML
// and the item link cover feeds that only embed media inline or link straight to
// a file. ImageURL is deliberately ignored: it is often a video poster or a
// link-preview thumbnail, which would mislabel the post as an image.
func mediaTypeCategories(it *Item) []string {
	var hasImage, hasVideo, hasAudio bool

	for _, e := range it.Enclosures {
		switch ResolveEnclosureKind(e.Kind, e.URL, e.MIMEType) {
		case EnclosureKindImage:
			hasImage = true
		case EnclosureKindVideo, EnclosureKindHLS:
			hasVideo = true
		case EnclosureKindAudio:
			hasAudio = true
		}
	}

	switch ResolveEnclosureKind("", it.Link, "") {
	case EnclosureKindImage:
		hasImage = true
	case EnclosureKindVideo, EnclosureKindHLS:
		hasVideo = true
	case EnclosureKindAudio:
		hasAudio = true
	}

	textLen := visibleTextRunes(it.Summary)
	if it.Summary != "" && strings.Contains(it.Summary, "<") {
		img, video, audio := summaryMedia(it.Summary)
		hasImage = hasImage || img
		hasVideo = hasVideo || video
		hasAudio = hasAudio || audio
	}

	var out []string
	if hasImage {
		out = append(out, CategoryTypeImage)
	}
	if hasVideo {
		out = append(out, CategoryTypeVideo)
	}
	if hasAudio {
		out = append(out, CategoryTypeAudio)
	}
	if textLen >= mediaTextMinRunes {
		out = append(out, CategoryTypeText)
	}
	return out
}

// summaryMedia reports which media elements an item's summary HTML contains. An
// <iframe> counts as video: stored iframes are stripped by the ingest
// sanitizer, so an iframe in a feed's own content is an embedded player hint
// (a YouTube/Vimeo embed), not markup the app will render.
func summaryMedia(summary string) (image, video, audio bool) {
	doc, err := html.Parse(strings.NewReader(summary))
	if err != nil {
		return false, false, false
	}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style":
				// Their text is not visible content.
				return
			case "img", "picture":
				image = true
			case "video", "iframe", "embed":
				video = true
			case "audio":
				audio = true
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return image, video, audio
}

// visibleTextRunes returns the length of a summary's visible text, ignoring
// markup, whitespace runs and the contents of script/style elements. It is used
// only for the type:text threshold.
func visibleTextRunes(summary string) int {
	if summary == "" {
		return 0
	}
	if !strings.Contains(summary, "<") {
		return len([]rune(strings.TrimSpace(summary)))
	}
	doc, err := html.Parse(strings.NewReader(summary))
	if err != nil {
		return len([]rune(strings.TrimSpace(summary)))
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style") {
			return
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			b.WriteByte(' ')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return len([]rune(strings.Join(strings.Fields(b.String()), " ")))
}
