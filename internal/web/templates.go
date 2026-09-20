package web

import (
	"embed"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"strings"

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
}

func has(id int64, ids []int64) bool {
	for _, i := range ids {
		if i == id {
			return true
		}
	}
	return false
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

// timeFmt formats a stored UTC timestamp for display.
func timeFmt(s string) string {
	t, err := db.ParseTime(s)
	if err != nil {
		return s
	}
	return t.Format("Jan 2, 2006 15:04")
}
