package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/charmbracelet/log"

	"github.com/metruzanca/nanoflux/internal/auth"
	"github.com/metruzanca/nanoflux/internal/store"
	"github.com/metruzanca/nanoflux/internal/urlmap"
	"github.com/metruzanca/nanoflux/internal/web"
)

// settingsMappingRow is one mapping in the settings list.
type settingsMappingRow struct {
	store.UrlMapping
}

func (s *Server) settingsMappingRows(userID int64) []settingsMappingRow {
	mappings, _ := s.store.UrlMappings.List(userID)
	rows := make([]settingsMappingRow, 0, len(mappings))
	for _, m := range mappings {
		rows = append(rows, settingsMappingRow{UrlMapping: m})
	}
	return rows
}

func (s *Server) renderSettingsMappingsList(w http.ResponseWriter, r *http.Request, userID int64) {
	web.Render(w, r, SettingsMappingsList(s.settingsMappingRows(userID)))
}

// mappedFeedURL applies the user's mappings to input, returning the mapped
// feed URL. The first matching mapping (by id, oldest first) wins. Mappings
// are only consulted when adding a feed; existing feeds keep the feed url
// they were created with.
func (s *Server) mappedFeedURL(userID int64, input string) (string, bool) {
	mappings, err := s.store.UrlMappings.List(userID)
	if err != nil || len(mappings) == 0 {
		return "", false
	}
	for _, m := range mappings {
		compiled, err := urlmap.Compile(m.Pattern, m.Template)
		if err != nil {
			// Stored mappings are validated on save; skip anything that no
			// longer compiles rather than failing the add.
			log.Error("compile url mapping", "id", m.ID, "err", err)
			continue
		}
		if out, ok := compiled.Apply(input); ok {
			return out, true
		}
	}
	return "", false
}

// settingsMappingAdd registers a pattern -> feed url mapping.
func (s *Server) settingsMappingAdd(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	pattern := strings.TrimSpace(r.FormValue("pattern"))
	template := strings.TrimSpace(r.FormValue("template"))
	if _, err := urlmap.Compile(pattern, template); err != nil {
		writeFormError(w, r, "settings-mappings-error", err.Error())
		return
	}
	m, err := s.store.UrlMappings.Create(u.ID, pattern, template)
	if errors.Is(err, store.ErrExists) {
		writeFormError(w, r, "settings-mappings-error", "a mapping for that pattern already exists")
		return
	}
	if err != nil {
		log.Error("create url mapping", "err", err)
		writeFormError(w, r, "settings-mappings-error", "could not add mapping")
		return
	}
	web.Render(w, r, SettingsMappingRow(settingsMappingRow{UrlMapping: m}))
}

// settingsMappingUpdate edits a mapping in place. Existing feeds keep the
// feed url they were created with — the change only affects future adds.
func (s *Server) settingsMappingUpdate(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	pattern := strings.TrimSpace(r.FormValue("pattern"))
	template := strings.TrimSpace(r.FormValue("template"))
	if _, err := urlmap.Compile(pattern, template); err != nil {
		writeFormError(w, r, "settings-mapping-error-"+strconv.FormatInt(id, 10), err.Error())
		return
	}
	err = s.store.UrlMappings.Update(u.ID, id, pattern, template)
	if errors.Is(err, store.ErrExists) {
		writeFormError(w, r, "settings-mapping-error-"+strconv.FormatInt(id, 10), "a mapping for that pattern already exists")
		return
	}
	if err != nil {
		log.Error("update url mapping", "id", id, "err", err)
		writeFormError(w, r, "settings-mapping-error-"+strconv.FormatInt(id, 10), "could not save mapping")
		return
	}
	m, err := s.store.UrlMappings.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	web.Render(w, r, SettingsMappingRow(settingsMappingRow{UrlMapping: m}))
}

// settingsMappingDelete removes a mapping.
func (s *Server) settingsMappingDelete(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.store.UrlMappings.Delete(u.ID, id); err != nil {
		log.Error("delete url mapping", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderSettingsMappingsList(w, r, u.ID)
}

// settingsMappingTest applies a not-yet-saved pattern/template to a sample url
// so users can verify a mapping before saving. The response swaps into the
// form's test-result slot.
func (s *Server) settingsMappingTest(w http.ResponseWriter, r *http.Request) {
	pattern := strings.TrimSpace(r.FormValue("pattern"))
	template := strings.TrimSpace(r.FormValue("template"))
	testURL := normalizeURL(r.FormValue("test_url"))
	if testURL == "" {
		renderError(w, r, "enter a url to test")
		return
	}
	m, err := urlmap.Compile(pattern, template)
	if err != nil {
		renderError(w, r, err.Error())
		return
	}
	mapped, ok := m.Apply(testURL)
	web.Render(w, r, MappingTestResult(mapped, ok))
}

// settingsMappingEditFragment swaps a mapping row for its inline edit form.
func (s *Server) settingsMappingEditFragment(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	m, err := s.store.UrlMappings.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	web.Render(w, r, SettingsMappingEditForm(settingsMappingRow{UrlMapping: m}))
}

// settingsMappingRowFragment renders a single mapping row (the cancel path
// out of the inline edit form).
func (s *Server) settingsMappingRowFragment(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	m, err := s.store.UrlMappings.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	web.Render(w, r, SettingsMappingRow(settingsMappingRow{UrlMapping: m}))
}
