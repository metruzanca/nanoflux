package httpapi

import (
	"net/http"
	"strconv"

	"github.com/metruzanca/nanoflux/internal/auth"
)

// apiEntity is one searchable entity in the command palette's unified search:
// an author, a collection, or a feed. The kind drives the sigil the client
// prefixes (@ author, # collection, ! feed) and lets a bare sigil filter down
// to one kind.
type apiEntity struct {
	Kind   string `json:"kind"` // "author", "collection", or "feed"
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	URL    string `json:"url"`
	Unread int    `json:"unread"` // unread items owned by this entity
}

// apiEntities lists the user's authors, collections, and feeds for the
// command palette's unified search. Names are matched client-side; this is a
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

	out := make([]apiEntity, 0, len(authors)+len(collections)+len(feeds))
	for _, a := range authors {
		out = append(out, apiEntity{Kind: "author", ID: a.ID, Name: a.Name, URL: "/authors/" + strconv.FormatInt(a.ID, 10), Unread: a.UnreadCount})
	}
	for _, c := range collections {
		out = append(out, apiEntity{Kind: "collection", ID: c.ID, Name: c.Name, URL: "/collections/" + strconv.FormatInt(c.ID, 10), Unread: c.Unread})
	}
	for _, f := range feeds {
		out = append(out, apiEntity{Kind: "feed", ID: f.ID, Name: f.Title, URL: "/feeds/" + strconv.FormatInt(f.ID, 10), Unread: f.Unread})
	}
	writeJSON(w, http.StatusOK, map[string]any{"entities": out})
}
