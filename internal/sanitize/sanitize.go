// Package sanitize cleans untrusted HTML before it is stored. Feed summaries
// and plugin-enriched item bodies are rendered as raw HTML in the app, so they
// are sanitized once at ingest rather than on every render.
package sanitize

import "github.com/microcosm-cc/bluemonday"

// policy is the allow-list applied to every stored item body. It keeps the
// formatting, links, images, tables and media a feed can reasonably contain
// and drops everything executable: <script>, <style>, <iframe>, <object>,
// <embed>, <form>, <svg>/<math>, every on* handler, and javascript:/data: URLs.
var policy = buildPolicy()

// HTML returns s with any disallowed markup or attribute stripped. It is safe
// to call on plain text (it round-trips unchanged) and idempotent.
func HTML(s string) string {
	if s == "" {
		return ""
	}
	return policy.Sanitize(s)
}

func buildPolicy() *bluemonday.Policy {
	p := bluemonday.NewPolicy()

	p.AllowElements(
		"p", "br", "hr", "div", "span",
		"strong", "b", "em", "i", "u", "s", "strike", "del", "ins",
		"sub", "sup", "small", "mark", "abbr", "cite", "q", "time",
		"blockquote", "pre", "code", "kbd", "samp", "var",
		"h1", "h2", "h3", "h4", "h5", "h6",
		"ul", "ol", "li", "dl", "dt", "dd",
		"figure", "figcaption", "picture",
		"table", "thead", "tbody", "tfoot", "tr", "th", "td", "caption", "colgroup", "col",
		"details", "summary",
	)

	p.AllowAttrs("href", "title").OnElements("a")
	p.AllowAttrs("src", "alt", "title", "width", "height").OnElements("img")
	p.AllowAttrs("src", "type").OnElements("source")
	p.AllowAttrs("src", "poster", "title", "controls", "width", "height", "preload").OnElements("video", "audio")
	p.AllowAttrs("colspan", "rowspan").OnElements("td", "th")
	p.AllowAttrs("start").OnElements("ol")
	p.AllowAttrs("dir", "lang", "title").Globally()

	p.AllowStandardURLs() // http, https, mailto
	p.RequireParseableURLs(true)
	p.RequireNoFollowOnLinks(false)

	return p
}
