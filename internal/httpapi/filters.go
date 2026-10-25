package httpapi

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/metruzanca/nanoflux/internal/store"
)

// Author-level list filters (the funnel-plus modal on author, feed and
// collection pages) are transient query state. A filter row is a tag or feed
// predicate whose values combine with any (OR) or all (AND), and which either
// shows or hides matching items:
//
//	f=tag:show:r/golang
//	f=tag:show:any:["r/golang","r/rust"]
//	f=feed:hide:12
//
// Legacy single-value ?tags= and ?feed= params still parse as show/all
// filters. The modal form submits one filter_row token per row plus
// filter_value_<token> values, so a multi-select cannot desynchronize the
// parallel row fields.
const (
	filterKindTag  = "tag"
	filterKindFeed = "feed"

	filterShow = "show"
	filterHide = "hide"

	filterAny = "any"
	filterAll = "all"
)

// listFilter is one filter row.
type listFilter struct {
	Kind    string   // filterKindTag or filterKindFeed
	Action  string   // filterShow or filterHide
	Combine string   // filterAny or filterAll
	Values  []string // tag texts, or feed ids as strings
	Labels  []string // display labels parallel to Values (feed titles)
}

// listFilters is an ordered set of filter rows.
type listFilters []listFilter

func (f listFilter) isTag() bool  { return f.Kind == filterKindTag }
func (f listFilter) isFeed() bool { return f.Kind == filterKindFeed }

func (f listFilter) shown() bool  { return f.Action == filterShow }
func (f listFilter) hidden() bool { return f.Action == filterHide }

func (f listFilter) allValues() bool { return f.Combine == filterAll }

// param is the canonical ?f= value. A single value keeps the legacy shape; a
// multi-value row embeds its values as a JSON array after the combine.
func (f listFilter) param() string {
	if len(f.Values) == 1 {
		return f.Kind + ":" + f.Action + ":" + f.Values[0]
	}
	b, _ := json.Marshal(f.Values)
	return f.Kind + ":" + f.Action + ":" + f.Combine + ":" + string(b)
}

// label renders a row for a chip: values joined by "or"/"and".
func (f listFilter) label() string {
	values := f.Labels
	if len(values) == 0 {
		values = f.Values
	}
	joiner := " or "
	if f.allValues() {
		joiner = " and "
	}
	return strings.Join(values, joiner)
}

// filtersFromRequest reads the active filters from a list request.
func filtersFromRequest(r *http.Request) listFilters {
	return filtersFromValues(r.URL.Query())
}

// filtersFromValues parses filters from an already-parsed query. Form rows win
// when present so a modal submission replaces the applied set exactly (blank
// rows are ignored). Otherwise canonical ?f= values are read, then the legacy
// ?tags=/?feed= aliases.
func filtersFromValues(vals url.Values) listFilters {
	if vals.Has("filter_row") {
		return normalizeFilters(filtersFromRows(vals))
	}
	var out listFilters
	for _, raw := range vals["f"] {
		if f, ok := parseFilterParam(raw); ok {
			out = append(out, f)
		}
	}
	for _, tag := range vals["tags"] {
		out = append(out, listFilter{Kind: filterKindTag, Action: filterShow, Combine: filterAll, Values: []string{tag}})
	}
	if id := strings.TrimSpace(vals.Get("feed")); id != "" && id != "0" {
		out = append(out, listFilter{Kind: filterKindFeed, Action: filterShow, Combine: filterAll, Values: []string{id}})
	}
	return normalizeFilters(out)
}

// filtersFromRows parses the modal form: filter_row tokens identify each row,
// and that row's selected values are repeated as filter_value_<token>.
func filtersFromRows(vals url.Values) listFilters {
	tokens := vals["filter_row"]
	actions := vals["filter_action"]
	kinds := vals["filter_kind"]
	combines := vals["filter_combine"]
	out := make(listFilters, 0, len(tokens))
	for i, token := range tokens {
		kind := at(kinds, i)
		action := at(actions, i)
		combine := at(combines, i)
		values := vals["filter_value_"+token]
		if f, ok := newFilter(kind, action, combine, values); ok {
			out = append(out, f)
		}
	}
	return out
}

func at(values []string, i int) string {
	if i >= 0 && i < len(values) {
		return values[i]
	}
	return ""
}

// parseFilterParam parses one canonical ?f= value. The multi-value form carries
// a JSON array, which keeps values containing ':' or ',' intact; anything else
// is the legacy kind:action:value shape.
func parseFilterParam(raw string) (listFilter, bool) {
	parts := strings.SplitN(raw, ":", 4)
	if len(parts) == 4 && validCombine(parts[2]) && strings.HasPrefix(strings.TrimSpace(parts[3]), "[") {
		var values []string
		if err := json.Unmarshal([]byte(parts[3]), &values); err == nil {
			return newFilter(parts[0], parts[1], parts[2], values)
		}
	}
	parts = strings.SplitN(raw, ":", 3)
	if len(parts) != 3 {
		return listFilter{}, false
	}
	return newFilter(parts[0], parts[1], filterAll, []string{parts[2]})
}

func validCombine(combine string) bool {
	combine = strings.ToLower(strings.TrimSpace(combine))
	return combine == filterAny || combine == filterAll
}

