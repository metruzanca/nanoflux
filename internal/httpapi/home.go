package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/charmbracelet/log"

	"github.com/metruzanca/nanoflux/internal/auth"
	"github.com/metruzanca/nanoflux/internal/store"
	"github.com/metruzanca/nanoflux/internal/web"
)

// homeSectionLimit caps how many items a single pinned section shows before
// the "more" link.
const homeSectionLimit = 8

// homeSection is one pinned section of the home dashboard, stored as JSON in
// users.home_config. The first cut only renders collections, but the Kind field
// leaves room for more source kinds (unread, favorites, list, author, feed).
type homeSection struct {
	Kind  string `json:"kind"`
	RefID int64  `json:"ref_id"`
}

// parseHomeConfig decodes the user's JSON config, dropping anything malformed
// so a corrupt value degrades to the default home instead of erroring.
func parseHomeConfig(raw string) []homeSection {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var sections []homeSection
	if err := json.Unmarshal([]byte(raw), &sections); err != nil {
		log.Error("parse home config", "err", err)
		return nil
	}
	out := sections[:0]
	for _, s := range sections {
		if s.Kind == "collection" && s.RefID != 0 {
			out = append(out, s)
		}
	}
	return out
}

func marshalHomeConfig(sections []homeSection) string {
	if len(sections) == 0 {
		return ""
	}
	b, err := json.Marshal(sections)
	if err != nil {
		return ""
	}
	return string(b)
}

// homeSectionData is one rendered dashboard section: the collection plus its
// unread items (never empty — empty sections are dropped).
type homeSectionData struct {
	Collection store.Collection
	Items      []store.ItemWithFeed
}

type dashboardData struct {
	Sections []homeSectionData
}

// home is the landing page: the user's pinned collection sections, each showing
// recent unread items. With no config (or when every pinned section is empty) it
// falls back to the plain unread list, so behavior is unchanged until the user
// customizes their home.
func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	sections := parseHomeConfig(u.HomeConfig)

	var rendered []homeSectionData
	for _, sec := range sections {
		c, err := s.store.Collections.ByID(u.ID, sec.RefID)
		if err != nil {
			continue // collection deleted since it was pinned
		}
		items, _, err := s.store.Items.ListPage(u.ID, store.ItemFilter{
			CollectionID: c.ID, UnreadOnly: true, Limit: homeSectionLimit,
		})
		if err != nil {
			log.Error("home section", "collection_id", c.ID, "err", err)
			continue
		}
		if len(items) == 0 {
			continue // hide sections with no unread
		}
		rendered = append(rendered, homeSectionData{Collection: c, Items: withTZ(u.Timezone, items)})
	}

	if len(rendered) == 0 {
		// No config, or everything pinned is quiet: show the unread list.
		s.renderUnread(w, r)
		return
	}
	web.Render(w, r, basePage("home", u, dashboardPage(dashboardData{Sections: rendered})))
}

// unread is the canonical unread page (moved off "/" now that home is a
// dashboard).
func (s *Server) unread(w http.ResponseWriter, r *http.Request) {
	s.renderUnread(w, r)
}

// renderUnread renders the full unread list used by both "/" (fallback) and
// "/unread".
func (s *Server) renderUnread(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	asc := itemsAsc(r)
	items, more, _ := s.store.Items.ListPage(u.ID, store.ItemFilter{UnreadOnly: true, Ascending: asc, Limit: pageSize})
	unread, _ := s.store.Items.CountUnread(u.ID, 0)
	web.Render(w, r, basePage("unread", u, homePage(homeData{
		Unread: withTZ(u.Timezone, items), UnreadCount: unread, Dir: dirParam(asc),
		More: pageCursor("/items?dir="+dirParam(asc), items, more, asc),
	})))
}

// settingsHomeRow is one pinned collection in the settings card.
type settingsHomeRow struct {
	Collection store.Collection
}

