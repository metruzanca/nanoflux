package httpapi

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/store"
)

// Author-level list filters (the funnel-plus modal on author, feed and
// collection pages, and the global /unread list) are transient query state. A
// filter row is a tag, feed or time-window predicate whose values combine with
// any (OR) or all (AND), and which either shows or hides matching items:
//
//	f=tag:show:r/golang
//	f=tag:show:any:["r/golang","r/rust"]
//	f=feed:hide:12
//	f=time:show:["between","2026-01-01","2026-01-31"]
//
// Legacy single-value ?tags= and ?feed= params still parse as show/all
// filters. The modal form submits one filter_row token per row plus
// filter_value_<token> values (and filter_time_*_<token> for a time row), so a
// multi-select cannot desynchronize the parallel row fields.
const (
	filterKindTag  = "tag"
	filterKindFeed = "feed"
	filterKindTime = "time"

	filterShow = "show"
	filterHide = "hide"

	filterAny = "any"
	filterAll = "all"

	filterTimeBefore  = "before"
	filterTimeAfter   = "after"
	filterTimeBetween = "between"
)

// listFilter is one filter row. Time rows keep Values as [mode, primary,
// secondary]: primary is the single bound for before/after, secondary is the
// end of a between window and empty otherwise.
type listFilter struct {
	Kind    string   // filterKindTag, filterKindFeed or filterKindTime
	Action  string   // filterShow or filterHide
	Combine string   // filterAny or filterAll
	Values  []string // tag texts, feed ids as strings, or [mode, start, end]
	Labels  []string // display labels parallel to Values (feed titles)
}

// listFilters is an ordered set of filter rows.
type listFilters []listFilter

func (f listFilter) isTag() bool  { return f.Kind == filterKindTag }
func (f listFilter) isFeed() bool { return f.Kind == filterKindFeed }
func (f listFilter) isTime() bool { return f.Kind == filterKindTime }

func (f listFilter) shown() bool  { return f.Action == filterShow }
func (f listFilter) hidden() bool { return f.Action == filterHide }

func (f listFilter) allValues() bool { return f.Combine == filterAll }

// param is the canonical ?f= value. A single value keeps the legacy shape; a
// multi-value row embeds its values as a JSON array after the combine. A time
// row embeds [mode, start, end] as JSON without a combine mode.
func (f listFilter) param() string {
	if f.isTime() {
		b, _ := json.Marshal(f.Values)
		return filterKindTime + ":" + f.Action + ":" + string(b)
	}
	if len(f.Values) == 1 {
		return f.Kind + ":" + f.Action + ":" + f.Values[0]
	}
	b, _ := json.Marshal(f.Values)
	return f.Kind + ":" + f.Action + ":" + f.Combine + ":" + string(b)
}

// label renders a row for a chip: values joined by "or"/"and", or a readable
// window for a time row.
func (f listFilter) label() string {
	if f.isTime() {
		return f.timeLabel()
	}
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

// timeLabel renders a time row's window for its chip.
func (f listFilter) timeLabel() string {
	mode, primary, secondary := timeValues(f.Values)
	switch mode {
	case filterTimeBefore:
		return "before " + timeBoundLabel(primary)
	case filterTimeAfter:
		return "after " + timeBoundLabel(primary)
	default:
		if secondary == "" {
			return "after " + timeBoundLabel(primary)
		}
		return "between " + timeBoundLabel(primary) + " and " + timeBoundLabel(secondary)
	}
}

// timeValues unpacks a time row's [mode, primary, secondary] values.
func timeValues(values []string) (mode, primary, secondary string) {
	return at(values, 0), at(values, 1), at(values, 2)
}

// timeModeOf returns a time row's mode, defaulting to between for a fresh row.
func timeModeOf(values []string) string {
	mode, _, _ := timeValues(values)
	switch mode {
	case filterTimeBefore, filterTimeAfter, filterTimeBetween:
		return mode
	}
	return filterTimeBetween
}

// timePrimary is the row's single/start bound.
func timePrimary(values []string) string {
	_, primary, _ := timeValues(values)
	return primary
}

// timeSecondary is the row's end bound (between only).
func timeSecondary(values []string) string {
	_, _, secondary := timeValues(values)
	return secondary
}

// splitTimeInput splits a stored bound into its date and optional time parts.
func splitTimeInput(v string) (date, clock string) {
	if i := strings.IndexByte(v, 'T'); i >= 0 {
		return v[:i], v[i+1:]
	}
	return v, ""
}

// timeBoundLabel renders a date or datetime bound as the user entered it, in
// their own local terms (no timezone conversion).
func timeBoundLabel(v string) string {
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t.Format("Jan 2, 2006 3:04pm")
		}
	}
	if t, err := time.Parse("2006-01-02", v); err == nil {
		return t.Format("Jan 2, 2006")
	}
	return v
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
// and that row's selected values are repeated as filter_value_<token> (time
// rows instead carry their window in filter_time_*_<token> fields).
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
		if strings.ToLower(strings.TrimSpace(kind)) == filterKindTime {
			mode := vals.Get("filter_time_mode_" + token)
			primary := joinDateClock(vals.Get("filter_time_start_"+token), vals.Get("filter_time_start_clock_"+token))
			secondary := joinDateClock(vals.Get("filter_time_end_"+token), vals.Get("filter_time_end_clock_"+token))
			if f, ok := newTimeFilter(action, []string{mode, primary, secondary}); ok {
				out = append(out, f)
			}
			continue
		}
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

