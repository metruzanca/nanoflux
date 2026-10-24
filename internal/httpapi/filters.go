package httpapi

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Author-level list filters (the funnel-plus modal on author, feed and
// collection pages) are transient query state. A filter is a tag or a feed,
// shown or hidden:
//
//	f=tag:show:r/golang
//	f=tag:hide:r/rust
//	f=feed:hide:12
//
// The legacy ?tags= and ?feed= params still parse as show filters, and the modal
// form submits parallel row fields (filter_kind/action/value) which are
// normalized to the same model.
const (
	filterKindTag  = "tag"
	filterKindFeed = "feed"

	filterShow = "show"
	filterHide = "hide"
)

// listFilter is one applied filter row.
type listFilter struct {
	Kind   string // filterKindTag or filterKindFeed
	Action string // filterShow or filterHide
	Value  string // tag text, or a feed id as a string
	Label  string // feed title for display; tags use their value
}

// listFilters is an ordered set of applied filters.
type listFilters []listFilter

// param is the canonical ?f= value.
func (f listFilter) param() string {
	return f.Kind + ":" + f.Action + ":" + f.Value
}

func (f listFilter) isTag() bool  { return f.Kind == filterKindTag }
func (f listFilter) isFeed() bool { return f.Kind == filterKindFeed }

func (f listFilter) shown() bool  { return f.Action == filterShow }
func (f listFilter) hidden() bool { return f.Action == filterHide }

// filtersFromRequest reads the active filters from a list request.
func filtersFromRequest(r *http.Request) listFilters {
	return filtersFromValues(r.URL.Query())
}

// filtersFromValues parses filters from an already-parsed query. Form rows win
// when present so a modal submission replaces the applied set exactly (blank
// rows are ignored). Otherwise canonical ?f= values are read, then the legacy
// ?tags=/?feed= aliases.
func filtersFromValues(vals url.Values) listFilters {
	if vals.Has("filter_kind") {
		return normalizeFilters(filtersFromRows(vals))
	}
	var out listFilters
	for _, raw := range vals["f"] {
		if f, ok := parseFilterParam(raw); ok {
			out = append(out, f)
		}
	}
	for _, tag := range vals["tags"] {
		out = append(out, listFilter{Kind: filterKindTag, Action: filterShow, Value: tag})
	}
	if id := strings.TrimSpace(vals.Get("feed")); id != "" && id != "0" {
		out = append(out, listFilter{Kind: filterKindFeed, Action: filterShow, Value: id})
	}
	return normalizeFilters(out)
}

// filtersFromRows parses the modal form's parallel row fields.
func filtersFromRows(vals url.Values) listFilters {
	kinds := vals["filter_kind"]
	actions := vals["filter_action"]
	values := vals["filter_value"]
	n := len(kinds)
	if len(actions) > n {
		n = len(actions)
	}
	if len(values) > n {
		n = len(values)
	}
	out := make(listFilters, 0, n)
	for i := 0; i < n; i++ {
		var kind, action, value string
		if i < len(kinds) {
			kind = kinds[i]
		}
		if i < len(actions) {
			action = actions[i]
		}
		if i < len(values) {
			value = values[i]
		}
		if f, ok := newFilter(kind, action, value); ok {
			out = append(out, f)
		}
	}
	return out
}

// parseFilterParam parses one canonical "kind:action:value" value. The value is
// the remainder so a tag containing ':' survives.
func parseFilterParam(raw string) (listFilter, bool) {
	parts := strings.SplitN(raw, ":", 3)
	if len(parts) != 3 {
		return listFilter{}, false
	}
	return newFilter(parts[0], parts[1], parts[2])
}

// newFilter validates a row's fields and builds a filter.
func newFilter(kind, action, value string) (listFilter, bool) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	action = strings.ToLower(strings.TrimSpace(action))
	value = strings.TrimSpace(value)
	if value == "" {
		return listFilter{}, false
	}
	if action != filterShow && action != filterHide {
		action = filterShow
	}
	switch kind {
	case filterKindTag:
		return listFilter{Kind: kind, Action: action, Value: value, Label: value}, true
	case filterKindFeed:
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 {
			return listFilter{}, false
		}
		return listFilter{Kind: kind, Action: action, Value: strconv.FormatInt(id, 10)}, true
	default:
		return listFilter{}, false
	}
}

