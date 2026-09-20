package httpapi

import (
	"github.com/charmbracelet/log"
	"html/template"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/metruzanca/nanoflux/internal/auth"
	"github.com/metruzanca/nanoflux/internal/store"
	"github.com/metruzanca/nanoflux/internal/web"
)

type homeData struct {
	Unread      []store.ItemWithFeed
	UnreadCount int
}

type readData struct {
	Read      []store.ItemWithFeed
	ReadCount int
}

type feedRow struct {
	store.Feed
	AuthorName string
	Unread     int
}

type feedForm struct {
	ID              int64
	Title           string
	FeedURL         string
	HomeURL         string
	Description     string
	AuthorID        int64
	PollIntervalSec int
	CollectionIDs   []int64
}

type feedsData struct {
	Rows        []feedRow
	Authors     []store.Author
	Collections []store.Collection
	Form        feedForm
}

type authorRow struct {
	store.Author
	FeedCount int
}

type authorForm struct {
	ID          int64
	Name        string
	URL         string
	AvatarURL   string
	Description string
}

type authorsData struct {
	Rows []authorRow
	Form authorForm
}

type authorData struct {
	Author store.Author
	Rows   []feedRow
	Items  []store.ItemWithFeed
}

type feedPageData struct {
	Row   feedRow
	Items []store.ItemWithFeed
}

type collectionData struct {
	Collection store.Collection
	Feeds      []store.Feed
	AllFeeds   []store.Feed
	Items      []store.ItemWithFeed
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	items, _ := s.store.Items.List(u.ID, store.ItemFilter{UnreadOnly: true, Limit: 100})
	unread, _ := s.store.Items.CountUnread(u.ID, 0)
	web.Render(w, "home", web.Page{Title: "unread", User: u, Data: homeData{
		Unread: items, UnreadCount: unread,
	}})
}

