package web

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"golang.org/x/net/html"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/feedparse"
	"github.com/metruzanca/nanoflux/internal/store"
)

// markdown renders plugin documentation. Raw HTML is left disabled (goldmark's
// default), so an external plugin's readme cannot inject markup; the output is
// safe to render as HTML. Tables, strikethrough, task lists and autolinks are
// enabled because a readme may reasonably use them.
var markdown = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithParserOptions(parser.WithAutoHeadingID()),
)

// Markdown renders src as HTML, for plugin documentation set in Markdown.
//
// Anchors are marked as external (class, target, rel, and the project's ↗ via
// a.external), matching how every other outbound link in the app renders.
// Goldmark escapes raw HTML, so the result is safe for template.HTML.
func Markdown(src string) template.HTML {
	if strings.TrimSpace(src) == "" {
		return ""
	}
	var buf bytes.Buffer
	if err := markdown.Convert([]byte(src), &buf); err != nil {
		return ""
	}
	doc, err := html.Parse(&buf)
	if err != nil {
		return template.HTML(buf.String())
	}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			setAttr(n, "class", "external")
			setAttr(n, "target", "_blank")
			setAttr(n, "rel", "noopener noreferrer")
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	var out bytes.Buffer
	for c := doc.FirstChild; c != nil; c = c.NextSibling {
		if err := html.Render(&out, c); err != nil {
			return template.HTML(buf.String())
		}
	}
	return template.HTML(out.String())
}

// setAttr sets an attribute on n, replacing any existing value.
func setAttr(n *html.Node, key, val string) {
	for i := range n.Attr {
		if n.Attr[i].Key == key {
			n.Attr[i].Val = val
			return
		}
	}
	n.Attr = append(n.Attr, html.Attribute{Key: key, Val: val})
}

//go:embed static/*
var staticFS embed.FS

// ReadStatic returns the bytes of an embedded asset under static/ (e.g.
// "sw.js", "manifest.webmanifest"), for serving at a non-/static/ route.
func ReadStatic(name string) ([]byte, error) {
	return staticFS.ReadFile("static/" + name)
}

// Render executes a templ component against w.
func Render(w http.ResponseWriter, r *http.Request, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	c.Render(r.Context(), w)
}

