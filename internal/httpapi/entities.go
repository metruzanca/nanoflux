package httpapi

import (
	"net/http"
	"strconv"

	"github.com/metruzanca/nanoflux/internal/auth"
)

// apiEntity is one searchable entity in the command palette's unified search:
// an author, a collection, a feed, or a saved view. The kind drives the sigil
// the client prefixes (@ author, # collection, ! feed and view) and lets a bare
// sigil filter down to the kinds that carry it.
type apiEntity struct {
	Kind   string `json:"kind"` // "author", "collection", "feed", or "view"
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	URL    string `json:"url"`
	Avatar string `json:"avatar,omitempty"` // author avatar; empty when there is none
	Unread int    `json:"unread"`           // unread items owned by this entity
}

// apiEntities lists the user's authors, collections, feeds, and saved views for
// the command palette's unified search. Names are matched client-side; this is a
// small per-user list, so one call returns everything.
func (s *Server) apiEntities(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)

	authors, err := s.store.Authors.ListWithFeedCount(u.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	collections, err := s.store.Collections.ListWithCounts(u.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	feeds, err := s.store.Feeds.ListWithUnread(u.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	views, err := s.store.SavedViews.List(u.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	out := make([]apiEntity, 0, len(authors)+len(collections)+len(feeds)+len(views))
	counts, _ := s.store.Items.UnreadCountsByAuthor(u.ID)
	for _, a := range authors {
		out = append(out, apiEntity{Kind: "author", ID: a.ID, Name: a.Name, URL: "/authors/" + strconv.FormatInt(a.ID, 10), Avatar: authorAvatarSrc(a.Author), Unread: counts[a.ID]})
	}
	for _, c := range collections {
		out = append(out, apiEntity{Kind: "collection", ID: c.ID, Name: c.Name, URL: "/collections/" + strconv.FormatInt(c.ID, 10), Unread: c.Unread})
	}
	for _, f := range feeds {
		out = append(out, apiEntity{Kind: "feed", ID: f.ID, Name: f.Title, URL: authorFeedURL(f.AuthorID, f.ID), Unread: f.Unread})
	}
	// A view is a source on its author's page, like a feed, so it opens at the
	// author's scoped list. Its unread count is the view's own recipe count;
	// an unfiltered or legacy blank view is skipped, exactly as the author page
	// skips it.
	titles := s.savedViewFeedTitles(u.ID)
	names := s.savedViewAuthorNames(u.ID)
	for _, v := range views {
		if v.AuthorID <= 0 {
			continue
		}
		filters := savedViewFiltersWithTitles(filtersFromParams(v.Filters), titles, names)
		if len(filters) == 0 {
			continue
		}
		unread, _ := s.savedViewCount(u.ID, v.AuthorID, "unread", filters, u.Timezone)
		out = append(out, apiEntity{
			Kind: "view", ID: v.ID, Name: v.Title,
			URL:    "/authors/" + strconv.FormatInt(v.AuthorID, 10) + "?view=" + strconv.FormatInt(v.ID, 10),
			Unread: unread,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"entities": out})
}
