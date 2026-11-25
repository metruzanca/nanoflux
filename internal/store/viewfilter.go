package store

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/metruzanca/nanoflux/internal/db"
)

// Filter parameters are stored on saved_views in their canonical ?f= spelling.
// The HTTP layer parses them for a page render; the store parses the same
// spelling for background work (claim maintenance and auto-read) so both paths
// apply exactly one interpretation of a view.
const (
	viewFilterTimeBefore  = "before"
	viewFilterTimeAfter   = "after"
	viewFilterTimeBetween = "between"
)

// FiltersForAuthor returns the usable view predicates assigned to an author.
// Unfiltered views are omitted, so a legacy blank recipe cannot pull the whole
// library into an author by accident.
func (s *SavedViewStore) FiltersForAuthor(userID, authorID int64, tz string) ([]ViewFilter, error) {
	views, err := s.ListByAuthor(userID, authorID)
	if err != nil {
		return nil, err
	}
	out := make([]ViewFilter, 0, len(views))
	for _, v := range views {
		vf := savedViewFilter(v, tz)
		if !viewFilterEmpty(vf) {
			out = append(out, vf)
		}
	}
	return out, nil
}

func viewFilterEmpty(v ViewFilter) bool {
	return len(v.TagGroups) == 0 && len(v.FeedGroups) == 0 &&
		len(v.AuthorGroups) == 0 && len(v.TimeGroups) == 0
}

// savedViewFilter compiles one saved view into the store predicate shape. It
// mirrors the HTTP layer's savedViewFilters so a view means the same thing in a
// page read and in a background claim/auto-read sweep.
func savedViewFilter(v SavedView, tz string) ViewFilter {
	var rules []FilterRule
	var timeGroups []TimeGroup
	for _, raw := range v.Filters {
		kind, action, combine, values, ok := parseViewFilterParam(raw)
		if !ok {
			continue
		}
		if kind == "time" {
			if g, ok := viewTimeGroup(action, values, tz); ok {
				timeGroups = append(timeGroups, g)
			}
			continue
		}
		rules = append(rules, FilterRule{Kind: kind, Action: action, Combine: combine, Values: values})
	}
	rules = NormalizeFilterRules(rules)
	tagGroups, feedGroups, authorGroups, _ := FilterGroups(rules)
	return ViewFilter{
		AuthorID:     v.AuthorID,
		TagGroups:    tagGroups,
		FeedGroups:   feedGroups,
		AuthorGroups: authorGroups,
		TimeGroups:   timeGroups,
	}
}

// parseViewFilterParam parses one canonical ?f= parameter.
func parseViewFilterParam(raw string) (kind, action, combine string, values []string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", "", nil, false
	}
	parts := strings.SplitN(raw, ":", 3)
	if len(parts) != 3 {
		return "", "", "", nil, false
	}
	kind = strings.ToLower(strings.TrimSpace(parts[0]))
	action = strings.ToLower(strings.TrimSpace(parts[1]))
	if action != FilterActionShow && action != FilterActionHide {
		action = FilterActionShow
	}
	if kind == "time" || kind == FilterKindAuthor {
		var vals []string
		if strings.HasPrefix(strings.TrimSpace(parts[2]), "[") {
			if err := json.Unmarshal([]byte(parts[2]), &vals); err != nil {
				return "", "", "", nil, false
			}
		} else {
			vals = []string{parts[2]}
		}
		combine = FilterCombineAny
		if kind == "time" {
			combine = FilterCombineAll
		}
		return kind, action, combine, vals, true
	}
	// tag/feed rows: kind:action:combine:["a","b"] or kind:action:value.
	if i := strings.IndexByte(parts[2], ':'); i >= 0 {
		head := parts[2][:i]
		tail := parts[2][i+1:]
		if (head == FilterCombineAny || head == FilterCombineAll) && strings.HasPrefix(strings.TrimSpace(tail), "[") {
			var vals []string
			if err := json.Unmarshal([]byte(tail), &vals); err == nil {
				return kind, action, head, vals, true
			}
		}
	}
	return kind, action, FilterCombineAll, []string{parts[2]}, true
}

// viewTimeGroup parses a time row's [mode, start, end] values in tz and converts
// them to the canonical UTC bounds the item queries use.
func viewTimeGroup(action string, values []string, tz string) (TimeGroup, bool) {
	mode := strings.ToLower(strings.TrimSpace(at(values, 0)))
	primary := strings.TrimSpace(at(values, 1))
	secondary := strings.TrimSpace(at(values, 2))
	switch mode {
	case viewFilterTimeBefore, viewFilterTimeAfter:
		secondary = ""
	case viewFilterTimeBetween:
	default:
		mode = viewFilterTimeBetween
	}
	loc := viewLocation(tz)
	var g TimeGroup
	switch mode {
	case viewFilterTimeBefore:
		if t, ok := parseViewBound(primary, loc, true); ok {
			g.Before = db.FormatTime(t)
		}
	case viewFilterTimeAfter:
		if t, ok := parseViewBound(primary, loc, false); ok {
			g.After = db.FormatTime(t)
		}
	default:
		if t, ok := parseViewBound(primary, loc, false); ok {
			g.After = db.FormatTime(t)
		}
		if t, ok := parseViewBound(secondary, loc, true); ok {
			g.Before = db.FormatTime(t)
		}
	}
	if g.After == "" && g.Before == "" {
		return TimeGroup{}, false
	}
	g.Exclude = action == FilterActionHide
	return g, true
}

func viewLocation(tz string) *time.Location {
	if tz == "" {
		return time.Local
	}
	if loc, err := time.LoadLocation(tz); err == nil {
		return loc
	}
	return time.Local
}

func parseViewBound(v string, loc *time.Location, endOfDay bool) (time.Time, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04"} {
		if t, err := time.ParseInLocation(layout, v, loc); err == nil {
			return t, true
		}
	}
	if t, err := time.ParseInLocation("2006-01-02", v, loc); err == nil {
		if endOfDay {
			y, m, d := t.Date()
			t = time.Date(y, m, d+1, 0, 0, 0, 0, loc).Add(-time.Second)
		}
		return t, true
	}
	return time.Time{}, false
}

func at(values []string, i int) string {
	if i < 0 || i >= len(values) {
		return ""
	}
	return values[i]
}
