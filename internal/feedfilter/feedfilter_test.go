package feedfilter

import "testing"

func TestAllowed(t *testing.T) {
	fields := Fields{
		Title:      "Hello World",
		Link:       "https://example.com/post/1",
		Summary:    "a post about <b>go</b>",
		Categories: []string{"r/golang", "u/sam"},
	}
	cat := Rule{Field: FieldCategory, Op: OpIs, Value: "r/rust"}

	cases := []struct {
		name    string
		mode    string
		combine string
		rules   []Rule
		want    bool
	}{
		{"off keeps everything", ModeOff, CombineAny, []Rule{cat}, true},
		{"no rules keeps everything", ModeBlock, CombineAny, nil, true},
		{"block list hides a match", ModeBlock, CombineAny, []Rule{cat}, true},
		{"block list keeps a non-match", ModeBlock, CombineAny, []Rule{{Field: FieldCategory, Op: OpIs, Value: "r/other"}}, true},
		{"allow list keeps a match", ModeAllow, CombineAny, []Rule{{Field: FieldCategory, Op: OpContains, Value: "golang"}}, true},
		{"allow list hides a non-match", ModeAllow, CombineAny, []Rule{cat}, false},
		{"any: one rule matching keeps", ModeAllow, CombineAny, []Rule{cat, {Field: FieldTitle, Op: OpContains, Value: "hello"}}, true},
		{"all: one rule failing hides", ModeAllow, CombineAll, []Rule{cat, {Field: FieldTitle, Op: OpContains, Value: "hello"}}, false},
		{"block any hides on first match", ModeBlock, CombineAny, []Rule{{Field: FieldTitle, Op: OpContains, Value: "world"}}, false},
		{"block all hides only when both match", ModeBlock, CombineAll, []Rule{{Field: FieldTitle, Op: OpContains, Value: "world"}, cat}, true},
		{"is is case-insensitive whole value", ModeBlock, CombineAny, []Rule{{Field: FieldTitle, Op: OpIs, Value: "hello world"}}, false},
		{"starts_with", ModeBlock, CombineAny, []Rule{{Field: FieldLink, Op: OpStartsWith, Value: "https://example"}}, false},
		{"regex", ModeBlock, CombineAny, []Rule{{Field: FieldSummary, Op: OpRegex, Value: "about\\s+<b>"}}, false},
		{"invalid regex matches nothing", ModeBlock, CombineAny, []Rule{{Field: FieldTitle, Op: OpRegex, Value: "("}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Allowed(c.mode, c.combine, c.rules, fields); got != c.want {
				t.Fatalf("Allowed = %v, want %v", got, c.want)
			}
		})
	}
}
