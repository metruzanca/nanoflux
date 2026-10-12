// Package feedfilter evaluates a feed's per-feed content filters against an
// item. Filters are view-level: nothing is deleted or altered, the verdict only
// decides whether an item is shown. The single decision is used by the poller
// (which records the verdict on item_feeds.filtered for new items) and by the
// store's recompute pass (which re-evaluates a feed's items when its rules
// change), plus the HTTP preview.
//
// The package is deliberately dependency-free (stdlib only) so both the store
// and the poller can use it without an import cycle.
package feedfilter

import (
	"regexp"
	"strings"
)

// Modes, stored in feeds.filter_mode.
const (
	ModeOff   = "off"
	ModeBlock = "block"
	ModeAllow = "allow"
)

// Combinators, stored in feeds.filter_combine.
const (
	CombineAny = "any"
	CombineAll = "all"
)

// Rule fields.
const (
	FieldCategory = "category"
	FieldTitle    = "title"
	FieldLink     = "link"
	FieldSummary  = "summary"
)

// Rule operators.
const (
	OpIs         = "is"
	OpContains   = "contains"
	OpStartsWith = "starts_with"
	OpRegex      = "regex"
)

// Rule is one content-filter predicate.
type Rule struct {
	Field string
	Op    string
	Value string
}

// Fields is the subset of an item a rule matches against.
type Fields struct {
	Title      string
	Link       string
	Summary    string
	Categories []string
}

// NormalizeMode maps any stored value onto a known mode, defaulting to off.
func NormalizeMode(mode string) string {
	switch mode {
	case ModeAllow:
		return ModeAllow
	case ModeBlock:
		return ModeBlock
	default:
		return ModeOff
	}
}

// NormalizeCombine maps any stored value onto a known combinator, defaulting to
// any (OR).
func NormalizeCombine(combine string) string {
	if combine == CombineAll {
		return CombineAll
	}
	return CombineAny
}

// ValidField reports whether field is one this package matches.
func ValidField(field string) bool {
	switch field {
	case FieldCategory, FieldTitle, FieldLink, FieldSummary:
		return true
	}
	return false
}

// ValidOp reports whether op is a known operator.
func ValidOp(op string) bool {
	switch op {
	case OpIs, OpContains, OpStartsWith, OpRegex:
		return true
	}
	return false
}

// Allowed reports whether an item is visible under a feed's filter. An off mode
// or an empty rule set keeps everything.
func Allowed(mode, combine string, rules []Rule, f Fields) bool {
	mode = NormalizeMode(mode)
	if mode == ModeOff || len(rules) == 0 {
		return true
	}
	matched := matchAll(combine, rules, f)
	if mode == ModeAllow {
		// Allow list: an item is shown only when it satisfies the rules.
		return matched
	}
	// Block list: an item is hidden when it satisfies the rules.
	return !matched
}

// matchAll combines the rules: all of them must match (CombineAll) or any one
// of them (CombineAny).
func matchAll(combine string, rules []Rule, f Fields) bool {
	if NormalizeCombine(combine) == CombineAll {
		for _, r := range rules {
			if !Match(r, f) {
				return false
			}
		}
		return true
	}
	for _, r := range rules {
		if Match(r, f) {
			return true
		}
	}
	return false
}

// Match reports whether one rule matches an item. A category rule matches when
// any one of the item's categories matches, so a rule need not know how the
// item's categories are joined.
func Match(rule Rule, f Fields) bool {
	if rule.Field == FieldCategory {
		for _, c := range f.Categories {
			if matchValue(rule, c) {
				return true
			}
		}
		return false
	}
	switch rule.Field {
	case FieldTitle:
		return matchValue(rule, f.Title)
	case FieldLink:
		return matchValue(rule, f.Link)
	case FieldSummary:
		return matchValue(rule, f.Summary)
	default:
		return false
	}
}

// matchValue applies one rule's operator to one string. The text operators are
// case-insensitive; regex is compiled as written (a caller wanting
// case-insensitive can use (?i)). An invalid regex matches nothing.
func matchValue(rule Rule, text string) bool {
	switch rule.Op {
	case OpIs:
		return strings.EqualFold(strings.TrimSpace(text), strings.TrimSpace(rule.Value))
	case OpContains:
		return strings.Contains(strings.ToLower(text), strings.ToLower(rule.Value))
	case OpStartsWith:
		return strings.HasPrefix(strings.ToLower(text), strings.ToLower(rule.Value))
	case OpRegex:
		re, err := regexp.Compile(rule.Value)
		if err != nil {
			return false
		}
		return re.MatchString(text)
	default:
		return false
	}
}
