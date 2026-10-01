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

// Filter modes. Stored in feeds.filter_mode as plain text.
const (
	// ModeBlock is the default: a matching rule applies its own action to the
	// matching item, and an item matching no rule is kept.
	ModeBlock = "block"
	// ModeAllow turns the feed's rule set into an allow list: an item is kept
	// only when it matches at least one rule (the per-rule action is ignored),
	// and an item matching no rule is dropped. With no rules at all nothing is
	// matched, so an allow feed with no rules keeps everything rather than
	// wiping itself.
	ModeAllow = "allow"
)

// NormalizeMode maps an unknown/empty mode to ModeBlock, so a missing or bad
// value behaves like the historical default.
func NormalizeMode(mode string) string {
	if mode == ModeAllow {
		return ModeAllow
	}
	return ModeBlock
}

// Decision is the outcome of evaluating a feed's rules against one item.
type Decision int

const (
	// Keep stores the item normally.
	Keep Decision = iota
	// Drop discards the item (never stored at ingest; removed retroactively).
	Drop
	// MarkRead stores the item as already read.
	MarkRead
)

// Decide evaluates a feed's whole rule set against an item under the feed's
// mode. It is the single decision shared by ingest and the HTTP preview/apply
// paths. In block mode the first matching rule's action wins; in allow mode an
// item is kept iff at least one rule matches, with no rule at all meaning keep.
func Decide(mode string, rules []store.Filter, f Fields) (Decision, error) {
	if len(rules) == 0 {
		return Keep, nil
	}
	if NormalizeMode(mode) == ModeAllow {
		for _, rule := range rules {
			m, err := Match(rule, f)
			if err != nil {
				return Keep, err
			}
			if m {
				// The rule's action is intentionally ignored in allow mode:
				// matching is what grants the item entry.
				return Keep, nil
			}
		}
		return Drop, nil
	}
	for _, rule := range rules {
		m, err := Match(rule, f)
		if err != nil {
			return Keep, err
		}
		if m {
			if rule.Action == ActionMarkRead {
				return MarkRead, nil
			}
			return Drop, nil
		}
	}
	return Keep, nil
}

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
