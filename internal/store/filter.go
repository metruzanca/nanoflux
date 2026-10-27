package store

import "strconv"

// Filter rule constants shared by the transient list filters and the persisted
// author-level filters. Actions are show/hide internally; the author settings UI
// labels them allow/block.
const (
	FilterKindTag  = "tag"
	FilterKindFeed = "feed"

	FilterActionShow = "show"
	FilterActionHide = "hide"

	FilterCombineAny = "any"
	FilterCombineAll = "all"
)

// FilterRule is one filter row, the canonical shape reused by transient list
// filters and persisted author rules. Values are tag texts or feed ids as
// strings.
type FilterRule struct {
	Kind    string
	Action  string
	Combine string
	Values  []string
}

func (r FilterRule) IsTag() bool     { return r.Kind == FilterKindTag }
func (r FilterRule) IsFeed() bool    { return r.Kind == FilterKindFeed }
func (r FilterRule) Shown() bool     { return r.Action == FilterActionShow }
func (r FilterRule) Hidden() bool    { return r.Action == FilterActionHide }
func (r FilterRule) AllValues() bool { return r.Combine == FilterCombineAll }

// Valid reports whether the rule has a known kind, action, combine mode and at
// least one value.
func (r FilterRule) Valid() bool {
	if len(r.Values) == 0 {
		return false
	}
	switch r.Kind {
	case FilterKindTag, FilterKindFeed:
	default:
		return false
	}
	switch r.Action {
	case FilterActionShow, FilterActionHide:
	default:
		return false
	}
	switch r.Combine {
	case FilterCombineAny, FilterCombineAll:
	default:
		return false
	}
	return true
}

// NormalizeFilterRules drops invalid rows, cleans values and removes duplicate
// values, preserving order. It mirrors the HTTP layer's normalizeFilters so both
// systems accept the same input shape.
func NormalizeFilterRules(in []FilterRule) []FilterRule {
	out := make([]FilterRule, 0, len(in))
	for _, r := range in {
		values := make([]string, 0, len(r.Values))
		seen := make(map[string]bool, len(r.Values))
		for _, v := range r.Values {
			if v == "" || seen[v] {
				continue
			}
			seen[v] = true
			values = append(values, v)
		}
		r.Values = values
		if r.Valid() {
			out = append(out, r)
		}
	}
	return out
}

// FilterGroups converts rules into the store's per-row predicates and returns
// the selected show-feed ids (used by scoped tab counts). Both the transient
// list filters and author rules go through this one conversion, so a semantics
// change benefits both.
func FilterGroups(rules []FilterRule) (tagGroups []TagGroup, feedGroups []FeedGroup, showFeedIDs []int64) {
	for _, r := range rules {
		if r.IsTag() {
			tagGroups = append(tagGroups, TagGroup{
				Tags: r.Values, All: r.AllValues(), Exclude: r.Hidden(),
			})
			continue
		}
		ids := make([]int64, 0, len(r.Values))
		for _, value := range r.Values {
			id, _ := strconv.ParseInt(value, 10, 64)
			if id > 0 {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			continue
		}
		feedGroups = append(feedGroups, FeedGroup{
			FeedIDs: ids, All: r.AllValues(), Exclude: r.Hidden(),
		})
		if r.Shown() {
			showFeedIDs = append(showFeedIDs, ids...)
		}
	}
	return tagGroups, feedGroups, showFeedIDs
}
