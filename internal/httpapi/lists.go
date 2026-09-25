package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/charmbracelet/log"

	"github.com/metruzanca/nanoflux/internal/auth"
	"github.com/metruzanca/nanoflux/internal/store"
	"github.com/metruzanca/nanoflux/internal/web"
)

type listsPageData struct {
	FavoritesCount    int
	FavoritesShareTok string
	Lists             []store.ListWithCount
}

type listPageData struct {
	List  store.List
	Items []store.ItemWithFeed
	Dir   string
	More  *loadMoreData
	Mode  string // saved display mode for "/lists/{id}"
}

// sharedListData renders the public, unauthenticated view of a shared list.
type sharedListData struct {
	Path  string // "/l/{token}" or "/f/{token}", for the load-more link
	Name  string
	Items []store.ItemWithFeed
	More  int64 // last item id for the plain "load more" link, 0 when none
}

// listsPage renders the lists index: the special favorites list pinned first,
// then every user-created list.
func (s *Server) listsPage(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	rows, _ := s.store.Lists.List(u.ID)
	favCount, _ := s.store.Items.CountFavorites(u.ID, 0)
	favTok, _ := s.store.Users.FavoritesShareToken(u.ID)
	web.Render(w, r, basePage("lists", u, listsPage(u, listsPageData{
		FavoritesCount:    favCount,
		FavoritesShareTok: favTok,
		Lists:             rows,
	})))
}

func (s *Server) listCreate(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		writeFormError(w, r, "add-list-error", "name is required")
		return
	}
	l, err := s.store.Lists.Create(u.ID, name)
	if err != nil {
		log.Error("create list", "err", err)
		writeFormError(w, r, "add-list-error", "could not create list")
		return
	}
	web.Render(w, r, ListRow(store.ListWithCount{List: l}))
}

func (s *Server) listPage(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	l, err := s.store.Lists.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	asc := itemsAsc(r)
	items, more, _ := s.store.Lists.ItemList(u.ID, id, 0, pageSize, asc)
	base := "/lists/" + strconv.FormatInt(id, 10) + "/items?dir=" + dirParam(asc)
	web.Render(w, r, basePage(l.Name, u, listPage(u, listPageData{
		List: l, Items: withTZ(u.Timezone, items), Dir: dirParam(asc), More: pageCursor(base, items, more, asc),
		Mode: s.store.ViewPrefs.Mode(u.ID, "/lists/"+strconv.FormatInt(id, 10)),
	})))
}

