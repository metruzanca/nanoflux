package urlmap

import (
	"strings"
	"testing"
)

func TestApply(t *testing.T) {
	m, err := Compile(`abc\.com/(?P<user>[^/]+)`, `{user}.abc.com/feed`)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"https://abc.com/john", "john.abc.com/feed", true},
		{"abc.com/john", "john.abc.com/feed", true},
		{"http://abc.com/john", "john.abc.com/feed", true},
		{"https://abc.com/john/", "john.abc.com/feed", true},
		// Case-insensitive host, but the capture keeps the original case.
		{"https://ABC.com/Jane", "Jane.abc.com/feed", true},
		{"https://abc.com/john/photos", "", false}, // whole string must match
		{"https://abc.com/", "", false},            // empty capture
		{"https://other.com/john", "", false},
	}
	for _, c := range cases {
		got, ok := m.Apply(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("Apply(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestApplyMultipleGroups(t *testing.T) {
	m, err := Compile(`(?P<lang>[a-z]{2})\.abc\.com/(?P<user>[^/]+)`, `{user}.{lang}.abc.com/feed`)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := m.Apply("https://en.abc.com/jane")
	if !ok || got != "jane.en.abc.com/feed" {
		t.Fatalf("Apply = %q, %v", got, ok)
	}
}

func TestApplySchemeInTemplate(t *testing.T) {
	m, err := Compile(`localhost:8080/(?P<user>[^/]+)`, `http://localhost:8080/feed/{user}`)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := m.Apply("http://localhost:8080/jane")
	if !ok || got != "http://localhost:8080/feed/jane" {
		t.Fatalf("Apply = %q, %v", got, ok)
	}
}

func TestCompileErrors(t *testing.T) {
	cases := []struct {
		pattern  string
		template string
		want     string
	}{
		{"", "{user}/feed", "pattern is required"},
		{"abc.com", "", "feed url is required"},
		{`abc\.com/(?P<user>[^/]`, "{user}/feed", "invalid pattern"},
		{"abc.com/feed", "feed", "pattern must include a named group"},
		{`abc\.com/(?P<user>[^/]+)`, "{user}/{missing}.feed", "references {missing}"},
	}
	for _, c := range cases {
		_, err := Compile(c.pattern, c.template)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("Compile(%q, %q) err = %v; want containing %q", c.pattern, c.template, err, c.want)
		}
	}
}

func TestCompileValidWithoutAnchors(t *testing.T) {
	// Patterns without anchors must still match the whole host/path.
	m, err := Compile(`abc\.com/(?P<user>[^/]+)`, `{user}.abc.com/feed`)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Apply("https://xabc.com/john"); ok {
		t.Fatal("substring match should be rejected")
	}
}
