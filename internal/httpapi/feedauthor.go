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

// changeAuthorData backs the change-author dialog: the feed being moved and the
// other authors it can move to. The current author is deliberately excluded, so
// the picker never offers a no-op.
type changeAuthorData struct {
	FeedID    int64
	FeedTitle string
	Authors   []comboItem
	Err       string
}

// changeAuthorFragment renders the change-author form into the shared
// #change-author-dialog. Ownership is checked through Feeds.ByID.
func (s *Server) changeAuthorFragment(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := strconv.ParseInt(r.URL.Query().Get("feed"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	f, err := s.store.Feeds.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	web.Render(w, r, changeAuthorForm(s.changeAuthorDialogData(u, f, "")))
}

// changeAuthorDialogData lists every author but the feed's current one. A
// failed save reuses it to render the error inside the dialog.
func (s *Server) changeAuthorDialogData(u store.User, f store.Feed, errMsg string) changeAuthorData {
	authors, _ := s.store.Authors.List(u.ID)
	items := make([]comboItem, 0, len(authors))
	for _, a := range authors {
		if a.ID == f.AuthorID {
			continue
		}
		items = append(items, comboItem{Value: strconv.FormatInt(a.ID, 10), Label: a.Name})
	}
	return changeAuthorData{FeedID: f.ID, FeedTitle: f.Title, Authors: items, Err: errMsg}
}

// feedChangeAuthor moves a feed to another author. It is driven by the
// change-author dialog's htmx form: a successful move returns an empty 200, so
// the outerHTML swap removes the feed's accordion item from the current
// author's edit page, and data-form-close-reset closes the dialog. A validation
// failure re-renders the form into the dialog (HX-Retarget) with a 400, which
// the global beforeSwap listener still swaps and which leaves the dialog open.
func (s *Server) feedChangeAuthor(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	f, err := s.store.Feeds.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	fail := func(msg string) {
		w.Header().Set("HX-Retarget", "#change-author-dialog")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		changeAuthorForm(s.changeAuthorDialogData(u, f, msg)).Render(r.Context(), w)
	}
	newID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("author_id")), 10, 64)
	if err != nil || newID <= 0 {
		fail("choose an author")
		return
	}
	if newID == f.AuthorID {
		fail("this feed already belongs to that author")
		return
	}
	// Authors.ByID is user-scoped and hides the system author, so a foreign or
	// hidden id is rejected here.
	if _, err := s.store.Authors.ByID(u.ID, newID); err != nil {
		fail("choose a valid author")
		return
	}
	if err := s.store.Feeds.SetAuthor(u.ID, f.ID, newID); err != nil {
		log.Error("change feed author", "feed_id", f.ID, "author_id", newID, "err", err)
		fail("could not move the feed")
		return
	}
	// The target author's auto-read window now covers this feed; apply it right
	// away, like the author edit page does for its own save.
	if _, err := s.store.Items.MarkAutoReadForAuthor(u.ID, newID); err != nil {
		log.Error("auto-read sweep on feed move", "feed_id", f.ID, "author_id", newID, "err", err)
	}
	w.WriteHeader(http.StatusOK)
}