// parseFilterParam parses one canonical ?f= value. A time row carries a JSON
// array of [mode, start, end]; the multi-value form carries a JSON array,
// which keeps values containing ':' or ',' intact; anything else is the legacy
// kind:action:value shape.
func parseFilterParam(raw string) (listFilter, bool) {
	if strings.HasPrefix(raw, filterKindTime+":") {
		parts := strings.SplitN(raw, ":", 3)
		if len(parts) != 3 {
			return listFilter{}, false
		}
		var values []string
		if err := json.Unmarshal([]byte(parts[2]), &values); err != nil {
			return listFilter{}, false
		}
		return newTimeFilter(parts[1], values)
	}
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
	if kind == filterKindTime {
		return newTimeFilter(action, values)
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

// newTimeFilter validates a time row. values is [mode, start, end]; modes are
// between/before/after and a bound is a date or a datetime-local string. It
// reports false when the row has no usable bound.
func newTimeFilter(action string, values []string) (listFilter, bool) {
	action = strings.ToLower(strings.TrimSpace(action))
	if action != filterShow && action != filterHide {
		action = filterShow
	}
	mode := strings.ToLower(strings.TrimSpace(at(values, 0)))
	primary := strings.TrimSpace(at(values, 1))
	secondary := strings.TrimSpace(at(values, 2))
	switch mode {
	case filterTimeBefore, filterTimeAfter:
		secondary = ""
	case filterTimeBetween:
	default:
		mode = filterTimeBetween
	}
	if !validTimeBound(primary) || !validTimeBound(secondary) {
		return listFilter{}, false
	}
	if mode == filterTimeBetween {
		if primary == "" && secondary == "" {
			return listFilter{}, false
		}
		if primary == "" {
			// A lone end bound is just "before".
			mode, primary, secondary = filterTimeBefore, secondary, ""
		}
	} else if primary == "" {
		return listFilter{}, false
	}
	return listFilter{Kind: filterKindTime, Action: action, Combine: filterAll, Values: []string{mode, primary, secondary}}, true
}

// validTimeBound reports whether v is empty, a date or a datetime-local value.
func validTimeBound(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return true
	}
	_, ok := parseFilterBound(v, time.UTC, false)
	return ok
}

// joinDateClock combines a date input and an optional time input.
func joinDateClock(date, clock string) string {
	date = strings.TrimSpace(date)
	clock = strings.TrimSpace(clock)
	if date == "" {
		return ""
	}
	if clock == "" {
		return date
	}
	return date + "T" + clock
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
		if f.isTime() {
			if len(f.Values) == 0 {
				continue
			}
			out = append(out, f)
			continue
		}
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

// params returns the canonical ?f= spelling of each row, for persistence in a
// saved view. Time rows survive too (unlike the store rule conversion).
func (fs listFilters) params() []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.param())
	}
	return out
}