// normalizeFilters trims, validates and dedupes filters. Show/hide tags dedupe
// independently (the same tag can be shown and hidden, where hide wins in the
// query). Only one show feed and one hide feed are kept.
func normalizeFilters(in listFilters) listFilters {
	out := make(listFilters, 0, len(in))
	seenTags := map[string]bool{}
	tags := 0
	var showFeed, hideFeed bool
	for _, f := range in {
		if f.isTag() {
			if tags >= maxTagFilters {
				continue
			}
			key := f.Action + "\x00" + f.Value
			if seenTags[key] {
				continue
			}
			seenTags[key] = true
			tags++
			if f.Label == "" {
				f.Label = f.Value
			}
			out = append(out, f)
			continue
		}
		if f.isFeed() {
			if f.shown() {
				if showFeed {
					continue
				}
				showFeed = true
			} else {
				if hideFeed {
					continue
				}
				hideFeed = true
			}
			out = append(out, f)
		}
	}
	return out
}

// storeFields splits the filters into the store's include/exclude fields.
func (fs listFilters) storeFields() (tags, excludeTags []string, showFeed, hideFeed int64) {
	for _, f := range fs {
		switch {
		case f.isTag() && f.shown():
			tags = append(tags, f.Value)
		case f.isTag() && f.hidden():
			excludeTags = append(excludeTags, f.Value)
		case f.isFeed() && f.shown():
			showFeed, _ = strconv.ParseInt(f.Value, 10, 64)
		case f.isFeed() && f.hidden():
			hideFeed, _ = strconv.ParseInt(f.Value, 10, 64)
		}
	}
	return tags, excludeTags, showFeed, hideFeed
}

// onlyTags drops feed filters, for scopes that cannot filter by feed.
func (fs listFilters) onlyTags() listFilters {
	out := make(listFilters, 0, len(fs))
	for _, f := range fs {
		if f.isTag() {
			out = append(out, f)
		}
	}
	return out
}

// without returns the filters with the entry at index i removed, for a chip's
// remove link.
func (fs listFilters) without(i int) listFilters {
	if i < 0 || i >= len(fs) {
		return fs
	}
	out := make(listFilters, 0, len(fs)-1)
	out = append(out, fs[:i]...)
	out = append(out, fs[i+1:]...)
	return out
}

// appendFilters adds the active filters to a list URL base as repeated ?f=
// params, preserving any existing query string.
func (fs listFilters) appendFilters(base string) string {
	if len(fs) == 0 {
		return base
	}
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	var b strings.Builder
	b.WriteString(base)
	for _, f := range fs {
		b.WriteString(sep)
		b.WriteString("f=")
		b.WriteString(url.QueryEscape(f.param()))
		sep = "&"
	}
	return b.String()
}

// filterPageURL builds a scoped page's URL with its view/sort and filters, used
// by the active-filter chips (clicking one drops it) and their links.
func filterPageURL(path, view string, sort itemSort, filters listFilters) string {
	u := scopedViewQuery(path, view) + "&sort=" + sort.param()
	return filters.appendFilters(u)
}

// filterActionURL encodes filters for a POST action (mark-all-read), whose
// handler re-reads them from the query string. Show tags/feed keep the legacy
// ?tags=/?feed= spelling so existing actions and links stay stable; hidden
// filters use the canonical ?f= form.
func filterActionURL(base string, filters listFilters) string {
	if len(filters) == 0 {
		return base
	}
	v := url.Values{}
	for _, f := range filters {
		switch {
		case f.isTag() && f.shown():
			v.Add("tags", f.Value)
		case f.isFeed() && f.shown():
			v.Set("feed", f.Value)
		default:
			v.Add("f", f.param())
		}
	}
	return base + "?" + v.Encode()
}

// filterFeedTitles fills a feed filter's display label from the author's feeds.
func filterFeedTitles(fs listFilters, titles map[int64]string) listFilters {
	out := make(listFilters, len(fs))
	copy(out, fs)
	for i := range out {
		if !out[i].isFeed() {
			continue
		}
		id, _ := strconv.ParseInt(out[i].Value, 10, 64)
		if title := titles[id]; title != "" {
			out[i].Label = title
		}
	}
	return out
}

// filterOptions returns the combo options for a row of the given kind.
func (d scopedItemsData) filterOptions(kind string) []comboItem {
	if kind == filterKindFeed {
		return d.FeedOptions
	}
	return d.TagOptions
}

// hasFilterKind reports whether an applied filter of the given kind exists.
func (d scopedItemsData) hasFilterKind(kind string) bool {
	for _, f := range d.Filters {
		if f.Kind == kind {
			return true
		}
	}
	return false
}

func filterPlaceholder(kind string) string {
	if kind == filterKindFeed {
		return "feed"
	}
	return "tag"
}
