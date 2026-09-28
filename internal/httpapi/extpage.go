package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/log"

	"github.com/metruzanca/nanoflux/internal/auth"
	"github.com/metruzanca/nanoflux/internal/store"
	"github.com/metruzanca/nanoflux/internal/web"
)

// saveTargetBookmarks is the picker value for the native bookmarks list: saved
// pages land in bookmarks by default, so saving is not tied to a user-created
// list.
const saveTargetBookmarks = "bookmarks"

// savedListItems maps the save-page picker's options: the native bookmarks list
// first (the default target), then the user's lists by id.
func savedListItems(rows []store.ListWithCount) []comboItem {
	out := make([]comboItem, 0, len(rows)+1)
	out = append(out, comboItem{Value: saveTargetBookmarks, Label: "bookmarks"})
	for _, l := range rows {
		out = append(out, comboItem{Value: strconv.FormatInt(l.ID, 10), Label: l.Name})
	}
	return out
}

// apiExtPageForm returns the save-page form as an HTML fragment for the browser
// extension popup. The default target is the native bookmarks list.
func (s *Server) apiExtPageForm(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	pageURL := normalizeURL(r.FormValue("url"))
	if pageURL == "" {
		web.Render(w, r, extError("page url required"))
		return
	}
	title := strings.TrimSpace(r.FormValue("title"))
	rows, _ := s.store.Lists.List(u.ID)
	web.Render(w, r, extPageForm(pageURL, title, saveTargetBookmarks, savedListItems(rows)))
}

// savePageTo resolves the destination and stores the posted page, fetching its
// metadata server-side so the item renders like a feed entry. The destination is
// the native bookmarks list (the default), a posted list_id, or a list named by
// the optional new_list field (created on demand). It returns the saved item id
// (for a deep link), title and destination name, or a user-facing error message.
// Shared by the browser extension (apiExtPageSave) and the in-app dialog
// (savePageWeb).
func (s *Server) savePageTo(r *http.Request, u store.User) (itemID int64, title, listName, errMsg string) {
	pageURL := normalizeURL(r.FormValue("url"))
	if pageURL == "" {
		return 0, "", "", "page url required"
	}

	target := strings.TrimSpace(r.FormValue("list_id"))
	var listID int64
	bookmark := target == "" || target == saveTargetBookmarks
	if name := strings.TrimSpace(r.FormValue("new_list")); name != "" {
		l, err := s.store.Lists.Ensure(u.ID, name)
		if err != nil {
			log.Error("save page list", "err", err)
			return 0, "", "", "could not create that list"
		}
		listID, bookmark = l.ID, false
	} else if !bookmark {
		v, _ := strconv.ParseInt(target, 10, 64)
		if v == 0 {
			return 0, "", "", "list not found"
		}
		if _, err := s.store.Lists.ByID(u.ID, v); err != nil {
			return 0, "", "", "list not found"
		}
		listID = v
	}

	title = strings.TrimSpace(r.FormValue("title"))
	summary, imageURL, metaTitle := s.pageMetaFor(r.Context(), pageURL)
	if title == "" {
		title = metaTitle
	}
	if title == "" {
		title = pageURL
	}

	// The guid is the normalized page URL so re-saving is idempotent; fall back
	// to the raw URL when it cannot be normalized.
	key := normExtKey(pageURL)
	if key == "" {
		key = pageURL
	}
	guid := "page:" + key
	itemID, _, err := s.store.SavePage(u.ID, listID, guid, title, pageURL, summary, imageURL)
	if err != nil {
		log.Error("save page", "url", pageURL, "err", err)
		return 0, "", "", "could not save that page"
	}
	if bookmark {
		listName = "bookmarks"
		if err := s.store.Items.SetBookmark(u.ID, itemID, true); err != nil {
			log.Error("bookmark saved page", "item_id", itemID, "err", err)
			return 0, "", "", "could not save that page"
		}
	} else {
		l, _ := s.store.Lists.ByID(u.ID, listID)
		listName = l.Name
	}
	return itemID, title, listName, ""
}

// apiExtPageSave stores the current page as an item. The destination is the
// native bookmarks list (the default), the posted list_id, or a list named by
// the optional new_list field (created on demand). Page metadata (summary,
// image, title fallback) is fetched server-side so the item renders like a feed
// entry.
func (s *Server) apiExtPageSave(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	itemID, title, listName, errMsg := s.savePageTo(r, u)
	if errMsg != "" {
		web.Render(w, r, extError(errMsg))
		return
	}
	web.Render(w, r, extPageSaved(itemID, title, listName))
}

// savePageFormFragment renders the in-app "save url for later" form into the
// shared #save-page-dialog. The default target is the native bookmarks list.
func (s *Server) savePageFormFragment(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	rows, _ := s.store.Lists.List(u.ID)
	web.Render(w, r, savePageForm(saveTargetBookmarks, savedListItems(rows)))
}

// savePageWeb stores a page submitted from the in-app dialog.
func (s *Server) savePageWeb(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	itemID, title, listName, errMsg := s.savePageTo(r, u)
	if errMsg != "" {
		writeFormError(w, r, "save-page-error", errMsg)
		return
	}
	web.Render(w, r, savePageSaved(itemID, title, listName))
}

// pageMetaFor fetches a saved page's title, description and og:image, with a
// short timeout so a slow page never blocks the save. Any failure degrades to
// empty values; the page is still saved with what the client provided.
func (s *Server) pageMetaFor(ctx context.Context, pageURL string) (summary, imageURL, title string) {
	cctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	meta, err := s.discoverer.PageMeta(cctx, pageURL)
	if err != nil {
		return "", "", ""
	}
	return meta.Description, meta.ImageURL, meta.Title
}