// newFilter validates a row's fields and builds a filter.
func newFilter(kind, action, combine string, values []string) (listFilter, bool) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	action = strings.ToLower(strings.TrimSpace(action))
	combine = strings.ToLower(strings.TrimSpace(combine))
	if action != filterShow && action != filterHide {
		action = filterShow
	}
	if combine != filterAny && combine != filterAll {
		combine = filterAll
	}
	cleaned := cleanValues(values)
	if len(cleaned) == 0 {
		return listFilter{}, false
	}
	switch kind {
	case filterKindTag:
		return listFilter{Kind: kind, Action: action, Combine: combine, Values: cleaned, Labels: cleaned}, true
	case filterKindFeed:
		ids := make([]string, 0, len(cleaned))
		for _, value := range cleaned {
			id, err := strconv.ParseInt(value, 10, 64)
			if err != nil || id <= 0 {
				continue
			}
			ids = append(ids, strconv.FormatInt(id, 10))
		}
		if len(ids) == 0 {
			return listFilter{}, false
		}
		return listFilter{Kind: kind, Action: action, Combine: combine, Values: ids}, true
	default:
		return listFilter{}, false
	}
}

// cleanValues trims, drops empties and dedupes values preserving order.
func cleanValues(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

// normalizeFilters caps tag values across every row and drops empty rows.
func normalizeFilters(in listFilters) listFilters {
	out := make(listFilters, 0, len(in))
	tags := 0
	for _, f := range in {
		if f.isTag() {
			values := make([]string, 0, len(f.Values))
			for _, value := range f.Values {
				if tags >= maxTagFilters {
					break
				}
				values = append(values, value)
				tags++
			}
			if len(values) == 0 {
				continue
			}
			f.Values = values
			f.Labels = values
		}
		if len(f.Values) == 0 {
			continue
		}
		out = append(out, f)
	}
	return out
}

// storeGroups converts the rows to the store's per-row predicates and returns
// the selected show-feed ids (for tab counts/tag options).
func (fs listFilters) storeGroups() (tagGroups []store.TagGroup, feedGroups []store.FeedGroup, showFeedIDs []int64) {
	for _, f := range fs {
		if f.isTag() {
			tagGroups = append(tagGroups, store.TagGroup{
				Tags: f.Values, All: f.allValues(), Exclude: f.hidden(),
			})
			continue
		}
		ids := make([]int64, 0, len(f.Values))
		for _, value := range f.Values {
			id, _ := strconv.ParseInt(value, 10, 64)
			if id > 0 {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			continue
		}
		feedGroups = append(feedGroups, store.FeedGroup{
			FeedIDs: ids, All: f.allValues(), Exclude: f.hidden(),
		})
		if f.shown() {
			showFeedIDs = append(showFeedIDs, ids...)
		}
	}
	return tagGroups, feedGroups, showFeedIDs
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
// handler re-reads them from the query string. A simple single-value show
// filter keeps the legacy ?tags=/?feed= spelling so existing actions and links
// stay stable; everything else uses the canonical ?f= form.
func filterActionURL(base string, filters listFilters) string {
	if len(filters) == 0 {
		return base
	}
	v := url.Values{}
	for _, f := range filters {
		switch {
		case f.isTag() && f.shown() && len(f.Values) == 1 && f.allValues():
			v.Add("tags", f.Values[0])
		case f.isFeed() && f.shown() && len(f.Values) == 1 && f.allValues():
			v.Set("feed", f.Values[0])
		default:
			v.Add("f", f.param())
		}
	}
	return base + "?" + v.Encode()
}

// filterFeedTitles fills feed labels from the author's feeds.
func filterFeedTitles(fs listFilters, titles map[int64]string) listFilters {
	out := make(listFilters, len(fs))
	copy(out, fs)
	for i := range out {
		if !out[i].isFeed() {
			continue
		}
		labels := make([]string, len(out[i].Values))
		for j, value := range out[i].Values {
			id, _ := strconv.ParseInt(value, 10, 64)
			labels[j] = titles[id]
			if labels[j] == "" {
				labels[j] = value
			}
		}
		out[i].Labels = labels
	}
	return out
}

func filterPlaceholder(kind string) string {
	if kind == filterKindFeed {
		return "feed"
	}
	return "tag"
}

// effectiveFeedOptions drops the feed kind when an author has only one feed
// (filtering by that feed is the same as no filter).
func (d scopedItemsData) effectiveFeedOptions() []comboItem {
	if len(d.FeedOptions) <= 1 {
		return nil
	}
	return d.FeedOptions
}

// filterKindOptions returns the kinds worth offering: a kind is available when
// it has options, or when an existing filter already uses it.
func (d scopedItemsData) filterKindOptions() (tags, feeds bool) {
	tags = len(d.TagOptions) > 0
	feeds = len(d.effectiveFeedOptions()) > 0
	for _, f := range d.Filters {
		if f.isTag() {
			tags = true
		}
		if f.isFeed() {
			feeds = true
		}
	}
	return tags, feeds
}

// filterDefaultKind is the kind a fresh row starts with: tag when tags are
// available (including the single-feed case), otherwise feed.
func (d scopedItemsData) filterDefaultKind() string {
	tags, feeds := d.filterKindOptions()
	if tags {
		return filterKindTag
	}
	if feeds {
		return filterKindFeed
	}
	return filterKindTag
}

// filterKindsSwitchable reports whether the row needs a visible tag/feed select.
func (d scopedItemsData) filterKindsSwitchable() bool {
	tags, feeds := d.filterKindOptions()
	return tags && feeds
}

// filterAvailable reports whether the filter dialog has anything to offer.
func (d scopedItemsData) filterAvailable() bool {
	tags, feeds := d.filterKindOptions()
	return tags || feeds || len(d.Filters) > 0
}
