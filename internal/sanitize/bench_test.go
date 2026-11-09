package sanitize

import (
	"strings"
	"testing"
)

func BenchmarkHTMLPlain(b *testing.B) {
	s := strings.Repeat("plain text with no markup ", 100)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if HTML(s) != s {
			b.Fatal("plain text should round-trip")
		}
	}
}

func BenchmarkHTMLMarkup(b *testing.B) {
	s := `<p>` + strings.Repeat(`<a href="https://example.dev">link</a> <strong>bold</strong> `, 50) + `</p>`
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = HTML(s)
	}
}
