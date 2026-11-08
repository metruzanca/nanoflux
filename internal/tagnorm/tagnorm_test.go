package tagnorm

import (
	"reflect"
	"testing"

	"golang.org/x/text/unicode/norm"
)

func TestNormalize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"r/golang", "r/golang"},
		{" /R/Cats ", "r/cats"},
		{"R/Cats", "r/cats"},
		{"  u/Some   User ", "u/some user"},
		{"", ""},
		{"/", ""},
		{"  ", ""},
		{"reblog", "reblog"},
	}
	for _, c := range cases {
		if got := Normalize(c.in); got != c.want {
			t.Errorf("Normalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeComposesUnicode(t *testing.T) {
	decomposed := "Cafe\u0301"
	got := Normalize(decomposed)
	if norm.NFC.String(got) != got {
		t.Fatalf("Normalize left a decomposed string: %q", got)
	}
	if got != "café" {
		t.Fatalf("Normalize(%q) = %q, want café", decomposed, got)
	}
}

func TestNormalizeAll(t *testing.T) {
	got := NormalizeAll([]string{"R/Cats", " r/cats ", "", "u/Sam", "//u/sam"})
	want := []string{"r/cats", "u/sam"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizeAll = %v, want %v", got, want)
	}
	if NormalizeAll(nil) != nil {
		t.Fatal("NormalizeAll(nil) should stay nil")
	}
	if got := NormalizeAll([]string{"", "  "}); got != nil {
		t.Fatalf("NormalizeAll of blanks = %v, want nil", got)
	}
}

func TestLabelKeepsCase(t *testing.T) {
	if got := Label("  Metru   Dev "); got != "Metru Dev" {
		t.Fatalf("Label = %q, want %q", got, "Metru Dev")
	}
	if got := Label("Cafe\u0301"); got != "Café" {
		t.Fatalf("Label did not compose Unicode: %q", got)
	}
	if got := Label(""); got != "" {
		t.Fatalf("Label(\"\") = %q", got)
	}
}

func TestUsername(t *testing.T) {
	if got := Username("  Alice  "); got != "alice" {
		t.Fatalf("Username = %q, want alice", got)
	}
	if got := Username(""); got != "" {
		t.Fatalf("Username(\"\") = %q", got)
	}
}
