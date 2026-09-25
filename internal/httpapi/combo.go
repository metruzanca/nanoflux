package httpapi

import (
	"strconv"

	"github.com/metruzanca/nanoflux/internal/discover"
	"github.com/metruzanca/nanoflux/internal/store"
)

// comboItem is one option for a Vaadin combo-box. Value is the form value
// (usually a numeric id as a string); Label is what the user sees and types to
// filter. Group is optional and only used to render a section heading.
type comboItem struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Group string `json:"group,omitempty"`
}

// comboPayload is the JSON payload a combo wrapper embeds for the client to
// turn into the component's `items` (and initial selection). The components take
// options as a JS array property, so options are shipped as data, not markup.
type comboPayload struct {
	Items []comboItem `json:"items"`
	Value any         `json:"value,omitempty"` // string for single, []string for multi
}

// authorItems maps authors to combo items with a leading "+ create new author"
// option (value "new").
func authorItems(authors []store.Author) []comboItem {
	items := make([]comboItem, 0, len(authors)+1)
	items = append(items, comboItem{Value: "new", Label: "+ create new author"})
	for _, a := range authors {
		items = append(items, comboItem{Value: strconv.FormatInt(a.ID, 10), Label: a.Name})
	}
	return items
}

// authorValue is the combo's initial value for an author selection: the author
// id, or "new" when none is selected (id 0).
func authorValue(selected int64) string {
	if selected == 0 {
		return "new"
	}
	return strconv.FormatInt(selected, 10)
}

// candidateItems maps discovered feeds to combo items (feed URL value, title
// label falling back to the URL).
func candidateItems(cs []discover.Candidate) []comboItem {
	out := make([]comboItem, 0, len(cs))
	for _, c := range cs {
		label := c.Title
		if label == "" {
			label = c.FeedURL
		}
		out = append(out, comboItem{Value: c.FeedURL, Label: label})
	}
	return out
}

// candidateValue is the combo's initial value: the first candidate's feed URL.
func candidateValue(cs []discover.Candidate) string {
	if len(cs) == 0 {
		return ""
	}
	return cs[0].FeedURL
}

// groupedFeedItems maps author-grouped feeds to combo items, prefixing each
// label with its author so the author stays visible (v25 combo boxes have no
// optgroup equivalent) and searchable.
func groupedFeedItems(groups []collectionFeedGroup) []comboItem {
	out := make([]comboItem, 0)
	for _, g := range groups {
		for _, f := range g.Feeds {
			out = append(out, comboItem{
				Value: strconv.FormatInt(f.ID, 10),
				Label: g.AuthorName + " · " + f.Title,
			})
		}
	}
	return out
}

// firstGroupedFeedValue is the default selection for the grouped feed picker:
// the first feed's id, or "" when there are none.
func firstGroupedFeedValue(groups []collectionFeedGroup) string {
	for _, g := range groups {
		if len(g.Feeds) > 0 {
			return strconv.FormatInt(g.Feeds[0].ID, 10)
		}
	}
	return ""
}

// extAuthorItems maps authors to combo items with a leading "auto — new author"
// option (empty value), which the extension save handler treats as "create one".
func extAuthorItems(authors []store.Author) []comboItem {
	items := make([]comboItem, 0, len(authors)+1)
	items = append(items, comboItem{Value: "", Label: "auto — new author"})
	for _, a := range authors {
		items = append(items, comboItem{Value: strconv.FormatInt(a.ID, 10), Label: a.Name})
	}
	return items
}

// filterActionItems and filterFieldItems are the fixed choices for a feed
// filter rule (action and field).
var filterActionItems = []comboItem{
	{Value: "hide", Label: "hide"},
	{Value: "mark_read", Label: "mark read"},
}

var filterFieldItems = []comboItem{
	{Value: "title", Label: "title"},
	{Value: "summary", Label: "summary"},
	{Value: "link", Label: "link"},
}

// nonAutoCollections returns the user's own collections (auto collections are
// managed from each feed's site and are not user-selectable on the feed form).
func nonAutoCollections(cs []store.Collection) []comboItem {
	out := make([]comboItem, 0, len(cs))
	for _, c := range cs {
		if !c.IsAuto {
			out = append(out, comboItem{Value: strconv.FormatInt(c.ID, 10), Label: c.Name})
		}
	}
	return out
}

// nonAutoCollectionValues returns the selected collection ids that are
// user-selectable (non-auto), so auto collections never appear as combo
// selections even though they are part of the feed's membership.
func nonAutoCollectionValues(collections []store.Collection, ids []int64) []string {
	selectable := map[int64]bool{}
	for _, c := range collections {
		if !c.IsAuto {
			selectable[c.ID] = true
		}
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if selectable[id] {
			out = append(out, strconv.FormatInt(id, 10))
		}
	}
	return out
}

// feedItems maps feeds to combo items (id value, title label).
func feedItems(feeds []store.Feed) []comboItem {
	out := make([]comboItem, 0, len(feeds))
	for _, f := range feeds {
		out = append(out, comboItem{Value: strconv.FormatInt(f.ID, 10), Label: f.Title})
	}
	return out
}

// feedIDs renders feed ids as combo values, in the same order as feedItems.
func feedIDs(feeds []store.Feed) []string {
	out := make([]string, 0, len(feeds))
	for _, f := range feeds {
		out = append(out, strconv.FormatInt(f.ID, 10))
	}
	return out
}

// removeFeedURL is the per-feed remove endpoint prefix for a user collection, or
// "" for an auto collection (its chips are readonly, so no removal is offered).
func removeFeedURL(c store.Collection) string {
	if c.IsAuto {
		return ""
	}
	return "/collections/" + strconv.FormatInt(c.ID, 10) + "/remove-feed/"
}
