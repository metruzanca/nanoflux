package web

import (
	"embed"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"

	"golang.org/x/net/html"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

// Page is the data passed to every rendered template.
type Page struct {
	Title string
	User  store.User
	Data  any
}

var funcMap = template.FuncMap{
	"stripHTML": stripHTML,
	"timeFmt":   timeFmt,
	"has":       has,
	"isVideo": func(link string) bool {
		return YoutubeEmbedURL(link) != ""
	},
	"isImagePost": isImagePost,
	"isLinkPost":  isLinkPost,
	"isGallery":   isGallery,
	"galleryThumb": galleryThumb,
	"sourceIcon":  sourceIcon,
	"initial":     initial,
	"favIcon":     favIcon,
}

// initial returns the uppercased first character of a name, for the default
// avatar shown when a user has no profile picture.
func initial(s string) string {
	for _, r := range s {
		return strings.ToUpper(string(r))
	}
	return "?"
}

// favIcon renders a star for the favorite toggle, filled when the item is a
// favorite. Fill comes from currentColor so CSS can tint it.
func favIcon(fav bool) template.HTML {
	fill := "none"
	cls := "star"
	if fav {
		fill = "currentColor"
		cls = "star on"
	}
	return template.HTML(`<svg class="` + cls + `" viewBox="0 0 24 24" width="16" height="16" fill="` + fill + `" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><polygon points="12 2 15.09 8.26 22 9.27 17 14.14 18.18 21.02 12 17.77 5.82 21.02 7 14.14 2 9.27 8.91 8.26 12 2"/></svg>`)
}

func has(id int64, ids []int64) bool {
	for _, i := range ids {
		if i == id {
			return true
		}
	}
	return false
}

var (
	globeIcon = template.HTML(`<svg class="src-icon" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="10"/><line x1="2" y1="12" x2="22" y2="12"/><path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"/></svg>`)
)

// sourceIcon renders a source-type icon for a feed URL. Icons are resolved
// server-side by /icons/{domain}, which prefers the user's cached custom icon
// for the domain and falls back to the built-in X/YouTube/globe icons.
func sourceIcon(rawurl string) template.HTML {
	u, err := url.Parse(rawurl)
	if err != nil || u.Hostname() == "" {
		return globeIcon
	}
	return template.HTML(`<img class="src-icon" src="/icons/` +
		url.PathEscape(strings.ToLower(u.Hostname())) +
		`" width="16" height="16" alt="">`)
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

var tmpl *template.Template

func init() {
	tmpl = template.Must(
		template.New("").Funcs(funcMap).ParseFS(templateFS, "templates/*.html"),
	)
}

// Render executes the named template (e.g. "home") against p.
func Render(w http.ResponseWriter, name string, p Page) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	return tmpl.ExecuteTemplate(w, name, p)
}

// RenderFragment executes a named partial against data without a Page wrapper.
func RenderFragment(w http.ResponseWriter, name string, data any) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	return tmpl.ExecuteTemplate(w, name, data)
}

// Static serves embedded assets (css, htmx.js) at /static/.
func Static() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	return http.StripPrefix("/static/", http.FileServerFS(sub))
}

// isLinkPost reports whether an item is a reddit "link post" — its primary
// content is an external site (imgur, a news article) previewed by
// reddit's external-preview thumbnail.
func isLinkPost(imageURL string) bool {
	if imageURL == "" {
		return false
	}
	u, err := url.Parse(imageURL)
	if err != nil {
		return false
	}
	return u.Hostname() == "external-preview.redd.it"
}

// isImagePost reports whether an item's primary content is a single image:
// the summary is an <img> (optionally wrapped in a link) with no real text
// beyond the image's alt text. imageURL is required so items whose thumbnail
// comes from feed metadata but whose body is text are not treated as images.
// Reddit's external-preview.redd.it thumbnails are excluded: they preview a
// link post's external destination, not an in-post image.
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

// isGallery reports whether an item is a reddit gallery post, from its stored
// thumbnail alone: galleries use a small square cover in the feed
// (width=140&height=140&crop=1:1) rather than the natural-aspect image-post
// crop.
func isGallery(imageURL string) bool {
	u, err := url.Parse(imageURL)
	if err != nil || u.Hostname() != "preview.redd.it" {
		return false
	}
	q := u.Query()
	return q.Get("crop") == "1:1,smart"
}

// galleryThumb returns the full-res first image of a reddit gallery post (the
// stored thumbnail is a tiny square cover), or "" when the item is not a
// gallery.
func galleryThumb(imageURL string) string {
	if !isGallery(imageURL) {
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

// stripHTML extracts plain text from feed-provided HTML.
func stripHTML(s string) string {
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

// timeFmt renders a stored UTC timestamp relative to the user's timezone.
// tz is an IANA timezone name; empty means the server's local time. Labels:
// "Today at 3:04pm", "Yesterday at 3:04pm", "3 days ago", "2 weeks ago",
// "3 months ago", or an absolute "Sep 2, 2026 at 3:04pm" for past years and
// future timestamps.
func timeFmt(tz, s string) string {
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
