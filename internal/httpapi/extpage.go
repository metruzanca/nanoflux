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

// defaultSavedList is the list a saved page lands in when the user does not
// pick another one. It is created on demand.
const defaultSavedList = "watch later"

// savedListItems maps a user's lists to combo items for the extension's
// save-page picker.
func savedListItems(rows []store.ListWithCount) []comboItem {
	out := make([]comboItem, 0, len(rows))
	for _, l := range rows {
		out = append(out, comboItem{Value: strconv.FormatInt(l.ID, 10), Label: l.Name})
	}
	return out
}

// defaultSavedListID returns the id of the default "watch later" list,
// creating it when absent.
func (s *Server) defaultSavedListID(userID int64) (int64, error) {
	l, err := s.store.Lists.Ensure(userID, defaultSavedList)
	if err != nil {
		return 0, err
	}
	return l.ID, nil
}

// apiExtPageForm returns the save-page form as an HTML fragment for the browser
// extension popup. It creates the default list on demand so the picker always
// has a sensible initial selection.
func (s *Server) apiExtPageForm(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	pageURL := normalizeURL(r.FormValue("url"))
	if pageURL == "" {
		web.Render(w, r, extError("page url required"))
		return
	}
	title := strings.TrimSpace(r.FormValue("title"))
	defaultID, err := s.defaultSavedListID(u.ID)
	if err != nil {
		internalError(w, "could not prepare list", err)
		return
	}
	rows, _ := s.store.Lists.List(u.ID)
	web.Render(w, r, extPageForm(pageURL, title, extDefaultListValue(rows, defaultID), savedListItems(rows)))
}

// extDefaultListValue is the picker's initial selected value: the default
// "watch later" list's id, falling back to the first list when it is missing.
func extDefaultListValue(rows []store.ListWithCount, defaultID int64) string {
	for _, l := range rows {
		if l.ID == defaultID {
			return strconv.FormatInt(l.ID, 10)
		}
	}
	if len(rows) > 0 {
		return strconv.FormatInt(rows[0].ID, 10)
	}
	return ""
}

// apiExtPageSave stores the current page as an item in a list. The list is the
// posted list_id, or a list named by the optional new_list field (created on
// demand), or the default "watch later" list. Page metadata (summary, image,
// title fallback) is fetched server-side so the item renders like a feed entry.
func (s *Server) apiExtPageSave(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	pageURL := normalizeURL(r.FormValue("url"))
	if pageURL == "" {
		web.Render(w, r, extError("page url required"))
		return
	}

	var listID int64
	if name := strings.TrimSpace(r.FormValue("new_list")); name != "" {
		l, err := s.store.Lists.Ensure(u.ID, name)
		if err != nil {
			log.Error("extension save page list", "err", err)
			web.Render(w, r, extError("could not create that list"))
			return
		}
		listID = l.ID
	} else if v, _ := strconv.ParseInt(r.FormValue("list_id"), 10, 64); v != 0 {
		if _, err := s.store.Lists.ByID(u.ID, v); err != nil {
			web.Render(w, r, extError("list not found"))
			return
		}
		listID = v
	} else {
		id, err := s.defaultSavedListID(u.ID)
		if err != nil {
			internalError(w, "could not prepare list", err)
			return
		}
		listID = id
	}

	title := strings.TrimSpace(r.FormValue("title"))
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
	if _, _, err := s.store.SavePage(u.ID, listID, guid, title, pageURL, summary, imageURL); err != nil {
		log.Error("extension save page", "url", pageURL, "err", err)
		web.Render(w, r, extError("could not save that page"))
		return
	}
	l, _ := s.store.Lists.ByID(u.ID, listID)
	web.Render(w, r, extPageSaved(title, l.Name))
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
