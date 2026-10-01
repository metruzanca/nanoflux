package imageutil

import "testing"

var pngHead = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}

func TestSniff(t *testing.T) {
	cases := []struct {
		name     string
		declared string
		head     []byte
		wantOK   bool
		wantType string
	}{
		{"declared png", "image/png", nil, true, "image/png"},
		{"declared png with charset", "image/png; charset=binary", nil, true, "image/png"},
		{"sniffed png", "", pngHead, true, "image/png"},
		{"svg declared", "image/svg+xml", []byte("<svg></svg>"), false, ""},
		{"svg sniffed as xml", "", []byte(`<?xml version="1.0"?><svg></svg>`), false, ""},
		{"html", "text/html", []byte("<html></html>"), false, ""},
		{"octet-stream text", "application/octet-stream", []byte("hello"), false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := Sniff(c.declared, c.head)
			if ok != c.wantOK {
				t.Fatalf("Sniff(%q) ok = %v, want %v", c.declared, ok, c.wantOK)
			}
			if ok && got != c.wantType {
				t.Fatalf("Sniff(%q) = %q, want %q", c.declared, got, c.wantType)
			}
		})
	}
}
