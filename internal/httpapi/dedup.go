package httpapi

import (
	"strings"
	"unicode"

	"github.com/metruzanca/nanoflux/internal/store"
)

// dedupItems collapses a page of items that share a (near-)identical title
// across different feeds into a single row: the newest item survives and the
// others are listed as alternate sources. Items within the same feed are never
// merged, because two distinct posts in one feed can legitimately share a
// title. This is a view-time transformation only — nothing is written back.
func dedupItems(items []store.ItemWithFeed) []store.ItemWithFeed {
	type group struct {
		key   string
		index int // index of the survivor in out
	}
	var groups []group
	out := make([]store.ItemWithFeed, 0, len(items))
	for _, it := range items {
		key := normalizeTitle(it.Title)
		// A saved page (hidden system feed) is never merged into a feed item:
		// it is deliberate, user-held content, not a duplicate to collapse.
		if key == "" || it.FeedIsSystem {
			out = append(out, it)
			continue
		}
		var matched *group
		for i := range groups {
			if fuzzyTitles(groups[i].key, key) {
				matched = &groups[i]
				break
			}
		}
		if matched == nil {
			out = append(out, it)
			groups = append(groups, group{key: key, index: len(out) - 1})
			continue
		}
		surv := &out[matched.index]
		if surv.FeedID == it.FeedID {
			// Distinct post in the same feed — keep both rows.
			out = append(out, it)
			groups = append(groups, group{key: key, index: len(out) - 1})
			continue
		}
		dup := false
		for _, src := range surv.Sources {
			if src.FeedID == it.FeedID {
				dup = true
				break
			}
		}
		if !dup {
			surv.Sources = append(surv.Sources, store.ItemSource{
				FeedID:    it.FeedID,
				FeedTitle: it.FeedTitle,
				Link:      it.Link,
			})
		}
	}
	return out
}

// normalizeTitle folds a title into a comparable key: lowercase, letters and
// digits only (whitespace collapses to single spaces).
func normalizeTitle(s string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			space = false
		} else if !space && b.Len() > 0 {
			b.WriteRune(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}

// fuzzyTitles reports whether two normalized titles refer to the same post:
// identical, within two edits, or within 10% of the shorter length (for longer
// titles). Short, unrelated titles are never merged beyond the two-edit bound.
func fuzzyTitles(a, b string) bool {
	if a == b {
		return true
	}
	d := levenshtein(a, b)
	if d <= 2 {
		return true
	}
	short := len(a)
	if len(b) < short {
		short = len(b)
	}
	return short >= 10 && d <= short/10
}

// levenshtein computes the edit distance between two strings.
func levenshtein(a, b string) int {
	la, lb := len(a), len(b)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	prev := make([]int, lb+1)
	cur := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		cur[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min3(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[lb]
}

func min3(a, b, c int) int {
	if a < b {
		b = a
	}
	if c < b {
		return c
	}
	return b
}
