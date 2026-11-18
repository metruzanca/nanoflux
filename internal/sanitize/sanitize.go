// Package sanitize cleans untrusted HTML before it is stored. Feed summaries
// and plugin-enriched item bodies are rendered as raw HTML in the app, so they
// are sanitized once at ingest rather than on every render.
package sanitize

import (
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"golang.org/x/net/html"
)

// policy is the allow-list applied to every stored item body. It keeps the
// formatting, links, images, tables and media a feed can reasonably contain
// and drops everything executable: <script>, <style>, <iframe>, <object>,
// <embed>, <form>, <svg>/<math>, every on* handler, and javascript:/data: URLs.
var policy = buildPolicy()

// HTML returns s with any disallowed markup or attribute stripped. It is safe
// to call on plain text (it round-trips unchanged) and idempotent. Text with no
// markup is returned untouched, which makes a poll of a text-only feed cheap.
func HTML(s string) string {
	if s == "" || !strings.Contains(s, "<") {
		return s
	}
	return policy.Sanitize(s)
}

// Text returns the visible text of s with markup removed and character
// references decoded. Feed titles are HTML too, so rendering a title verbatim
// would show its literal tags ("A <b>bold</b> title"). It is safe on plain
// text, including a bare "<" that is not a tag ("5 < 10 things").
//
// Block-level elements and <br> become a space, so "<p>one</p><p>two</p>"
// reads as "one two" instead of "onetwo". Whitespace is collapsed to single
// spaces, matching how HTML renders it.
func Text(s string) string {
	if s == "" {
		return s
	}
	if !strings.Contains(s, "<") {
		// Plain text can still carry a character reference (a feed that
		// double-escaped its title); decode it but leave the text otherwise
		// untouched.
		if strings.Contains(s, "&") {
			return html.UnescapeString(s)
		}
		return s
	}
	doc, err := html.Parse(strings.NewReader(s))
	if err != nil {
		return s
	}
	var b strings.Builder
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		switch n.Type {
		case html.TextNode:
			b.WriteString(n.Data)
			return
		case html.ElementNode:
			switch n.Data {
			case "script", "style", "noscript", "template":
				return
			}
			if textSeparatorElements[n.Data] {
				b.WriteByte(' ')
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode && textSeparatorElements[n.Data] {
			b.WriteByte(' ')
		}
	}
	for c := doc.FirstChild; c != nil; c = c.NextSibling {
		walk(c)
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// textSeparatorElements are the elements whose boundaries separate words in
// rendered text. Without them a paragraph or line break would join the text on
// either side.
var textSeparatorElements = map[string]bool{
	"address": true, "article": true, "aside": true, "blockquote": true,
	"br": true, "dd": true, "div": true, "dl": true, "dt": true,
	"figcaption": true, "figure": true, "footer": true, "form": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"header": true, "hr": true, "li": true, "main": true, "nav": true,
	"ol": true, "p": true, "pre": true, "section": true, "table": true,
	"tbody": true, "td": true, "tfoot": true, "th": true, "thead": true,
	"tr": true, "ul": true,
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
