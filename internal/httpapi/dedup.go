package httpapi

import (
	"strings"
	"unicode"

	"github.com/metruzanca/nanoflux/internal/store"
)

// dedupItems collapses a page of items that are the same content across
// different feeds into a single row: the newest item survives and the others
// are listed as alternate sources. Two items match when a plugin decorator gave
// them the same DedupeKey (the same content reposted under different titles,
// e.g. a reddit link post crossposted to several subreddits), or when their
// titles are near-identical. Items within the same feed are never merged,
// because two distinct posts in one feed can legitimately share a title. This
// is a view-time transformation only — nothing is written back.
func dedupItems(items []store.ItemWithFeed) []store.ItemWithFeed {
	type group struct {
		key   string // normalized survivor title, "" when none
		link  string // survivor's plugin dedupe key, "" when none
		index int    // index of the survivor in out
	}
	var groups []group
	out := make([]store.ItemWithFeed, 0, len(items))
	for _, it := range items {
		// A saved page (hidden system feed) is never merged into a feed item:
		// it is deliberate, user-held content, not a duplicate to collapse.
		if it.FeedIsSystem {
			out = append(out, it)
			continue
		}
		titleKey := normalizeTitle(it.Title)
		linkKey := it.DedupeKey
		var matched *group
		for i := range groups {
			// A plugin's content identity wins: it merges the same content
			// reposted under different titles, including cross-feed posts (a
			// reddit CrossKey marks distinct posts that may still share an
			// external link).
			if linkKey != "" && groups[i].link != "" && groups[i].link == linkKey {
				matched = &groups[i]
				break
			}
			// Fuzzy title merging applies only to items without a cross-feed
			// key: two such rows that merely share a title are distinct posts,
			// so collapsing them by title would be wrong.
			if titleKey != "" && it.CrossKey == "" && groups[i].key != "" && fuzzyTitles(groups[i].key, titleKey) {
				matched = &groups[i]
				break
			}
		}
		if matched == nil {
			out = append(out, it)
			groups = append(groups, group{key: titleKey, link: linkKey, index: len(out) - 1})
			continue
		}
		surv := &out[matched.index]
		if surv.FeedID == it.FeedID {
			// Distinct post in the same feed — keep both rows.
			out = append(out, it)
			groups = append(groups, group{key: titleKey, link: linkKey, index: len(out) - 1})
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
		// A title match may carry a content identity too; fold both in so later
		// items with the same key cluster with this row.
		if matched.link == "" {
			matched.link = linkKey
		}
		if matched.key == "" {
			matched.key = titleKey
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
