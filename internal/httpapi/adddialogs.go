package httpapi

import (
	"net/http"

	"github.com/metruzanca/nanoflux/internal/web"
)

// The add dialogs are shared shells in views_layout.templ, filled from these
// fragments. The add menu and the command palette then open them in place on
// any page instead of navigating to the index that used to own the form.

func (s *Server) addFeedDialogFragment(w http.ResponseWriter, r *http.Request) {
	web.Render(w, r, addFeedDialog())
}

func (s *Server) createAuthorDialogFragment(w http.ResponseWriter, r *http.Request) {
	web.Render(w, r, createAuthorDialog())
}

func (s *Server) addCollectionDialogFragment(w http.ResponseWriter, r *http.Request) {
	web.Render(w, r, addCollectionDialog())
}

func (s *Server) addListDialogFragment(w http.ResponseWriter, r *http.Request) {
	web.Render(w, r, addListDialog())
}