// filtersFromParams parses a saved view's stored canonical params. Invalid
// params are skipped.
func filtersFromParams(params []string) listFilters {
	out := make(listFilters, 0, len(params))
	for _, raw := range params {
		if f, ok := parseFilterParam(raw); ok {
			out = append(out, f)
		}
	}
	return normalizeFilters(out)
}

// rules converts the HTTP rows to the shared store rule shape, which is also
// what persisted author filters use. Time rows are transient only, so they are
// never handed to the store's rule conversion.
func (fs listFilters) rules() []store.FilterRule {
	out := make([]store.FilterRule, 0, len(fs))
	for _, f := range fs {
		if f.isTime() {
			continue
		}
		out = append(out, store.FilterRule{
			Kind: f.Kind, Action: f.Action, Combine: f.Combine, Values: f.Values,
		})
	}
	return out
}

// storeGroups converts the rows to the store's per-row tag/feed predicates and
// returns the selected show-feed ids (for tab counts/tag options). Time rows
// are applied separately through timeGroups.
func (fs listFilters) storeGroups() (tagGroups []store.TagGroup, feedGroups []store.FeedGroup, showFeedIDs []int64) {
	return store.FilterGroups(fs.rules())
}

// timeGroups converts the time rows to store time-window predicates, resolving
// each local bound in tz and converting it to the canonical UTC format.
func (fs listFilters) timeGroups(tz string) []store.TimeGroup {
	loc := filterLocation(tz)
	var out []store.TimeGroup
	for _, f := range fs {
		if !f.isTime() {
			continue
		}
		mode, primary, secondary := timeValues(f.Values)
		var g store.TimeGroup
		switch mode {
		case filterTimeBefore:
			if t, ok := parseFilterBound(primary, loc, true); ok {
				g.Before = db.FormatTime(t)
			}
		case filterTimeAfter:
			if t, ok := parseFilterBound(primary, loc, false); ok {
				g.After = db.FormatTime(t)
			}
		default:
			if t, ok := parseFilterBound(primary, loc, false); ok {
				g.After = db.FormatTime(t)
			}
			if t, ok := parseFilterBound(secondary, loc, true); ok {
				g.Before = db.FormatTime(t)
			}
		}
		if g.After == "" && g.Before == "" {
			continue
		}
		g.Exclude = f.hidden()
		out = append(out, g)
	}
	return out
}

// filterLocation resolves a user's IANA timezone, defaulting to the server's
// local timezone when unset or invalid (the same fallback web uses).
func filterLocation(tz string) *time.Location {
	if tz == "" {
		return time.Local
	}
	if loc, err := time.LoadLocation(tz); err == nil {
		return loc
	}
	return time.Local
}

