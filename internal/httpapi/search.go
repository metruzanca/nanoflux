package httpapi

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"github.com/charmbracelet/log"

	"github.com/metruzanca/nanoflux/internal/auth"
	"github.com/metruzanca/nanoflux/internal/store"
	"github.com/metruzanca/nanoflux/internal/web"
)

// parsedSearch holds a search query broken into an FTS5 MATCH expression plus
// the scoping qualifiers Miniflux-style queries support: title:, author:,
// feed:, unread:.
type parsedSearch struct {
	fts    string // valid FTS5 MATCH expression
	author string // author: qualifier (author name)
	feed   string // feed: qualifier (feed title)
	unread bool
}

// tokenizeSearch splits raw into whitespace-separated tokens, keeping
// double-quoted runs intact so phrases like title:"two words" survive.
func tokenizeSearch(s string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			cur.WriteRune(r)
		case unicode.IsSpace(r) && !inQuote:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// ftsPhrase quotes a value as an FTS5 phrase so user input cannot alter the
// MATCH grammar. Embedded quotes are doubled per FTS5 escaping rules.
func ftsPhrase(v string) string {
	v = strings.Trim(v, `"`)
	v = strings.ReplaceAll(v, `"`, `""`)
	return `"` + v + `"`
}

// unquoteValue strips one pair of wrapping double quotes, if present.
func unquoteValue(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		return v[1 : len(v)-1]
	}
	return v
}

// parseSearchQuery turns a raw search string into an FTS5 MATCH expression and
// scoping qualifiers. General terms and title: values are phrase-quoted; the
// author:, feed:, and unread: qualifiers become filters applied outside FTS.
func parseSearchQuery(raw string) parsedSearch {
	var p parsedSearch
	var general, titles []string
	for _, tok := range tokenizeSearch(raw) {
		switch {
		case strings.HasPrefix(tok, "title:"):
			titles = append(titles, ftsPhrase(strings.TrimPrefix(tok, "title:")))
		case strings.HasPrefix(tok, "author:"):
			p.author = unquoteValue(strings.TrimPrefix(tok, "author:"))
		case strings.HasPrefix(tok, "feed:"):
			p.feed = unquoteValue(strings.TrimPrefix(tok, "feed:"))
		case strings.HasPrefix(tok, "unread:"):
			v := unquoteValue(strings.TrimPrefix(tok, "unread:"))
			p.unread = v == "1" || v == "true"
		default:
			general = append(general, ftsPhrase(tok))
		}
	}
	parts := general
	if len(titles) > 0 {
		parts = append([]string{"title:" + strings.Join(titles, " ")}, parts...)
	}
	p.fts = strings.Join(parts, " ")
	return p
}

// searchFilter resolves the qualifiers of a parsed query into an item filter.
func (s *Server) searchFilter(userID int64, p parsedSearch, before int64) store.ItemFilter {
	f := store.ItemFilter{BeforeID: before, Limit: pageSize}
	if p.unread {
		f.UnreadOnly = true
	}
	if p.author != "" {
		if a, err := s.store.Authors.ByName(userID, p.author); err == nil {
			f.AuthorID = a.ID
		}
	}
	if p.feed != "" {
		if f2, err := s.store.Feeds.ByTitle(userID, p.feed); err == nil {
			f.FeedID = f2.ID
		}
	}
	return f
}

type searchData struct {
	Query string
	Items []store.ItemWithFeed
	More  *loadMoreData
}

// searchPage renders results for GET /search?q=. With a before= cursor it
// serves the load-more fragment instead of a full page.
func (s *Server) searchPage(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	before := cursorID(r, false)

	if q == "" {
		web.Render(w, r, basePage("search", u, searchResultsPage(searchData{})))
		return
	}
	p := parseSearchQuery(q)
	if p.fts == "" {
		web.Render(w, r, basePage("search", u, searchResultsPage(searchData{Query: q})))
		return
	}
	items, more, err := s.store.Items.SearchPage(u.ID, p.fts, s.searchFilter(u.ID, p, before))
	if err != nil {
		log.Error("search", "q", q, "err", err)
		http.Error(w, "search failed", http.StatusInternalServerError)
		return
	}
	base := "/search?q=" + url.QueryEscape(q)
	if before > 0 {
		web.Render(w, r, ItemsPage(withTZ(u.Timezone, items), pageCursor(base, items, more, false), false))
		return
	}
	web.Render(w, r, basePage("search", u, searchResultsPage(searchData{
		Query: q, Items: withTZ(u.Timezone, items), More: pageCursor(base, items, more, false),
	})))
}

// apiSearch exposes search to the JSON API (for the browser extension).
func (s *Server) apiSearch(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "q required"})
		return
	}
	p := parseSearchQuery(q)
	if p.fts == "" {
		writeJSON(w, http.StatusOK, map[string]any{"items": []any{}})
		return
	}
	filter := s.searchFilter(u.ID, p, 0)
	if limit, _ := strconv.ParseInt(r.URL.Query().Get("limit"), 10, 64); limit > 0 {
		filter.Limit = int(limit)
	}
	items, _, err := s.store.Items.SearchPage(u.ID, p.fts, filter)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "search failed"})
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
