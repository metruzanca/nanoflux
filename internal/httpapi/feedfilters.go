package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/charmbracelet/log"

	"github.com/metruzanca/nanoflux/internal/auth"
	"github.com/metruzanca/nanoflux/internal/feedfilter"
	"github.com/metruzanca/nanoflux/internal/store"
	"github.com/metruzanca/nanoflux/internal/web"
)

// The content-filter rule vocabulary, shared by the editor's pickers.
var filterFieldItems = []comboItem{
	{Value: feedfilter.FieldCategory, Label: "category"},
	{Value: feedfilter.FieldTitle, Label: "title"},
	{Value: feedfilter.FieldLink, Label: "link"},
	{Value: feedfilter.FieldSummary, Label: "summary"},
}

var filterOpItems = []comboItem{
	{Value: feedfilter.OpIs, Label: "is"},
	{Value: feedfilter.OpContains, Label: "contains"},
	{Value: feedfilter.OpStartsWith, Label: "starts with"},
	{Value: feedfilter.OpRegex, Label: "regex"},
}

// filterOpLabel is the human label for a stored operator.
func filterOpLabel(op string) string {
	for _, it := range filterOpItems {
		if it.Value == op {
			return it.Label
		}
	}
	return op
}

// filterHelp explains the current filter mode.
func filterHelp(d feedsData) string {
	switch d.FilterMode {
	case feedfilter.ModeAllow:
		return "allow list: an item is kept only when it matches the rules below, combined with “" + filterCombineLabel(d.FilterCombine) + "”. everything else is hidden (never deleted)."
	case feedfilter.ModeBlock:
		return "block list: an item matching the rules below, combined with “" + filterCombineLabel(d.FilterCombine) + "”, is hidden (never deleted)."
	default:
		return "no filtering: every item is shown. pick block list to hide matching posts or allow list to keep only matching posts."
	}
}

func filterCombineLabel(combine string) string {
	if feedfilter.NormalizeCombine(combine) == feedfilter.CombineAll {
		return "all"
	}
	return "any"
}

// filterModeOptions builds the mode picker. Each option carries the current
// combine so only the mode changes.
func filterModeOptions(d feedsData) []pickerOption {
	id := strconv.FormatInt(d.Form.ID, 10)
	endpoint := "/feeds/" + id + "/filter-config"
	target := "#feed-filters"
	combine := feedfilter.NormalizeCombine(d.FilterCombine)
	opt := func(value, label string) pickerOption {
		return pickerOption{
			Value: value, Label: label, HxPost: endpoint, HxTarget: target,
			HxVals: `{"mode":"` + value + `","combine":"` + combine + `"}`,
		}
	}
	return []pickerOption{
		opt(feedfilter.ModeOff, "off"),
		opt(feedfilter.ModeBlock, "block list"),
		opt(feedfilter.ModeAllow, "allow list"),
	}
}

// filterCombineOptions builds the combine picker, carrying the current mode.
func filterCombineOptions(d feedsData) []pickerOption {
	id := strconv.FormatInt(d.Form.ID, 10)
	endpoint := "/feeds/" + id + "/filter-config"
	target := "#feed-filters"
	mode := feedfilter.NormalizeMode(d.FilterMode)
	opt := func(value, label string) pickerOption {
		return pickerOption{
			Value: value, Label: label, HxPost: endpoint, HxTarget: target,
			HxVals: `{"mode":"` + mode + `","combine":"` + value + `"}`,
		}
	}
	return []pickerOption{
		opt(feedfilter.CombineAny, "any"),
		opt(feedfilter.CombineAll, "all"),
	}
}

// feedFilterSectionData reloads just what the filters section renders.
func (s *Server) feedFilterSectionData(userID int64, f store.Feed) feedsData {
	rules, _ := s.store.FeedFilters.List(userID, f.ID)
	return feedsData{
		Form:          feedForm{ID: f.ID},
		FilterMode:    feedfilter.NormalizeMode(f.FilterMode),
		FilterCombine: feedfilter.NormalizeCombine(f.FilterCombine),
		FilterRules:   rules,
	}
}