// parseFilterBound parses a date or datetime-local bound in loc. A date-only
// bound becomes the start of that day, or its final second when endOfDay is
// set. The returned time is in loc; db.FormatTime converts it to UTC.
func parseFilterBound(v string, loc *time.Location, endOfDay bool) (time.Time, bool) {
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

// withoutFeedFilters drops feed filters, for scopes that cannot filter by feed
// (a collection spans feeds). Tag and time rows are kept.
func (fs listFilters) withoutFeedFilters() listFilters {
	out := make(listFilters, 0, len(fs))
	for _, f := range fs {
		if f.isFeed() {
			continue
		}
		out = append(out, f)
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
	return filterPageURLFor(path, view, sort, filters, 0)
}

// filterPageURLFor is filterPageURL for a view-scoped author page.
func filterPageURLFor(path, view string, sort itemSort, filters listFilters, viewID int64) string {
	u := scopedViewQueryFor(path, view, viewID) + "&sort=" + sort.param()
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

// mergeActiveTags adds active tag values that are missing from the option list
// (a filter can outlive the unread items that produced it) so the modal can
// still render and remove them.
func mergeActiveTags(opts []comboItem, fs listFilters) []comboItem {
	seen := make(map[string]bool, len(opts))
	for _, o := range opts {
		seen[o.Value] = true
	}
	for _, f := range fs {
		if !f.isTag() {
			continue
		}
		for _, value := range f.Values {
			if seen[value] {
				continue
			}
			seen[value] = true
			opts = append(opts, comboItem{Value: value, Label: value})
		}
	}
	return opts
}

func filterPlaceholder(kind string) string {
	if kind == filterKindFeed {
		return "feed"
	}
	return "tag"
}

// filterPanelData is the shared state of a list's filter toolbar: the modal,
// its option lists and the active chips. scopedItemsData and homeData both
// expose one so the same templates render on scoped pages and /unread.
type filterPanelData struct {
	Path          string
	View          string
	Sort          itemSort
	Filters       listFilters
	FilteredCount int
	TagOptions    []comboItem
	FeedOptions   []comboItem
	AllowTime     bool
	// ScopeAuthorID/ScopeCollectionID are the list's scope, carried into the
	// save-view link so a view remembers where it was created.
	ScopeAuthorID     int64
	ScopeCollectionID int64
	// ViewID is the selected saved view on a view-scoped author page (0 when no
	// view is selected). It makes links carry ?view=<id>&tab=<tab>.
	ViewID int64
}

// effectiveFeedOptions drops the feed kind when the scope has only one feed
// (filtering by that feed is the same as no filter).
func (d filterPanelData) effectiveFeedOptions() []comboItem {
	if len(d.FeedOptions) <= 1 {
		return nil
	}
	return d.FeedOptions
}

// kinds returns the filter kinds worth offering, in select order: tag when
// tags exist (or an active tag row needs editing), feed when more than one
// feed is in scope, and time where the transient modal allows it.
func (d filterPanelData) kinds() []string {
	var kinds []string
	tags := len(d.TagOptions) > 0
	if !tags {
		for _, f := range d.Filters {
			if f.isTag() {
				tags = true
				break
			}
		}
	}
	if tags {
		kinds = append(kinds, filterKindTag)
	}
	if len(d.effectiveFeedOptions()) > 0 {
		kinds = append(kinds, filterKindFeed)
	}
	if d.AllowTime {
		kinds = append(kinds, filterKindTime)
	}
	return kinds
}

// defaultKind is the kind a fresh row starts with: tag when available,
// otherwise feed, otherwise time.
func (d filterPanelData) defaultKind() string {
	kinds := d.kinds()
	if len(kinds) == 0 {
		return filterKindTag
	}
	return kinds[0]
}

// switchable reports whether the row needs a visible kind select.
func (d filterPanelData) switchable() bool { return len(d.kinds()) > 1 }

// available reports whether the filter dialog has anything to offer.
func (d filterPanelData) available() bool {
	return len(d.kinds()) > 0 || len(d.Filters) > 0
}

// filterPanel projects a scoped list's data onto the shared filter toolbar.
func (d scopedItemsData) filterPanel() filterPanelData {
	return filterPanelData{
		Path: d.Path, View: d.View, Sort: d.Sort,
		Filters: d.Filters, FilteredCount: d.FilteredCount,
		TagOptions: d.TagOptions, FeedOptions: d.FeedOptions, AllowTime: true,
		ScopeAuthorID: d.ScopeAuthorID, ScopeCollectionID: d.ScopeCollectionID,
		ViewID: d.ViewID,
	}
}

// filterPanel projects the /unread list onto the shared filter toolbar.
func (d homeData) filterPanel() filterPanelData {
	return filterPanelData{
		Path: "/unread", View: "unread", Sort: d.Sort,
		Filters: d.Filters, FilteredCount: d.FilteredCount,
		TagOptions: d.TagOptions, FeedOptions: d.FeedOptions, AllowTime: true,
	}
}

// saveViewURL is the "save as view" link for the current list: the view, sort,
// scope and active filters become the create form's prefill.
func (d filterPanelData) saveViewURL() string {
	v := url.Values{}
	v.Set("view", d.View)
	v.Set("sort", d.Sort.param())
	if d.ScopeAuthorID > 0 {
		v.Set("scope_author", strconv.FormatInt(d.ScopeAuthorID, 10))
	}
	if d.ScopeCollectionID > 0 {
		v.Set("scope_collection", strconv.FormatInt(d.ScopeCollectionID, 10))
	}
	for _, p := range d.Filters.params() {
		v.Add("f", p)
	}
	return "/views/new?" + v.Encode()
}
