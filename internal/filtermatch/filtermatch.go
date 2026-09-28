// Package filtermatch evaluates a feed's ingest filter rules against an item.
// It is the single matching implementation shared by the poller (which applies
// rules to newly fetched items) and the HTTP layer (which previews and applies
// them retroactively to already-stored items), so the two can never drift.
package filtermatch

import (
	"regexp"
	"strings"

	"github.com/metruzanca/nanoflux/internal/feedparse"
	"github.com/metruzanca/nanoflux/internal/store"
)

// Filter actions. Stored in filters.action as plain text; kept here so the
// poller, HTTP layer and templates agree on the spelling.
const (
	// ActionDelete drops a matching item: at ingest it is never stored, and when
	// a rule is added it is removed from the feed retroactively.
	ActionDelete = "delete"
	// ActionMarkRead stores a matching item as already read.
	ActionMarkRead = "mark_read"
)

// Fields is the subset of an item a rule matches against. Both feedparse.Item
// (freshly fetched) and store.ItemWithFeed (already stored) can be reduced to it.
type Fields struct {
	Title      string
	Link       string
	Summary    string
	Categories []string
}

// FieldsFromFeedItem reduces a freshly parsed item to its matchable fields.
func FieldsFromFeedItem(it feedparse.Item) Fields {
	return Fields{
		Title:      it.Title,
		Link:       it.Link,
		Summary:    it.Summary,
		Categories: it.Categories,
	}
}

// Match reports whether a rule's pattern matches an item's selected field.
// Summary matching uses the visible text, not the raw HTML. Category matching
// succeeds when any one of the item's feed-provided categories matches, so a
// rule need not know how categories are joined.
func Match(rule store.Filter, f Fields) (bool, error) {
	if rule.Field == "category" {
		for _, c := range f.Categories {
			if m, err := matchText(rule, c); err != nil {
				return false, err
			} else if m {
				return true, nil
			}
		}
		return false, nil
	}
	var text string
	switch rule.Field {
	case "title":
		text = f.Title
	case "link":
		text = f.Link
	case "summary":
		text = feedparse.PlainText(f.Summary)
	default:
		return false, nil
	}
	return matchText(rule, text)
}

// matchText applies a single rule's pattern to one string: a regex match when
// IsRegex is set, otherwise a case-insensitive substring test.
func matchText(rule store.Filter, text string) (bool, error) {
	if rule.IsRegex {
		re, err := regexp.Compile(rule.Pattern)
		if err != nil {
			return false, err
		}
		return re.MatchString(text), nil
	}
	return strings.Contains(strings.ToLower(text), strings.ToLower(rule.Pattern)), nil
}
