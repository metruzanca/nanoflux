package httpapi

import (
	"strconv"

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

// collectionValues renders the selected collection ids as combo values.
func collectionValues(ids []int64) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, strconv.FormatInt(id, 10))
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
