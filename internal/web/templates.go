package web

import (
	"embed"
	"html/template"
	"io/fs"
	"net/http"

	"github.com/metruzanca/rss/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

// Page is the data passed to every rendered template.
type Page struct {
	Title string
	User  store.User
	Any   any
}

var tmpl *template.Template

func init() {
	tmpl = template.Must(template.ParseFS(templateFS, "templates/*.html"))
}

// Render executes the named template (e.g. "base") against p.
func Render(w http.ResponseWriter, name string, p Page) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	return tmpl.ExecuteTemplate(w, name, p)
}

// Static serves embedded assets (css, htmx.js) at /static/.
func Static() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	return http.StripPrefix("/static/", http.FileServerFS(sub))
}