func (s *Server) itemsReadAll(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	if err := s.store.Items.MarkAllRead(u.ID, 0); err != nil {
		log.Error("mark all read", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderItemsList(w, r, u.ID)
}

func (s *Server) readPage(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	items, _ := s.store.Items.List(u.ID, store.ItemFilter{ReadOnly: true, Limit: 100})
	count, _ := s.store.Items.CountRead(u.ID, 0)
	web.Render(w, "read", web.Page{Title: "read", User: u, Data: readData{
		Read: items, ReadCount: count,
	}})
}

func (s *Server) itemsMarkAllUnread(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	if err := s.store.Items.MarkAllUnread(u.ID, 0); err != nil {
		log.Error("mark all unread", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderReadItemsList(w, u.ID)
}

func (s *Server) renderReadItemsList(w http.ResponseWriter, userID int64) {
	items, _ := s.store.Items.List(userID, store.ItemFilter{ReadOnly: true, Limit: 100})
	web.RenderFragment(w, "items_list", items)
}

func (s *Server) renderItemsList(w http.ResponseWriter, r *http.Request, userID int64) {
	items, _ := s.store.Items.List(userID, store.ItemFilter{UnreadOnly: true, Limit: 100})
	web.RenderFragment(w, "items_list", items)
}

type itemViewData struct {
	Title       string
	AuthorName  string
	FeedTitle   string
	FeedURL     string
	PublishedAt string
	Body        template.HTML
}

// itemView renders an item's stored content as a fragment, injected into the
// modal by the frontend.
func (s *Server) itemView(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	it, err := s.store.Items.OneWithFeed(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	web.RenderFragment(w, "item_view", itemViewData{
		Title:       it.Title,
		AuthorName:  it.AuthorName,
		FeedTitle:   it.FeedTitle,
		FeedURL:     it.FeedURL,
		PublishedAt: it.PublishedAt,
		Body:        template.HTML(it.Summary),
	})
}

func (s *Server) itemRead(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	it, err := s.store.Items.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.store.Items.SetRead(u.ID, id, !it.Read); err != nil {
		log.Error("set read", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	row, err := s.store.Items.OneWithFeed(u.ID, id)
	if err != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	web.RenderFragment(w, "item_row", row)
}

func (s *Server) feeds(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	rows, err := s.feedRows(u.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	authors, _ := s.store.Authors.List(u.ID)
	collections, _ := s.store.Collections.List(u.ID)
	web.Render(w, "feeds", web.Page{Title: "feeds", User: u, Data: feedsData{
		Rows: rows, Authors: authors, Collections: collections,
		Form: feedForm{PollIntervalSec: 900},
	}})
}

func (s *Server) feedRows(userID int64) ([]feedRow, error) {
	feeds, err := s.store.Feeds.List(userID)
	if err != nil {
		return nil, err
	}
	authors, _ := s.store.Authors.List(userID)
	names := make(map[int64]string, len(authors))
	for _, a := range authors {
		names[a.ID] = a.Name
	}
	rows := make([]feedRow, 0, len(feeds))
	for _, f := range feeds {
		unread, _ := s.store.Items.CountUnread(userID, f.ID)
		rows = append(rows, feedRow{Feed: f, AuthorName: names[f.AuthorID], Unread: unread})
	}
	return rows, nil
}

func (s *Server) feedCreate(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)

	title := strings.TrimSpace(r.FormValue("title"))
	feedURL := strings.TrimSpace(r.FormValue("feed_url"))
	homeURL := r.FormValue("home_url")
	interval, _ := strconv.Atoi(r.FormValue("poll_interval_sec"))
	if interval <= 0 {
		interval = 900
	}
	if title == "" || feedURL == "" {
		writeFormError(w, "add-feed-error", "title and feed url are required")
		return
	}

	authorID, errMsg := s.resolveAuthor(r, u.ID)
	if errMsg != "" {
		writeFormError(w, "add-feed-error", errMsg)
		return
	}

	f, err := s.store.Feeds.Create(u.ID, authorID, title, feedURL, homeURL, "", interval)
	if err != nil {
		log.Error("create feed", "err", err)
		writeFormError(w, "add-feed-error", "could not create feed")
		return
	}
	for _, cid := range r.Form["collections"] {
		if n, err := strconv.ParseInt(cid, 10, 64); err == nil {
			s.store.Collections.AddFeed(u.ID, n, f.ID)
		}
	}
	author, _ := s.store.Authors.ByID(u.ID, authorID)
	web.RenderFragment(w, "feed_row", feedRow{Feed: f, AuthorName: author.Name})
}

// resolveAuthor maps the feed form's author selection to an author id.
// "" or "0" means no author; "new" creates one, using the provided url as its
// home page. It returns a non-empty error message when the selection is invalid.
func (s *Server) resolveAuthor(r *http.Request, userID int64) (int64, string) {
	switch r.FormValue("author_id") {
	case "new":
		name := strings.TrimSpace(r.FormValue("author_name"))
		if name == "" {
			return 0, "new author needs a name"
		}
		a, err := s.store.Authors.Create(
			userID, name, r.FormValue("author_url"), r.FormValue("avatar_url"), "",
		)
		if err != nil {
			log.Error("create author", "err", err)
			return 0, "could not create author"
		}
		return a.ID, ""
	default:
		id, err := strconv.ParseInt(r.FormValue("author_id"), 10, 64)
		if err != nil {
			return 0, "" // empty or unparseable = no author
		}
		return id, ""
	}
}

func (s *Server) feedEdit(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	f, err := s.store.Feeds.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	form := feedForm{
		ID: f.ID, Title: f.Title, FeedURL: f.FeedURL, HomeURL: f.HomeURL,
		Description: f.Description, AuthorID: f.AuthorID,
		PollIntervalSec: f.PollIntervalSec,
	}
	collections, _ := s.store.Collections.List(u.ID)
	form.CollectionIDs = s.collectionIDsForFeed(u.ID, f.ID, collections)
	authors, _ := s.store.Authors.List(u.ID)

	web.Render(w, "feed_edit", web.Page{Title: "edit " + f.Title, User: u, Data: feedsData{
		Authors: authors, Collections: collections, Form: form,
	}})
}

func (s *Server) collectionIDsForFeed(userID, feedID int64, all []store.Collection) []int64 {
	var out []int64
	for _, c := range all {
		feeds, _ := s.store.Collections.Feeds(userID, c.ID)
		for _, f := range feeds {
			if f.ID == feedID {
				out = append(out, c.ID)
				break
			}
		}
	}
	return out
}

func (s *Server) feedUpdate(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	title := r.FormValue("title")
	feedURL := r.FormValue("feed_url")
	interval, _ := strconv.Atoi(r.FormValue("poll_interval_sec"))
	if interval <= 0 {
		interval = 900
	}
	if title == "" || feedURL == "" {
		http.Redirect(w, r, "/feeds", http.StatusFound)
		return
	}
	authorID, errMsg := s.resolveAuthor(r, u.ID)
	if errMsg != "" {
		http.Redirect(w, r, "/feeds", http.StatusFound)
		return
	}
	if err := s.store.Feeds.Update(u.ID, id, authorID, title, feedURL,
		r.FormValue("home_url"), "", interval, true); err != nil {
		log.Error("update feed", "err", err)
		http.Error(w, "update failed", http.StatusInternalServerError)
		return
	}
	// Sync collection memberships.
	form := feedForm{}
	collections, _ := s.store.Collections.List(u.ID)
	for _, cid := range r.Form["collections"] {
		if n, err := strconv.ParseInt(cid, 10, 64); err == nil {
			form.CollectionIDs = append(form.CollectionIDs, n)
		}
	}
	form.CollectionIDs = unique(form.CollectionIDs)
	for _, c := range collections {
		has := false
		for _, want := range form.CollectionIDs {
			if want == c.ID {
				has = true
				break
			}
		}
		feeds, _ := s.store.Collections.Feeds(u.ID, c.ID)
		contains := false
		for _, f := range feeds {
			if f.ID == id {
				contains = true
				break
			}
		}
		switch {
		case has && !contains:
			s.store.Collections.AddFeed(u.ID, c.ID, id)
		case !has && contains:
			s.store.Collections.RemoveFeed(u.ID, c.ID, id)
		}
	}
	http.Redirect(w, r, "/feeds", http.StatusFound)
}

func unique(ids []int64) []int64 {
	seen := map[int64]bool{}
	var out []int64
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func (s *Server) feedDelete(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.store.Feeds.Delete(u.ID, id); err != nil {
		log.Error("delete feed", "err", err)
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) feedRefresh(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	f, err := s.store.Feeds.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if s.poller != nil {
		if _, err := s.poller.PollOne(r.Context(), f); err != nil {
			log.Error("refresh feed", "feed_id", id, "err", err)
		}
	}
	unread, _ := s.store.Items.CountUnread(u.ID, id)
	author, _ := s.store.Authors.ByID(u.ID, f.AuthorID)
	web.RenderFragment(w, "feed_row", feedRow{Feed: f, AuthorName: author.Name, Unread: unread})
}

func (s *Server) authors(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	rows := s.authorRows(u.ID)
	web.Render(w, "authors", web.Page{Title: "authors", User: u, Data: authorsData{Rows: rows}})
}

func (s *Server) authorRows(userID int64) []authorRow {
	authors, _ := s.store.Authors.List(userID)
	rows := make([]authorRow, 0, len(authors))
	for _, a := range authors {
		feeds, _ := s.store.Feeds.ListByAuthor(userID, a.ID)
		rows = append(rows, authorRow{Author: a, FeedCount: len(feeds)})
	}
	return rows
}

func (s *Server) authorCreate(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		writeFormError(w, "add-author-error", "name is required")
		return
	}
	a, err := s.store.Authors.Create(u.ID, name, r.FormValue("url"), r.FormValue("avatar_url"), r.FormValue("description"))
	if err != nil {
		log.Error("create author", "err", err)
		writeFormError(w, "add-author-error", "could not create author")
		return
	}
	web.RenderFragment(w, "author_row", authorRow{Author: a})
}

func (s *Server) authorPage(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	a, err := s.store.Authors.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	rows, err := s.feedRowsForAuthor(u.ID, id)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	items, _ := s.store.Items.List(u.ID, store.ItemFilter{AuthorID: id, Limit: 100})
	web.Render(w, "author", web.Page{Title: a.Name, User: u, Data: authorData{
		Author: a, Rows: rows, Items: items,
	}})
}

func (s *Server) feedPage(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	feed, err := s.store.Feeds.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	authorName := ""
	if feed.AuthorID != 0 {
		if a, err := s.store.Authors.ByID(u.ID, feed.AuthorID); err == nil {
			authorName = a.Name
		}
	}
	unread, _ := s.store.Items.CountUnread(u.ID, id)
	items, _ := s.store.Items.List(u.ID, store.ItemFilter{FeedID: id, Limit: 100})
	web.Render(w, "feed", web.Page{Title: feed.Title, User: u, Data: feedPageData{
		Row: feedRow{Feed: feed, AuthorName: authorName, Unread: unread}, Items: items,
	}})
}

func (s *Server) feedRowsForAuthor(userID, authorID int64) ([]feedRow, error) {
	feeds, err := s.store.Feeds.ListByAuthor(userID, authorID)
	if err != nil {
		return nil, err
	}
	author, _ := s.store.Authors.ByID(userID, authorID)
	rows := make([]feedRow, 0, len(feeds))
	for _, f := range feeds {
		unread, _ := s.store.Items.CountUnread(userID, f.ID)
		rows = append(rows, feedRow{Feed: f, AuthorName: author.Name, Unread: unread})
	}
	return rows, nil
}

func (s *Server) authorEdit(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	a, err := s.store.Authors.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	web.Render(w, "author_edit", web.Page{Title: "edit " + a.Name, User: u, Data: authorsData{
		Form: authorForm{ID: a.ID, Name: a.Name, URL: a.URL, AvatarURL: a.AvatarURL, Description: a.Description},
	}})
}

func (s *Server) authorUpdate(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	name := r.FormValue("name")
	if name == "" {
		http.Redirect(w, r, "/authors", http.StatusFound)
		return
	}
	if err := s.store.Authors.Update(u.ID, id, name, r.FormValue("url"), r.FormValue("avatar_url"), r.FormValue("description")); err != nil {
		log.Error("update author", "err", err)
		http.Error(w, "update failed", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/authors/"+r.PathValue("id"), http.StatusFound)
}

func (s *Server) authorDelete(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.store.Authors.Delete(u.ID, id); err != nil {
		log.Error("delete author", "err", err)
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) authorFormFragment(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("author_id") != "new" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// When adding a feed, the fragment is asked with the feed's home url so the
	// new author's name/avatar can be derived from it.
	var name, homeURL, avatar string
	if home := strings.TrimSpace(r.FormValue("home_url")); home != "" {
		if meta, err := s.discoverer.PageMeta(r.Context(), home); err == nil {
			name, homeURL, avatar = meta.Title, meta.HomeURL, meta.IconURL
		}
	}
	web.RenderFragment(w, "author_create_fields", authorPreviewForm{
		Name: name, URL: homeURL, AvatarURL: avatar,
	})
}

func (s *Server) collections(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	rows, _ := s.store.Collections.List(u.ID)
	web.Render(w, "collections", web.Page{Title: "collections", User: u, Data: rows})
}

func (s *Server) collectionCreate(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		writeFormError(w, "add-collection-error", "name is required")
		return
	}
	c, err := s.store.Collections.Create(u.ID, name)
	if err != nil {
		log.Error("create collection", "err", err)
		writeFormError(w, "add-collection-error", "could not create collection")
		return
	}
	web.RenderFragment(w, "collection_row", c)
}

func (s *Server) collectionPage(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	d, err := s.collectionDataFor(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	web.Render(w, "collection", web.Page{Title: d.Collection.Name, User: u, Data: d})
}

func (s *Server) collectionDataFor(userID, id int64) (collectionData, error) {
	c, err := s.store.Collections.ByID(userID, id)
	if err != nil {
		return collectionData{}, err
	}
	feeds, _ := s.store.Collections.Feeds(userID, id)
	allFeeds, _ := s.store.Feeds.List(userID)
	items, _ := s.store.Items.List(userID, store.ItemFilter{CollectionID: id, Limit: 100})
	return collectionData{Collection: c, Feeds: feeds, AllFeeds: allFeeds, Items: items}, nil
}

func (s *Server) collectionDelete(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.store.Collections.Delete(u.ID, id); err != nil {
		log.Error("delete collection", "err", err)
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) collectionAddFeed(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	feedID, _ := strconv.ParseInt(r.FormValue("feed_id"), 10, 64)
	if feedID != 0 {
		s.store.Collections.AddFeed(u.ID, id, feedID)
	}
	s.renderCollectionFeeds(w, u.ID, id)
}

func (s *Server) collectionRemoveFeed(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	feedID, err := strconv.ParseInt(r.PathValue("feed_id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.store.Collections.RemoveFeed(u.ID, id, feedID)
	s.renderCollectionFeeds(w, u.ID, id)
}

func (s *Server) renderCollectionFeeds(w http.ResponseWriter, userID, id int64) {
	d, err := s.collectionDataFor(userID, id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	web.RenderFragment(w, "collection_updated", d)
}

// writeFormError responds to an htmx add-form submit with an out-of-band swap
// that renders msg into the modal's error div without disturbing the form.
func writeFormError(w http.ResponseWriter, target, msg string) {
	w.WriteHeader(http.StatusBadRequest)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	io.WriteString(w, `<div id="`+target+`" hx-swap-oob="innerHTML">`+
		template.HTMLEscapeString(msg)+`</div>`)
}
