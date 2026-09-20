package web

import (
	"embed"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
	"golang.org/x/net/html"

	"github.com/metruzanca/nanoflux/internal/db"
)

//go:embed static/*
var staticFS embed.FS

// Render executes a templ component against w.
func Render(w http.ResponseWriter, r *http.Request, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	c.Render(r.Context(), w)
}

// Static serves embedded assets (css, htmx.js, app.js) at /static/.
func Static() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	return http.StripPrefix("/static/", http.FileServerFS(sub))
}

// PageTitle renders the <title> text: "page — nanoflux" or just "nanoflux".
func PageTitle(title string) string {
	if title != "" {
		return title + " — nanoflux"
	}
	return "nanoflux"
}

// Initial returns the uppercased first character of a name, for the default
// avatar shown when a user has no profile picture.
func Initial(s string) string {
	for _, r := range s {
		return strings.ToUpper(string(r))
	}
	return "?"
}

// YoutubeEmbedURL returns the embeddable player URL for a YouTube video link
// (watch, youtu.be, shorts, live, embed), or "" for anything else.
func YoutubeEmbedURL(link string) string {
	u, err := url.Parse(link)
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if strings.HasSuffix(host, "youtu.be") {
		if id := strings.Trim(u.Path, "/"); id != "" {
			return "https://www.youtube.com/embed/" + id
		}
	}
	if !strings.HasSuffix(host, "youtube.com") {
		return ""
	}
	switch u.Path {
	case "/watch":
		if id := u.Query().Get("v"); id != "" {
			return "https://www.youtube.com/embed/" + id
		}
	default:
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) == 2 && (parts[0] == "shorts" || parts[0] == "live" || parts[0] == "embed") {
			return "https://www.youtube.com/embed/" + parts[1]
		}
	}
	return ""
}

// IsLinkPost reports whether an item is a reddit "link post" — its primary
// content is an external site (imgur, a news article) previewed by
// reddit's external-preview thumbnail.
func IsLinkPost(imageURL string) bool {
	if imageURL == "" {
		return false
	}
	u, err := url.Parse(imageURL)
	if err != nil {
		return false
	}
	return u.Hostname() == "external-preview.redd.it"
}

// IsImagePost reports whether an item's primary content is a single image:
// the summary is an <img> (optionally wrapped in a link) with no real text
// beyond the image's alt text. imageURL is required so items whose thumbnail
// comes from feed metadata but whose body is text are not treated as images.
// Reddit's external-preview.redd.it thumbnails are excluded: they preview a
// link post's external destination, not an in-post image.
func IsImagePost(summary, imageURL, title string) bool {
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
	hasImg := false
	var text strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			text.WriteString(n.Data)
		}
		if n.Type == html.ElementNode && n.Data == "img" {
			hasImg = true
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	for c := doc.FirstChild; c != nil; c = c.NextSibling {
		walk(c)
	}
	if !hasImg {
		return false
	}
	t := strings.TrimSpace(text.String())
	return t == "" || t == strings.TrimSpace(title)
}

// IsGallery reports whether an item is a reddit gallery post, from its stored
// thumbnail alone: galleries use a small square cover in the feed
// (width=140&height=140&crop=1:1) rather than the natural-aspect image-post
// crop.
func IsGallery(imageURL string) bool {
	u, err := url.Parse(imageURL)
	if err != nil || u.Hostname() != "preview.redd.it" {
		return false
	}
	q := u.Query()
	return q.Get("crop") == "1:1,smart"
}

// GalleryThumb returns the full-res first image of a reddit gallery post (the
// stored thumbnail is a tiny square cover), or "" when the item is not a
// gallery.
func GalleryThumb(imageURL string) string {
	if !IsGallery(imageURL) {
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

// StripHTML extracts plain text from feed-provided HTML.
func StripHTML(s string) string {
	doc, err := html.Parse(strings.NewReader(s))
	if err != nil {
		return s
	}
	var b strings.Builder
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	for c := doc.FirstChild; c != nil; c = c.NextSibling {
		walk(c)
	}
	return strings.TrimSpace(b.String())
}

// TimeFmt renders a stored UTC timestamp relative to the user's timezone.
// tz is an IANA timezone name; empty means the server's local time. Labels:
// "Today at 3:04pm", "Yesterday at 3:04pm", "3 days ago", "2 weeks ago",
// "3 months ago", or an absolute "Sep 2, 2026 at 3:04pm" for past years and
// future timestamps.
func TimeFmt(tz, s string) string {
	t, err := db.ParseTime(s)
	if err != nil {
		return s
	}
	return formatRel(t, serverLoc(tz), time.Now())
}

// serverLoc resolves a user's IANA timezone, defaulting to the server's local
// timezone when unset or invalid.
func serverLoc(tz string) *time.Location {
	if tz == "" {
		return time.Local
	}
	if loc, err := time.LoadLocation(tz); err == nil {
		return loc
	}
	return time.Local
}

// formatRel renders t relative to now in loc, using calendar days.
func formatRel(t time.Time, loc *time.Location, now time.Time) string {
	tl := t.In(loc)
	nl := now.In(loc)
	ty, tm, td := tl.Date()
	ny, nm, nd := nl.Date()
	dayDiff := int(time.Date(ty, tm, td, 0, 0, 0, 0, loc).
		Sub(time.Date(ny, nm, nd, 0, 0, 0, 0, loc)).Hours() / 24)
	timeStr := tl.Format("3:04pm")
	if dayDiff == 0 {
		return "Today at " + timeStr
	}
	if dayDiff == -1 {
		return "Yesterday at " + timeStr
	}
	if dayDiff > 0 {
		return tl.Format("Jan 2, 2006 at 3:04pm")
	}
	days := -dayDiff
	switch {
	case days < 7:
		return plural(days, "day") + " ago"
	case days < 30:
		return plural(days/7, "week") + " ago"
	case days < 365:
		return plural(days/30, "month") + " ago"
	default:
		return tl.Format("Jan 2, 2006 at 3:04pm")
	}
}

// plural renders "1 day" or "2 days" etc.
func plural(n int, unit string) string {
	u := unit
	if n != 1 {
		u += "s"
	}
	return strconv.Itoa(n) + " " + u
}
