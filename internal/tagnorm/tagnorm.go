// Package tagnorm canonicalizes item categories (tags) and user-facing labels.
//
// Categories power exact-match filters, saved views and author allow/block
// rules, so every producer and every filter value must agree on one spelling.
// Normalize is that spelling: Unicode NFC, trimmed, no leading slash, internal
// whitespace collapsed, and lowercase. A tag is an identifier, not display
// text, so its case is never meaningful.
package tagnorm

import (
	"strings"

	"golang.org/x/text/unicode/norm"
)

// Normalize returns the canonical form of a tag. Empty input (or input that is
// only a leading slash and whitespace) returns "".
func Normalize(raw string) string {
	s := norm.NFC.String(strings.TrimSpace(raw))
	s = strings.TrimLeft(s, "/")
	s = strings.Join(strings.Fields(s), " ")
	s = strings.ToLower(s)
	// Lowercasing can produce a decomposed sequence for some scripts, so
	// compose once more.
	return norm.NFC.String(s)
}

// NormalizeAll normalizes each value, drops empties and case-insensitive
// duplicates, and preserves the first spelling's order. It returns nil for an
// empty result so callers store an empty slice, not []string{""}.
func NormalizeAll(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		tag := Normalize(value)
		if tag == "" || seen[tag] {
			continue
		}
		seen[tag] = true
		out = append(out, tag)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Label returns canonical display text: Unicode NFC with surrounding and
// repeated internal whitespace collapsed. It deliberately does not change the
// case, which is meaningful for titles and names.
func Label(raw string) string {
	return norm.NFC.String(strings.Join(strings.Fields(raw), " "))
}

// Username returns the canonical account name: trimmed, NFC and lowercase.
// Usernames are stored this way so lookups are case-insensitive without a
// collation.
func Username(raw string) string {
	return norm.NFC.String(strings.ToLower(strings.TrimSpace(raw)))
}