// FormatBytes renders a byte count as a human-readable size ("2.4 MB").
func FormatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n >= div*unit && exp < len("KMGTPE")-1 {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// FormatDuration renders a media runtime in seconds as a clock string:
// "5:41" under an hour, "1:02:03" at or above it. Zero means unknown and
// returns "", so callers can treat an empty result as "no duration to show".
func FormatDuration(sec int) string {
	if sec <= 0 {
		return ""
	}
	h, m, s := sec/3600, (sec%3600)/60, sec%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
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

// LinkLabel returns the display text for an author link: its label when set,
// otherwise the URL's hostname (without "www."), falling back to the raw URL.
func LinkLabel(label, rawurl string) string {
	if strings.TrimSpace(label) != "" {
		return label
	}
	u, err := url.Parse(rawurl)
	if err != nil || u.Hostname() == "" {
		return rawurl
	}
	return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
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

// IsSingleImagePost reports whether an item's primary content is a single image:
// the summary is exactly one <img> (optionally wrapped in a link) with no real
// text beyond the image's alt text. imageURL is required so items whose
// thumbnail comes from feed metadata but whose body is text are not treated as
// images. Summaries with more than one image are NOT single-image posts — they
// are multi-image posts whose full body should be rendered, not collapsed to one
// thumbnail. This is a generic baseline; a plugin may override the item's kind.
func IsSingleImagePost(summary, imageURL, title string) bool {
	if imageURL == "" {
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

// EnclosureKind reports how an enclosure should render: "image", "audio",
// "video", "hls", or "" when it is just a file link. It delegates to
// feedparse.ResolveEnclosureKind so the renderer, the image cache, and the
// plugin-declared kind all agree; a declared kind wins over the MIME
// type/extension.
func EnclosureKind(e store.Enclosure) string {
	return feedparse.ResolveEnclosureKind(e.Kind, e.URL, e.MIMEType)
}

// enclosurePath returns the path portion of an enclosure URL, or "" when it
// cannot be parsed. Path.Ext on the raw URL string would treat the query string
// as part of the filename, so lookups must go through here.
func enclosurePath(rawurl string) string {
	u, err := url.Parse(rawurl)
	if err != nil || u.Path == "" {
		return ""
	}
	return u.Path
}

// ImageEnclosures returns an item's image enclosures in order, for inline
// rendering instead of a bare download link.
func ImageEnclosures(encs []store.Enclosure) []store.Enclosure {
	var imgs []store.Enclosure
	for _, e := range encs {
		if EnclosureKind(e) == "image" {
			imgs = append(imgs, e)
		}
	}
	return imgs
}

// EnclosureLabel renders a display name for an enclosure's download link:
// its explicit title, else the filename from the URL.
func EnclosureLabel(e store.Enclosure) string {
	if strings.TrimSpace(e.Title) != "" {
		return e.Title
	}
	name := path.Base(e.URL)
	if name == "." || name == "/" || name == "" {
		return e.URL
	}
	return name
}

// BodyHasImage reports whether an item's HTML body contains an <img> element.
// Image enclosures render inline only when the body does not already show an
// image, so a description that embeds the same image isn't duplicated.
func BodyHasImage(htmlBody string) bool {
	if !strings.Contains(htmlBody, "<") {
		return false
	}
	doc, err := html.Parse(strings.NewReader(htmlBody))
	if err != nil {
		return false
	}
	var has bool
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if has {
			return
		}
		if n.Type == html.ElementNode && n.Data == "img" {
			has = true
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	for c := doc.FirstChild; c != nil; c = c.NextSibling {
		walk(c)
	}
	return has
}

// isImageHref reports whether href looks like it points at an image, by
// extension (used to recognise the common "thumbnail wrapped in a link to the
// full-size image" pattern).
func isImageHref(href string) bool {
	u, err := url.Parse(href)
	if err != nil || u.Path == "" {
		return false
	}
	switch strings.ToLower(path.Ext(u.Path)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".avif", ".svg", ".bmp":
		return true
	}
	return false
}

// ProxiedImageURL routes an absolute http(s) image URL through the app's /img
// proxy. Some hosts (Instagram's CDN) answer with
// Cross-Origin-Resource-Policy: same-origin, which blocks a direct browser
// hotlink; fetching server-side and re-serving from our own origin avoids it.
// Non-http(s) sources (data: URIs, relative paths) are returned unchanged, as
// are URLs that cannot be parsed.
func ProxiedImageURL(rawurl string) string {
	if rawurl == "" {
		return ""
	}
	u, err := url.Parse(rawurl)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return rawurl
	}
	return "/img?u=" + url.QueryEscape(rawurl)
}

// CachedImageURL routes a cached image through the app's authenticated /cache
// route. key is the object-storage key recorded at poll time; remote is the
// original URL, carried along so a purged cache can fall back to it. An empty
// key returns remote unchanged, so an uncached item keeps its normal path.
func CachedImageURL(key, remote string) string {
	if key == "" {
		return remote
	}
	p := "/cache/" + key
	if remote != "" {
		p += "?u=" + url.QueryEscape(remote)
	}
	return p
}

// UpgradeImageSrcs rewrites an item body so that every <img> wrapped in an
// <a href> pointing at an image uses that href as its src. Many sites (e.g.
// Blogger) show a downscaled <img> inside a link to the full-size original;
// rendering the linked image inline lets users see the full quality without
// opening the post. Non-image links and bare <img>s are left untouched.
func UpgradeImageSrcs(htmlBody string) string {
	return rewriteImageSrcs(htmlBody, nil)
}

// ProxyImageSrcs rewrites every <img> in an item body through the /img proxy
// (in addition to the link upgrade UpgradeImageSrcs performs), so bodies whose
// images block cross-origin hotlinking still render.
func ProxyImageSrcs(htmlBody string) string {
	return rewriteImageSrcs(htmlBody, ProxiedImageURL)
}

// rewriteImageSrcs is the shared body walker behind UpgradeImageSrcs and
// ProxyImageSrcs. A nil proxy leaves srcs as-is; otherwise it is applied to the
// upgraded src so both the link upgrade and proxying compose.
func rewriteImageSrcs(htmlBody string, proxy func(string) string) string {
	if !strings.Contains(htmlBody, "<") {
		return htmlBody
	}
	doc, err := html.Parse(strings.NewReader(htmlBody))
	if err != nil {
		return htmlBody
	}
	setSrc := func(img *html.Node, val string) {
		if proxy != nil {
			val = proxy(val)
		}
		for i := range img.Attr {
			if img.Attr[i].Key == "src" {
				img.Attr[i].Val = val
				return
			}
		}
		img.Attr = append(img.Attr, html.Attribute{Key: "src", Val: val})
	}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			href := ""
			for _, a := range n.Attr {
				if a.Key == "href" {
					href = a.Val
					break
				}
			}
			if isImageHref(href) {
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					if c.Type == html.ElementNode && c.Data == "img" {
						setSrc(c, href)
					}
				}
			}
		}
		if n.Type == html.ElementNode && n.Data == "img" && proxy != nil {
			// A bare <img> (or an image under a non-image link) still gets
			// proxied; its existing src is upgraded in place.
			for _, a := range n.Attr {
				if a.Key == "src" {
					setSrc(n, a.Val)
					break
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	for c := doc.FirstChild; c != nil; c = c.NextSibling {
		walk(c)
	}
	// html.Parse wraps the fragment in <html><head><body>, so render only the
	// <body> children to return a clean fragment (no stray wrapper tags in the
	// modal).
	var b strings.Builder
	for c := doc.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode || c.Data != "html" {
			continue
		}
		for b2 := c.FirstChild; b2 != nil; b2 = b2.NextSibling {
			if b2.Type != html.ElementNode || b2.Data != "body" {
				continue
			}
			for cc := b2.FirstChild; cc != nil; cc = cc.NextSibling {
				html.Render(&b, cc)
			}
			return b.String()
		}
	}
	for c := doc.FirstChild; c != nil; c = c.NextSibling {
		html.Render(&b, c)
	}
	return b.String()
}

// BestImageURL returns the highest-quality src for an image post's primary
// image: when the summary wraps the image in a link to a full-size image, that
// linked URL is returned; otherwise imageURL itself is kept.
func BestImageURL(summary, imageURL string) string {
	if !strings.Contains(summary, "<") {
		return imageURL
	}
	doc, err := html.Parse(strings.NewReader(summary))
	if err != nil {
		return imageURL
	}
	var walk func(*html.Node) string
	walk = func(n *html.Node) string {
		if n.Type == html.ElementNode && n.Data == "a" {
			href := ""
			for _, a := range n.Attr {
				if a.Key == "href" {
					href = a.Val
					break
				}
			}
			if isImageHref(href) {
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					if c.Type == html.ElementNode && c.Data == "img" {
						return href
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if u := walk(c); u != "" {
				return u
			}
		}
		return ""
	}
	for c := doc.FirstChild; c != nil; c = c.NextSibling {
		if u := walk(c); u != "" {
			return u
		}
	}
	return imageURL
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

// StaleFeed describes a feed that has stopped posting for a while.
type StaleFeed struct {
	Days      int  // days since the feed's newest item
	Abandoned bool // >= 30 days: the account may be abandoned
}

// StaleFeedFor reports whether a feed is quiet (its newest item is at least 7
// days old) and how long it has been. ok is false when the feed has no items
// yet or is still active. Distinct from a feed error: there is no failure,
// just no new posts.
func StaleFeedFor(lastItemAt string) (StaleFeed, bool) {
	return staleFeedFor(lastItemAt, db.FormatTime(time.Now()))
}

func staleFeedFor(lastItemAt, now string) (StaleFeed, bool) {
	if lastItemAt == "" {
		return StaleFeed{}, false
	}
	t, err := db.ParseTime(lastItemAt)
	if err != nil {
		return StaleFeed{}, false
	}
	n, err := db.ParseTime(now)
	if err != nil {
		return StaleFeed{}, false
	}
	d := n.Sub(t)
	if d < 7*24*time.Hour {
		return StaleFeed{}, false
	}
	return StaleFeed{Days: int(d / (24 * time.Hour)), Abandoned: d >= 30*24*time.Hour}, true
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

// Until renders how long from now until a stored UTC timestamp, for near-future
// deadlines (e.g. a rate-limit retry): "in 45s", "in 3m", "in 2h". It returns
// "" when s is unparseable or already past.
func Until(s string) string {
	t, err := db.ParseTime(s)
	if err != nil {
		return ""
	}
	d := time.Until(t)
	if d <= 0 {
		return ""
	}
	switch {
	case d < time.Minute:
		return "in " + plural(int(d.Seconds()+0.5), "s")
	case d < time.Hour:
		return "in " + plural(int(d.Minutes()+0.5), "m")
	default:
		return "in " + plural(int(d.Hours()+0.5), "h")
	}
}

// PostFrequency renders an approximate posting cadence from an average gap in
// seconds (e.g. "≈3/day", "≈2/week", "≈1/month"). It returns "" when the gap
// is not positive. The reciprocal is expressed in whichever unit reads most
// naturally: days for sub-weekly, weeks for sub-monthly, months otherwise.
func PostFrequency(avgGapSec float64) string {
	if avgGapSec <= 0 {
		return ""
	}
	perDay := 86400 / avgGapSec
	switch {
	case perDay >= 1:
		return "≈" + trimFloat(perDay) + "/day"
	case perDay*7 >= 1:
		return "≈" + trimFloat(perDay*7) + "/week"
	case perDay*30 >= 1:
		return "≈" + trimFloat(perDay*30) + "/month"
	default:
		return "≈" + trimFloat(perDay*365) + "/year"
	}
}

// trimFloat renders a frequency with at most one decimal, dropping a trailing
// ".0" ("3", "1.5").
func trimFloat(f float64) string {
	if f >= 10 {
		return strconv.FormatFloat(f, 'f', 0, 64)
	}
	return strings.TrimSuffix(strconv.FormatFloat(f, 'f', 1, 64), ".0")
}

// AverageGapSeconds estimates the average spacing between stored item times (in
// seconds). It sorts them, drops duplicates/out-of-order entries, and averages
// the positive gaps. ok is false when fewer than two valid, distinct times are
// available.
func AverageGapSeconds(times []string) (sec float64, ok bool) {
	var parsed []time.Time
	for _, s := range times {
		t, err := db.ParseTime(s)
		if err != nil {
			continue
		}
		parsed = append(parsed, t)
	}
	if len(parsed) < 2 {
		return 0, false
	}
	sort.Slice(parsed, func(i, j int) bool { return parsed[i].Before(parsed[j]) })
	var sum time.Duration
	var n int
	prev := parsed[0]
	for _, t := range parsed[1:] {
		d := t.Sub(prev)
		prev = t
		if d <= 0 {
			continue
		}
		sum += d
		n++
	}
	if n == 0 {
		return 0, false
	}
	return sum.Seconds() / float64(n), true
}