// feedFilterAdd adds one rule to a feed, re-evaluates the feed's items and
// re-renders the filters section.
func (s *Server) feedFilterAdd(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	f, err := s.store.Feeds.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	field := strings.TrimSpace(r.FormValue("field"))
	op := strings.TrimSpace(r.FormValue("op"))
	value := strings.TrimSpace(r.FormValue("value"))
	if !feedfilter.ValidField(field) {
		writeFormError(w, r, "feed-filters-error", "pick a valid field")
		return
	}
	if !feedfilter.ValidOp(op) {
		writeFormError(w, r, "feed-filters-error", "pick a valid match")
		return
	}
	if value == "" {
		writeFormError(w, r, "feed-filters-error", "a value is required")
		return
	}
	if _, err := s.store.FeedFilters.Create(u.ID, id, field, op, value); err != nil {
		log.Error("create feed filter", "feed_id", id, "err", err)
		http.Error(w, "could not save the rule", http.StatusInternalServerError)
		return
	}
	s.recomputeFeedFilter(u.ID, id)
	web.Render(w, r, feedFiltersSection(s.feedFilterSectionData(u.ID, f)))
}

// feedFilterDelete removes one of a feed's rules, re-evaluates its items and
// re-renders the filters section.
func (s *Server) feedFilterDelete(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ruleID, err := strconv.ParseInt(r.PathValue("ruleID"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	f, err := s.store.Feeds.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// Only delete a rule that belongs to this feed (the id in the path could be
	// another feed's).
	rules, _ := s.store.FeedFilters.List(u.ID, id)
	owned := false
	for _, rule := range rules {
		if rule.ID == ruleID {
			owned = true
			break
		}
	}
	if !owned {
		http.NotFound(w, r)
		return
	}
	if err := s.store.FeedFilters.Delete(u.ID, ruleID); err != nil && !errors.Is(err, store.ErrNotFound) {
		log.Error("delete feed filter", "feed_id", id, "rule_id", ruleID, "err", err)
		http.Error(w, "could not remove the rule", http.StatusInternalServerError)
		return
	}
	s.recomputeFeedFilter(u.ID, id)
	web.Render(w, r, feedFiltersSection(s.feedFilterSectionData(u.ID, f)))
}

// feedFilterConfig sets a feed's filter mode/combine, re-evaluates its items
// and re-renders the filters section.
func (s *Server) feedFilterConfig(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	f, err := s.store.Feeds.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	mode := r.FormValue("mode")
	if mode == "" {
		mode = f.FilterMode
	}
	combine := r.FormValue("combine")
	if combine == "" {
		combine = f.FilterCombine
	}
	if err := s.store.Feeds.SetFilterConfig(u.ID, id, mode, combine); err != nil {
		log.Error("set feed filter config", "feed_id", id, "err", err)
		http.Error(w, "could not save the filter settings", http.StatusInternalServerError)
		return
	}
	s.recomputeFeedFilter(u.ID, id)
	f, _ = s.store.Feeds.ByID(u.ID, id)
	web.Render(w, r, feedFiltersSection(s.feedFilterSectionData(u.ID, f)))
}

// recomputeFeedFilter re-evaluates a feed's items after a rule/config change,
// logging (not failing the request) if it errors: the rules are saved, and the
// next poll or a manual edit will reconcile.
func (s *Server) recomputeFeedFilter(userID, feedID int64) {
	if err := s.store.Items.RecomputeFeedFilter(userID, feedID); err != nil {
		log.Error("recompute feed filter", "feed_id", feedID, "err", err)
	}
}

// feedFilterPreviewData backs the filter-preview page: a feed's items split by
// the filter verdict, so a user can see what is shown and what is hidden.
type feedFilterPreviewData struct {
	Feed    store.Feed
	Mode    string
	Combine string
	Rules   []store.FeedFilter
	Allowed []store.ItemWithFeed
	Blocked []store.ItemWithFeed
}

// feedFilterPreview renders every item of a feed, grouped by allowed/blocked.
func (s *Server) feedFilterPreview(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	f, err := s.store.Feeds.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	items, _ := s.store.Items.ListFeedItemsForFilter(u.ID, id)
	allowed := make([]store.ItemWithFeed, 0, len(items))
	blocked := make([]store.ItemWithFeed, 0)
	for _, it := range items {
		if it.Filtered {
			blocked = append(blocked, it.Item)
		} else {
			allowed = append(allowed, it.Item)
		}
	}
	rules, _ := s.store.FeedFilters.List(u.ID, id)
	web.Render(w, r, basePage("filters: "+f.Title, u, feedFilterPreviewPage(u, feedFilterPreviewData{
		Feed:    f,
		Mode:    feedfilter.NormalizeMode(f.FilterMode),
		Combine: feedfilter.NormalizeCombine(f.FilterCombine),
		Rules:   rules,
		Allowed: withTZ(u.Timezone, allowed),
		Blocked: withTZ(u.Timezone, blocked),
	})))
}
