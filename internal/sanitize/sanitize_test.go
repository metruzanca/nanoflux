package sanitize

import (
	"strings"
	"testing"
)

func TestHTMLStripsActiveContent(t *testing.T) {
	cases := []struct {
		name, in, mustNot string
	}{
		{"script", `<p>hi</p><script>alert(1)</script>`, "<script"},
		{"event handler", `<img src="x" onerror="alert(1)">`, "onerror"},
		{"javascript url", `<a href="javascript:alert(1)">x</a>`, "javascript:"},
		{"iframe", `<iframe src="https://evil.test"></iframe>`, "<iframe"},
		{"style", `<style>body{display:none}</style>`, "<style"},
		{"svg", `<svg onload="alert(1)"></svg>`, "<svg"},
		{"form", `<form action="https://evil.test"><input name="x"></form>`, "<form"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := HTML(c.in)
			if strings.Contains(strings.ToLower(got), c.mustNot) {
				t.Fatalf("sanitized output still contains %q: %q", c.mustNot, got)
			}
		})
	}
}

func TestHTMLKeepsSafeMarkup(t *testing.T) {
	in := `<p>hello <strong>world</strong> <a href="https://example.com">link</a></p>` +
		`<img src="https://example.com/a.png" alt="a">` +
		`<ul><li>one</li></ul>`
	got := HTML(in)
	for _, want := range []string{"<strong>", `href="https://example.com"`, `<img`, `src="https://example.com/a.png"`, "<li>"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q to survive sanitizing: %q", want, got)
		}
	}
}

func TestHTMLPlainTextUnchanged(t *testing.T) {
	if got := HTML("just text"); got != "just text" {
		t.Fatalf("plain text changed: %q", got)
	}
	if got := HTML(""); got != "" {
		t.Fatalf("empty changed: %q", got)
	}
}

func TestTextStripsMarkup(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plain text", "just text", "just text"},
		{"inline markup", `A <b>bold</b> title`, "A bold title"},
		{"character references", `Tom &amp; Jerry &#8217;s`, "Tom & Jerry ’s"},
		{"block boundaries", `<p>one</p><p>two</p>`, "one two"},
		{"line break", `line<br>break`, "line break"},
		{"markup only", `<img src="https://example.test/a.png">`, ""},
		{"literal angle bracket", `5 < 10 things`, "5 < 10 things"},
		{"script is dropped", `<script>alert(1)</script>hi`, "hi"},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Text(c.in); got != c.want {
				t.Fatalf("Text(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
