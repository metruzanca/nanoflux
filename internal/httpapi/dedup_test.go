package httpapi

import (
	"testing"

	"github.com/metruzanca/nanoflux/internal/store"
)

func TestNormalizeTitle(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Hello, World!", "hello world"},
		{"  PSA:  New rules  ", "psa new rules"},
		{"Café au Lait", "café au lait"}, // diacritics are kept (letters)
		{"", ""},
		{"!!!", ""},
	}
	for _, tt := range tests {
		if got := normalizeTitle(tt.in); got != tt.want {
			t.Errorf("normalizeTitle(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestFuzzyTitles(t *testing.T) {
	yes := [][2]string{
		{"go 1.26 released", "go 1.26 released"},
		{"psa new rules", normalizeTitle("PSA: New Rules")},                        // punctuation/case folded first
		{"breaking news update", normalizeTitle("breaking news: update")},          // one edit
		{"a moderately long title about things", "a moderately long title about thigns"}, // transposition
	}
	for _, pair := range yes {
		if !fuzzyTitles(pair[0], pair[1]) {
			t.Errorf("fuzzyTitles(%q, %q) should match", pair[0], pair[1])
		}
	}
	no := [][2]string{
		{"hello world", "totally different"},
		{"short", "shorty but not really"},
	}
	for _, pair := range no {
		if fuzzyTitles(pair[0], pair[1]) {
			t.Errorf("fuzzyTitles(%q, %q) should not match", pair[0], pair[1])
		}
	}
}

func item(title string, feedID int64) store.ItemWithFeed {
	return store.ItemWithFeed{
		Item:      store.Item{Title: title, FeedID: feedID},
		FeedTitle: "feed" + string(rune('0'+feedID)),
	}
}

func TestDedupItems(t *testing.T) {
	// Same title across two feeds merges; the newest (first) survives.
	items := []store.ItemWithFeed{
		item("Go 1.26 released", 1),
		item("Go 1.26 released", 2),
	}
	got := dedupItems(items)
	if len(got) != 1 {
		t.Fatalf("dedup returned %d rows, want 1", len(got))
	}
	if len(got[0].Sources) != 1 || got[0].Sources[0].FeedID != 2 {
		t.Fatalf("survivor should list feed 2 as a source: %+v", got[0])
	}

	// Two distinct posts in the SAME feed with the same title stay separate.
	items = []store.ItemWithFeed{
		item("Episode 5", 1),
		item("Episode 5", 1),
		item("Episode 5", 2),
	}
	got = dedupItems(items)
	if len(got) != 2 {
		t.Fatalf("same-feed dupes should stay separate, got %d rows", len(got))
	}
	// First survivor merges feed 2; the second feed-1 row is untouched.
	if len(got[0].Sources) != 1 || got[0].Sources[0].FeedID != 2 {
		t.Fatalf("first row should merge feed 2: %+v", got[0])
	}
	if len(got[1].Sources) != 0 {
		t.Fatalf("second same-feed row should have no sources: %+v", got[1])
	}

	// Distinct titles are untouched.
	items = []store.ItemWithFeed{
		item("Go 1.26 released", 1),
		item("Rust 2.0 announced", 2),
	}
	got = dedupItems(items)
	if len(got) != 2 {
		t.Fatalf("distinct titles should not merge: %d", len(got))
	}

	// Empty title never merges.
	items = []store.ItemWithFeed{item("", 1), item("", 2)}
	if got := dedupItems(items); len(got) != 2 {
		t.Fatalf("empty titles should stay separate: %d", len(got))
	}
}