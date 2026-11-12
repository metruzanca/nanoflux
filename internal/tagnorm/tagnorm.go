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

// mediaTypeAliases maps a bare media word that feeds commonly publish as a
// category (their post type) onto the reserved type:* identifier. The core
// derives the same labels from an item's media, so folding the feed's own word
// avoids storing both "image" and "type:image" for one post.
var mediaTypeAliases = map[string]string{
	"image": "type:image",
	"video": "type:video",
	"audio": "type:audio",
	"text":  "type:text",
}

// FoldMediaTypeAliases rewrites each value that normalizes to a bare media
// word onto its reserved type:* identifier. Other values are returned
// unchanged; NormalizeAll still canonicalizes and dedupes the result.
func FoldMediaTypeAliases(values []string) []string {
	if len(values) == 0 {
		return values
	}
	out := make([]string, len(values))
	for i, value := range values {
		if alias, ok := mediaTypeAliases[Normalize(value)]; ok {
			out[i] = alias
			continue
		}
		out[i] = value
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