// settingsHomeData is the home-screen settings card's data.
type settingsHomeData struct {
	Rows        []settingsHomeRow
	Collections []store.Collection // the user's non-auto collections, for the add picker
	Error       string
}

func (s *Server) settingsHomeData(userID int64, errMsg string) settingsHomeData {
	u, err := s.store.Users.ByID(userID)
	if err != nil {
		return settingsHomeData{Error: errMsg}
	}
	all, _ := s.store.Collections.List(userID)
	byID := make(map[int64]store.Collection, len(all))
	var selectable []store.Collection
	for _, c := range all {
		byID[c.ID] = c
		if !c.IsAuto {
			selectable = append(selectable, c)
		}
	}
	var rows []settingsHomeRow
	for _, sec := range parseHomeConfig(u.HomeConfig) {
		if c, ok := byID[sec.RefID]; ok {
			rows = append(rows, settingsHomeRow{Collection: c})
		}
	}
	return settingsHomeData{Rows: rows, Collections: selectable, Error: errMsg}
}

// settingsHome applies one home-screen card action (add / up / down / remove)
// and re-renders the card. Ordering is handled server-side so no client JS is
// needed to move rows.
func (s *Server) settingsHome(w http.ResponseWriter, r *http.Request) {
	current, _ := auth.UserFrom(r)
	u, err := s.store.Users.ByID(current.ID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	sections := parseHomeConfig(u.HomeConfig)

	action := r.FormValue("action")
	id, _ := strconv.ParseInt(r.FormValue("collection_id"), 10, 64)

	// Verify ownership of the collection being acted on.
	owned := func(colID int64) bool {
		if colID == 0 {
			return false
		}
		_, err := s.store.Collections.ByID(u.ID, colID)
		return err == nil
	}

	switch action {
	case "add":
		addID, _ := strconv.ParseInt(r.FormValue("add_collection"), 10, 64)
		if owned(addID) && !homeHasSection(sections, addID) {
			sections = append(sections, homeSection{Kind: "collection", RefID: addID})
		}
	case "up":
		sections = moveSection(sections, id, -1)
	case "down":
		sections = moveSection(sections, id, +1)
	case "remove":
		sections = removeSection(sections, id)
	}

	if err := s.store.Users.SetHomeConfig(u.ID, marshalHomeConfig(sections)); err != nil {
		log.Error("set home config", "err", err)
		renderSettingsHome(w, r, s.settingsHomeData(u.ID, "could not save home screen"))
		return
	}
	web.Render(w, r, settingsHomeCard(s.settingsHomeData(u.ID, "")))
}

// settingsHomeReset clears the home config, restoring the default unread home.
func (s *Server) settingsHomeReset(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	if err := s.store.Users.SetHomeConfig(u.ID, ""); err != nil {
		log.Error("reset home config", "err", err)
		renderSettingsHome(w, r, s.settingsHomeData(u.ID, "could not reset home screen"))
		return
	}
	web.Render(w, r, settingsHomeCard(s.settingsHomeData(u.ID, "")))
}

func renderSettingsHome(w http.ResponseWriter, r *http.Request, d settingsHomeData) {
	w.WriteHeader(http.StatusBadRequest)
	web.Render(w, r, settingsHomeCard(d))
}

func homeHasSection(sections []homeSection, id int64) bool {
	for _, s := range sections {
		if s.RefID == id {
			return true
		}
	}
	return false
}

// moveSection swaps a section with its neighbor (delta -1 up, +1 down). A no-op
// at the ends.
func moveSection(sections []homeSection, id int64, delta int) []homeSection {
	i := -1
	for j, s := range sections {
		if s.RefID == id {
			i = j
			break
		}
	}
	k := i + delta
	if i < 0 || k < 0 || k >= len(sections) {
		return sections
	}
	sections[i], sections[k] = sections[k], sections[i]
	return sections
}

func removeSection(sections []homeSection, id int64) []homeSection {
	out := sections[:0]
	for _, s := range sections {
		if s.RefID != id {
			out = append(out, s)
		}
	}
	return out
}
