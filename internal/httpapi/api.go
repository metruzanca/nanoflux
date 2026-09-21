package httpapi

import (
	"encoding/json"
	"github.com/charmbracelet/log"
	"net/http"
	"strconv"
	"time"

	"github.com/metruzanca/nanoflux/internal/auth"
	"github.com/metruzanca/nanoflux/internal/discover"
	"github.com/metruzanca/nanoflux/internal/feedparse"
	"github.com/metruzanca/nanoflux/internal/store"
)

type apiItem struct {
	ID          int64  `json:"id"`
	FeedID      int64  `json:"feed_id"`
	GUID        string `json:"guid"`
	Title       string `json:"title"`
	Link        string `json:"link"`
	Summary     string `json:"summary"`
	ImageURL    string `json:"image_url,omitempty"`
	PublishedAt string `json:"published_at,omitempty"`
	Read        bool   `json:"read"`
	FeedTitle   string `json:"feed_title"`
	FeedURL     string `json:"feed_url"`
	AuthorID    int64  `json:"author_id"`
	AuthorName  string `json:"author_name"`
}

type apiFeed struct {
	ID         int64  `json:"id"`
	Title      string `json:"title"`
	FeedURL    string `json:"feed_url"`
	HomeURL    string `json:"home_url,omitempty"`
	AuthorID   int64  `json:"author_id"`
	AuthorName string `json:"author_name"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Error("write json", "err", err)
	}
}

func readJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

func (s *Server) apiLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if s.loginLimiter.blocked(ip) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many attempts — try again later"})
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	u, err := s.store.Users.ByUsername(req.Username)
	if err != nil || !auth.CheckPassword(u.PasswordHash, req.Password) {
		s.loginLimiter.fail(ip)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}
	s.loginLimiter.success(ip)
	token, err := s.auth.CreateSession(u.ID)
	if err != nil {
		log.Error("create session", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	s.auth.SetCookie(w, r, token) // convenience for same-origin web use
	writeJSON(w, http.StatusOK, map[string]any{
		"token":    token,
		"user_id":  u.ID,
		"username": u.Username,
	})
}

func (s *Server) apiUnreadCount(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	n, err := s.store.Items.CountUnread(u.ID, 0)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"count": n})
}

func (s *Server) apiItems(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	q := r.URL.Query()
	filter := store.ItemFilter{
		UnreadOnly: q.Get("unread") == "1" || q.Get("unread") == "true",
	}
	filter.FeedID, _ = strconv.ParseInt(q.Get("feed"), 10, 64)
	filter.AuthorID, _ = strconv.ParseInt(q.Get("author"), 10, 64)
	filter.CollectionID, _ = strconv.ParseInt(q.Get("collection"), 10, 64)
	filter.BeforeID, _ = strconv.ParseInt(q.Get("before"), 10, 64)
	filter.Limit, _ = strconv.Atoi(q.Get("limit"))

	items, err := s.store.Items.List(u.ID, filter)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	out := make([]apiItem, 0, len(items))
	for _, it := range items {
		out = append(out, apiItem{
			ID: it.ID, FeedID: it.FeedID, GUID: it.GUID, Title: it.Title, Link: it.Link,
			Summary: it.Summary, ImageURL: it.ImageURL, PublishedAt: it.PublishedAt, Read: it.Read,
			FeedTitle: it.FeedTitle, FeedURL: it.FeedURL, AuthorID: it.AuthorID, AuthorName: it.AuthorName,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) apiItemRead(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	read := true
	if body := struct {
		Read *bool `json:"read"`
	}{}; r.ContentLength != 0 {
		_ = readJSON(r, &body)
		if body.Read != nil {
			read = *body.Read
		}
	}
	if err := s.store.Items.SetRead(u.ID, id, read); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "item not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true, "read": read})
}

func (s *Server) apiDiscover(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	var req struct {
		URL string `json:"url"`
	}
	if err := readJSON(r, &req); err != nil || req.URL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "url required"})
		return
	}
	// Apply the user's url mappings first; fall back to the original when the
	// mapped url yields nothing.
	feedURL := req.URL
	if mapped, ok := s.mappedFeedURL(u.ID, req.URL); ok {
		feedURL = mapped
	}
	candidates, err := s.discoverer.Discover(r.Context(), feedURL)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if len(candidates) == 0 && feedURL != req.URL {
		if candidates, err = s.discoverer.Discover(r.Context(), req.URL); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	saved := s.savedFeedsFor(u.ID)
	type apiCandidate struct {
		discover.Candidate
		Saved       bool  `json:"saved"`
		SavedFeedID int64 `json:"saved_feed_id,omitempty"`
	}
	out := make([]apiCandidate, 0, len(candidates))
	for _, c := range candidates {
		id := saved.saved(c.FeedURL)
		out = append(out, apiCandidate{Candidate: c, Saved: id != 0, SavedFeedID: id})
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": req.URL, "accent": u.AccentColor, "candidates": out})
}

func (s *Server) apiSave(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)

	var req struct {
		FeedURL  string `json:"feed_url"`
		Title    string `json:"title,omitempty"`
		AuthorID int64  `json:"author_id"`
		Author   *struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		} `json:"author,omitempty"`
		CollectionID int64 `json:"collection_id"`
	}
	if err := readJSON(r, &req); err != nil || req.FeedURL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "feed_url required"})
		return
	}

	// Confirm the URL is a real feed before saving.
	client := &http.Client{Timeout: 15 * time.Second}
	res, err := feedparse.Fetch(r.Context(), req.FeedURL, client, "", "")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not a feed: " + err.Error()})
		return
	}
	title := req.Title
	if title == "" {
		title = res.Feed.Title
	}
	if title == "" {
		title = req.FeedURL
	}

	// Resolve the author: an existing id, one created from the provided object,
	// or (every feed needs an author) one auto-created from the feed itself.
	authorID := req.AuthorID
	if authorID == 0 && req.Author != nil {
		if req.Author.Name == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "author name required"})
			return
		}
		a, err := s.store.Authors.Create(u.ID, req.Author.Name, req.Author.URL, "", "")
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "create author failed"})
			return
		}
		authorID = a.ID
	}
	if authorID == 0 {
		a, err := s.store.Authors.Create(u.ID, title, res.Feed.HomeURL, "", "")
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "create author failed"})
			return
		}
		authorID = a.ID
	} else if _, err := s.store.Authors.ByID(u.ID, authorID); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "author not found"})
		return
	}

	f, err := s.store.Feeds.Create(u.ID, authorID, title, req.FeedURL, res.Feed.HomeURL, "", 900)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "create feed failed"})
		return
	}
	if req.CollectionID != 0 {
		if err := s.store.Collections.AddFeed(u.ID, req.CollectionID, f.ID); err != nil {
			log.Error("add to collection", "collection_id", req.CollectionID, "err", err)
		}
	}
	if err := s.store.Collections.AssignAuto(u.ID, f.ID, res.Feed.HomeURL, req.FeedURL); err != nil {
		log.Error("assign auto collection", "feed_id", f.ID, "err", err)
	}
	s.pollFeedNow(f)

	author, _ := s.store.Authors.ByID(u.ID, authorID)
	writeJSON(w, http.StatusOK, apiFeed{
		ID: f.ID, Title: f.Title, FeedURL: f.FeedURL, HomeURL: f.HomeURL,
		AuthorID: f.AuthorID, AuthorName: author.Name,
	})
}