// listItems serves a "load more" page of a list's items, appended to the
// existing #items-list.
func (s *Server) listItems(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	asc := itemsAsc(r)
	items, more, err := s.store.Lists.ItemList(u.ID, id, cursorID(r, asc), pageSize, asc)
	if err != nil {
		log.Error("list items", "list_id", id, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	base := "/lists/" + strconv.FormatInt(id, 10) + "/items?dir=" + dirParam(asc)
	web.Render(w, r, ItemsPage(withTZ(u.Timezone, items), pageCursor(base, items, more, asc), false))
}

// listEdit renders a list's edit form (rename + delete).
func (s *Server) listEdit(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	l, err := s.store.Lists.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	web.Render(w, r, basePage("edit "+l.Name, u, listEditPage(u, listPageData{List: l})))
}

// listUpdate renames a list.
func (s *Server) listUpdate(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if _, err := s.store.Lists.ByID(u.ID, id); err != nil {
		http.NotFound(w, r)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Redirect(w, r, "/lists/"+strconv.FormatInt(id, 10)+"/edit", http.StatusSeeOther)
		return
	}
	if err := s.store.Lists.Rename(u.ID, id, name); err != nil {
		log.Error("rename list", "list_id", id, "err", err)
		http.Error(w, "update failed", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/lists/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// listDelete removes a list and returns to the lists index.
func (s *Server) listDelete(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.store.Lists.Delete(u.ID, id); err != nil {
		if err == store.ErrNotFound {
			http.NotFound(w, r)
			return
		}
		log.Error("delete list", "list_id", id, "err", err)
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/lists", http.StatusSeeOther)
}

// listShare creates a public share link for a list and re-renders its share
// control in place.
func (s *Server) listShare(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	token, err := s.store.Lists.SetShare(u.ID, id)
	if err != nil {
		log.Error("share list", "list_id", id, "err", err)
		http.Error(w, "share failed", http.StatusInternalServerError)
		return
	}
	web.Render(w, r, listShareControl(id, token))
}

func (s *Server) listRevoke(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.store.Lists.SetShareToken(u.ID, id, ""); err != nil {
		log.Error("revoke share", "list_id", id, "err", err)
		http.Error(w, "revoke failed", http.StatusInternalServerError)
		return
	}
	web.Render(w, r, listShareControl(id, ""))
}

// favoritesShare creates a public share link for the special favorites list.
func (s *Server) favoritesShare(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	token, err := s.store.Users.ShareFavorites(u.ID)
	if err != nil {
		log.Error("share favorites", "err", err)
		http.Error(w, "share failed", http.StatusInternalServerError)
		return
	}
	web.Render(w, r, favoritesShareControl(token))
}

func (s *Server) favoritesRevoke(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	if err := s.store.Users.SetFavoritesShareToken(u.ID, ""); err != nil {
		log.Error("revoke favorites share", "err", err)
		http.Error(w, "revoke failed", http.StatusInternalServerError)
		return
	}
	web.Render(w, r, favoritesShareControl(""))
}

// itemLists renders the add-to-list picker for a single item, as a fragment
// injected into the shared dialog. The card/modal "add to list" menu entries
// fetch this before showing the dialog.
func (s *Server) itemLists(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	it, err := s.store.Items.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	rows, _ := s.store.Lists.List(u.ID)
	ids, _ := s.store.Lists.ItemListIDs(u.ID, id)
	web.Render(w, r, itemListsDialogInner(id, it.Favorite, listsOnly(rows), ids))
}

// itemListsUpdate applies the add-to-list picker's selections to an item:
// checked lists gain the item, unchecked ones drop it, and the special
// favorites checkbox flips items.favorite. It re-renders the picker dialog so
// membership state stays fresh.
func (s *Server) itemListsUpdate(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	it, err := s.store.Items.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		writeFormError(w, r, "item-lists-error", "could not update lists")
		return
	}
	want := map[string]bool{}
	for _, v := range r.Form["lists"] {
		want[v] = true
	}
	if want["favorites"] != it.Favorite {
		if err := s.store.Items.SetFavorite(u.ID, id, want["favorites"]); err != nil {
			log.Error("set favorite", "item_id", id, "err", err)
			writeFormError(w, r, "item-lists-error", "could not update lists")
			return
		}
	}
	rows, err := s.store.Lists.List(u.ID)
	if err != nil {
		internalError(w, "could not update lists", err)
		return
	}
	current, err := s.store.Lists.ItemListIDs(u.ID, id)
	if err != nil {
		internalError(w, "could not update lists", err)
		return
	}
	currentSet := map[int64]bool{}
	for _, l := range current {
		currentSet[l] = true
	}
	for _, row := range rows {
		inList := currentSet[row.ID]
		requested := want[strconv.FormatInt(row.ID, 10)]
		switch {
		case requested && !inList:
			if err := s.store.Lists.AddItem(u.ID, row.ID, id); err != nil {
				log.Error("add item to list", "item_id", id, "list_id", row.ID, "err", err)
				writeFormError(w, r, "item-lists-error", "could not update lists")
				return
			}
		case !requested && inList:
			if err := s.store.Lists.RemoveItem(u.ID, row.ID, id); err != nil {
				log.Error("remove item from list", "item_id", id, "list_id", row.ID, "err", err)
				writeFormError(w, r, "item-lists-error", "could not update lists")
				return
			}
		}
	}
	freshIDs, _ := s.store.Lists.ItemListIDs(u.ID, id)
	it, _ = s.store.Items.ByID(u.ID, id)
	// The form's swap target is its own container (#item-lists-dialog), so the
	// response detaches the form before htmx dispatches htmx:afterRequest on it
	// (removing any inline close handler first). Signal the close from the
	// server with HX-Trigger instead; app.js listens on body.
	w.Header().Set("HX-Trigger", "item-lists-saved")
	web.Render(w, r, itemListsDialogInner(id, it.Favorite, listsOnly(rows), freshIDs))
}

// listsOnly strips the item-count wrapper off a List list.
func listsOnly(rows []store.ListWithCount) []store.List {
	out := make([]store.List, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.List)
	}
	return out
}

// sharedListPage serves a shared list publicly by its token. It is
// intentionally unauthenticated and links only to external content.
func (s *Server) sharedListPage(w http.ResponseWriter, r *http.Request) {
	l, err := s.store.Lists.ByToken(r.PathValue("token"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	items, more, err := s.store.Lists.ItemListPublic(l.ID, cursorID(r, false), pageSize)
	if err != nil {
		log.Error("shared list items", "list_id", l.ID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	d := sharedListData{Path: "/l/" + r.PathValue("token"), Name: l.Name, Items: items}
	if more && len(items) > 0 {
		d.More = items[len(items)-1].ID
	}
	web.Render(w, r, sharedListPage(d))
}

// sharedFavoritesPage serves a user's favorites list publicly by its share
// token, reusing the same read-only layout as shared lists.
func (s *Server) sharedFavoritesPage(w http.ResponseWriter, r *http.Request) {
	u, err := s.store.Users.ByFavoritesShareToken(r.PathValue("token"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	items, more, err := s.store.Items.ListPage(u.ID, store.ItemFilter{
		FavoritesOnly: true, BeforeID: cursorID(r, false), Limit: pageSize,
	})
	if err != nil {
		log.Error("shared favorites items", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	d := sharedListData{Path: "/f/" + r.PathValue("token"), Name: "favorites", Items: items}
	if more && len(items) > 0 {
		d.More = items[len(items)-1].ID
	}
	web.Render(w, r, sharedListPage(d))
}
