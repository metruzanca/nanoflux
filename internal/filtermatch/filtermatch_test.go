package filtermatch

import (
	"testing"

	"github.com/metruzanca/nanoflux/internal/store"
)

func TestMatchFields(t *testing.T) {
	f := Fields{
		Title:      "A Sponsored Post",
		Link:       "https://site.dev/promo/1",
		Summary:    "<p>Buy this <b>now</b></p>",
		Categories: []string{"r/golang", "u/sam", "reblog"},
	}
	cases := []struct {
		name string
		rule store.Filter
		want bool
	}{
		{"title substring case-insensitive", store.Filter{Field: "title", Pattern: "sponsored"}, true},
		{"title no match", store.Filter{Field: "title", Pattern: "vacation"}, false},
		{"link substring", store.Filter{Field: "link", Pattern: "/promo/"}, true},
		{"summary uses visible text", store.Filter{Field: "summary", Pattern: "Buy this now"}, true},
		{"summary ignores html tags", store.Filter{Field: "summary", Pattern: "<b>"}, false},
		{"category any match", store.Filter{Field: "category", Pattern: "reblog"}, true},
		{"category miss", store.Filter{Field: "category", Pattern: "r/rust"}, false},
		{"regex", store.Filter{Field: "title", Pattern: "^A Sponsored", IsRegex: true}, true},
		{"unknown field", store.Filter{Field: "nope", Pattern: "x"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Match(tc.rule, f)
			if err != nil {
				t.Fatalf("Match: %v", err)
			}
			if got != tc.want {
				t.Fatalf("Match = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMatchInvalidRegex(t *testing.T) {
	if _, err := Match(store.Filter{Field: "title", Pattern: "(", IsRegex: true}, Fields{Title: "x"}); err == nil {
		t.Fatal("expected a compile error for an invalid regex")
	}
}

func TestDecide(t *testing.T) {
	sponsored := store.Filter{Field: "title", Pattern: "sponsored", Action: ActionDelete}
	saved := store.Filter{Field: "title", Pattern: "draft", Action: ActionMarkRead}
	it := Fields{Title: "A Sponsored Draft"}
	plain := Fields{Title: "A normal post"}

	cases := []struct {
		name  string
		mode  string
		rules []store.Filter
		f     Fields
		want  Decision
	}{
		{"block no match keeps", ModeBlock, []store.Filter{sponsored}, plain, Keep},
		{"block delete drops", ModeBlock, []store.Filter{sponsored}, it, Drop},
		{"block mark read", ModeBlock, []store.Filter{saved}, Fields{Title: "draft notes"}, MarkRead},
		{"block first match wins", ModeBlock, []store.Filter{saved, sponsored}, it, MarkRead},
		{"allow match keeps", ModeAllow, []store.Filter{sponsored}, it, Keep},
		{"allow no match drops", ModeAllow, []store.Filter{sponsored}, plain, Drop},
		{"allow action ignored", ModeAllow, []store.Filter{{Field: "title", Pattern: "sponsored", Action: ActionDelete}}, it, Keep},
		{"allow any rule keeps", ModeAllow, []store.Filter{{Field: "title", Pattern: "nope"}, sponsored}, it, Keep},
		{"allow zero rules keeps", ModeAllow, nil, plain, Keep},
		{"unknown mode is block", "bogus", []store.Filter{sponsored}, plain, Keep},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Decide(tc.mode, tc.rules, tc.f)
			if err != nil {
				t.Fatalf("Decide: %v", err)
			}
			if got != tc.want {
				t.Fatalf("Decide = %v, want %v", got, tc.want)
			}
		})
	}
}
