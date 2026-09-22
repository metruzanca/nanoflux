package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/log"

	"github.com/metruzanca/nanoflux/internal/auth"
	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/store"
	"github.com/metruzanca/nanoflux/internal/web"
)

const maxIconBytes = 1 << 20
const maxAvatarBytes = 5 << 20

// defaultAccent matches the built-in --accent in internal/web/static/app.css.
const defaultAccent = "#5b8cff"

// accentRe validates a normalized lowercase "#rrggbb" hex color.
var accentRe = regexp.MustCompile(`^#[0-9a-f]{6}$`)

// accentPresets are quick choices shown as swatches in the accent color card.
// The first entry matches the built-in default.
var accentPresets = []string{
	"#5b8cff",
	"#3ddc84",
	"#9b6bff",
	"#ff9f43",
	"#ff6b9d",
	"#ff6b6b",
	"#2dd4bf",
	"#f5c542",
}

type settingsData struct {
	settingsAvatarData
	Home      settingsHomeData
	Timezone  settingsTimezoneData
	Theme     settingsThemeData
	Accent    settingsAccentData
	Password  settingsPasswordData
	Sessions  settingsSessionsData
	Opml      settingsOpmlData
	Extension settingsExtensionData
	Icons     []settingsIconRow
	Mappings  []settingsMappingRow
}

// settingsExtensionData drives the browser-extension download card.
type settingsExtensionData struct {
	ServerURL string // this server's origin, shown in the setup instructions
}

type settingsPasswordData struct {
	Error   string
	Success string
}

type settingsSessionsData struct {
	Rows         []settingsSessionRow
	Timezone     string
	CurrentToken string
}

type settingsSessionRow struct {
	Token     string
	IsCurrent bool
	CreatedAt string
}

type settingsAvatarData struct {
	HasAvatar bool
	Error     string
}

type settingsTimezoneData struct {
	Timezone string
	Error    string
}

type settingsThemeData struct {
	Theme string
}

type settingsAccentData struct {
	Accent string
	Error  string
}

type settingsIconRow struct {
	store.SourceIcon
	Flash    string
	Timezone string
}

func (s *Server) settingsPage(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	web.Render(w, r, basePage("settings", u, settingsPage(u, settingsData{
		settingsAvatarData: settingsAvatarData{HasAvatar: u.HasAvatar},
		Home:               s.settingsHomeData(u.ID, ""),
		Timezone:           settingsTimezoneData{Timezone: u.Timezone},
		Theme:              settingsThemeData{Theme: u.Theme},
		Accent:             settingsAccentData{Accent: u.AccentColor},
		Sessions:           s.settingsSessionsData(u, auth.Token(r)),
		Extension:          settingsExtensionData{ServerURL: requestBaseURL(r)},
		Icons:              s.settingsIconRows(u.ID, u.Timezone),
		Mappings:           s.settingsMappingRows(u.ID),
	})))
}

// requestBaseURL returns the origin (scheme + host) the request was served on,
// for instructions that reference this server (e.g. the extension setup).
func requestBaseURL(r *http.Request) string {
	scheme := "http"
	if auth.SecureRequest(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// settingsPassword changes the user's password, logging out every other
// session.
func (s *Server) settingsPassword(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	render := func(d settingsPasswordData) {
		if d.Error != "" {
			w.WriteHeader(http.StatusBadRequest)
		}
		web.Render(w, r, settingsPassword(d))
	}
	current := r.FormValue("current_password")
	newpw := r.FormValue("new_password")
	if !auth.CheckPassword(u.PasswordHash, current) {
		render(settingsPasswordData{Error: "current password is incorrect"})
		return
	}
	if len(newpw) < 8 {
		render(settingsPasswordData{Error: "new password must be at least 8 characters"})
		return
	}
	if newpw != r.FormValue("confirm_password") {
		render(settingsPasswordData{Error: "new passwords do not match"})
		return
	}
	hash, err := auth.HashPassword(newpw)
	if err != nil {
		log.Error("change password: hash", "err", err)
		render(settingsPasswordData{Error: "could not change password"})
		return
	}
	if err := s.store.Users.ResetPassword(u.ID, hash); err != nil {
		log.Error("change password: reset", "user_id", u.ID, "err", err)
		render(settingsPasswordData{Error: "could not change password"})
		return
	}
	if err := s.store.Sessions.DeleteUserSessionsExcept(u.ID, auth.Token(r)); err != nil {
		log.Error("change password: revoke sessions", "user_id", u.ID, "err", err)
	}
	render(settingsPasswordData{Success: "password changed — other devices have been logged out"})
}

// settingsSessionsRevoke deletes a session. Revoking the current session logs
// the user out; revoking another just re-renders the list.
func (s *Server) settingsSessionsRevoke(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	token := r.PathValue("token")
	if token == "" {
		http.NotFound(w, r)
		return
	}
	if err := s.store.Sessions.Delete(token); err != nil {
		log.Error("revoke session", "err", err)
		http.NotFound(w, r)
		return
	}
	if token == auth.Token(r) {
		s.auth.ClearCookie(w, r)
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	web.Render(w, r, settingsSessions(s.settingsSessionsData(u, auth.Token(r))))
}

func (s *Server) settingsSessionsData(u store.User, currentToken string) settingsSessionsData {
	sessions, err := s.store.Sessions.ListUserSessions(u.ID)
	if err != nil {
		log.Error("list sessions", "user_id", u.ID, "err", err)
		return settingsSessionsData{}
	}
	rows := make([]settingsSessionRow, 0, len(sessions))
	for _, se := range sessions {
		rows = append(rows, settingsSessionRow{
			Token:     se.Token,
			IsCurrent: se.Token == currentToken,
			CreatedAt: se.CreatedAt,
		})
	}
	return settingsSessionsData{Rows: rows, Timezone: u.Timezone, CurrentToken: currentToken}
}

// settingsTheme stores the user's theme preference: dark, light, or system.
func (s *Server) settingsTheme(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	theme := strings.TrimSpace(r.FormValue("theme"))
	switch theme {
	case "", "dark", "light", "system":
	default:
		w.WriteHeader(http.StatusBadRequest)
		web.Render(w, r, settingsTheme(settingsThemeData{Theme: u.Theme}))
		return
	}
	if theme == "" {
		theme = "dark"
	}
	if err := s.store.Users.SetTheme(u.ID, theme); err != nil {
		log.Error("set theme", "err", err)
		w.WriteHeader(http.StatusBadRequest)
		web.Render(w, r, settingsTheme(settingsThemeData{Theme: u.Theme}))
		return
	}
	web.Render(w, r, settingsTheme(settingsThemeData{Theme: theme}))
}

// settingsAccent stores the user's accent color as a "#rrggbb" hex value.
func (s *Server) settingsAccent(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	accent := strings.ToLower(strings.TrimSpace(r.FormValue("accent")))
	if accent == "" {
		accent = defaultAccent
	}
	if !accentRe.MatchString(accent) {
		w.WriteHeader(http.StatusBadRequest)
		web.Render(w, r, settingsAccent(settingsAccentData{Accent: u.AccentColor, Error: "enter a hex color like #5b8cff"}))
		return
	}
	if err := s.store.Users.SetAccentColor(u.ID, accent); err != nil {
		log.Error("set accent color", "err", err)
		w.WriteHeader(http.StatusBadRequest)
		web.Render(w, r, settingsAccent(settingsAccentData{Accent: u.AccentColor, Error: "could not save accent color"}))
		return
	}
	web.Render(w, r, settingsAccent(settingsAccentData{Accent: accent}))
}

// settingsTimezone stores the user's IANA timezone for relative timestamps.
func (s *Server) settingsTimezone(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	tz := strings.TrimSpace(r.FormValue("timezone"))
	render := func(errMsg string) {
		if errMsg != "" {
			w.WriteHeader(http.StatusBadRequest)
		}
		web.Render(w, r, settingsTimezone(settingsTimezoneData{Timezone: tz, Error: errMsg}))
	}
	if tz != "" {
		if _, err := time.LoadLocation(tz); err != nil {
			render("unknown timezone")
			return
		}
	}
	if err := s.store.Users.SetTimezone(u.ID, tz); err != nil {
		log.Error("set timezone", "err", err)
		render("could not save timezone")
		return
	}
	render("")
}

// settingsAvatar stores an uploaded profile picture.
func (s *Server) settingsAvatar(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	render := func(hasAvatar bool, errMsg string) {
		if errMsg != "" {
			w.WriteHeader(http.StatusBadRequest)
		}
		web.Render(w, r, settingsAvatar(settingsAvatarData{HasAvatar: hasAvatar, Error: errMsg}))
	}

	file, _, err := r.FormFile("avatar")
	if err != nil {
		render(u.HasAvatar, "choose a picture to upload")
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxAvatarBytes+1))
	if err != nil {
		render(u.HasAvatar, "could not read the picture")
		return
	}
	if len(data) > maxAvatarBytes {
		render(u.HasAvatar, "that picture is too large")
		return
	}
	ct := http.DetectContentType(data)
	if !strings.HasPrefix(ct, "image/") {
		render(u.HasAvatar, "that file is not an image")
		return
	}
	key := "avatars/" + strconv.FormatInt(u.ID, 10)
	if err := s.files.Put(r.Context(), key, ct, data); err != nil {
		log.Error("store avatar", "err", err)
		render(u.HasAvatar, "could not save avatar")
		return
	}
	if err := s.store.Users.SetAvatarKey(u.ID, key); err != nil {
		log.Error("set avatar", "err", err)
		render(u.HasAvatar, "could not save avatar")
		return
	}
	render(true, "")
}

// avatarImage serves the current user's stored profile picture.
func (s *Server) avatarImage(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	key, err := s.store.Users.AvatarKey(u.ID)
	if err != nil || key == "" {
		http.NotFound(w, r)
		return
	}
	ct, data, err := s.files.Get(r.Context(), key)
	if err != nil {
		log.Error("load avatar", "key", key, "err", err)
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Write(data)
}

// settingsIconAdd registers a domain->icon mapping and caches the icon.
func (s *Server) settingsIconAdd(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	domain := normalizeDomain(r.FormValue("domain"))
	iconURL := strings.TrimSpace(r.FormValue("icon_url"))

	if domain == "" {
		writeFormError(w, r, "settings-icons-error", "enter a valid domain")
		return
	}
	if iconURL == "" {
		writeFormError(w, r, "settings-icons-error", "enter an icon url")
		return
	}
	if parsed, err := url.Parse(iconURL); err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		writeFormError(w, r, "settings-icons-error", "enter a valid image url")
		return
	}

	ic, err := s.store.SourceIcons.Create(u.ID, domain, iconURL)
	if errors.Is(err, store.ErrExists) {
		writeFormError(w, r, "settings-icons-error", "an icon for that domain already exists")
		return
	}
	if err != nil {
		log.Error("create source icon", "err", err)
		writeFormError(w, r, "settings-icons-error", "could not add icon")
		return
	}
	// Cache it now; failure is non-fatal (row renders with a "not cached"
	// note and the user can refresh).
	if err := s.fetchAndCacheIcon(r.Context(), ic); err != nil {
		log.Error("cache source icon", "domain", domain, "err", err)
	}
	ic, _ = s.store.SourceIcons.ByID(u.ID, ic.ID)
	web.Render(w, r, SettingsIconRow(settingsIconRow{SourceIcon: ic, Timezone: u.Timezone}))
}

// settingsIconRefresh re-fetches and re-caches an icon.
func (s *Server) settingsIconRefresh(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ic, err := s.store.SourceIcons.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.fetchAndCacheIcon(r.Context(), ic); err != nil {
		log.Error("refresh source icon", "domain", ic.Domain, "err", err)
		w.WriteHeader(http.StatusBadRequest)
		web.Render(w, r, SettingsIconRow(settingsIconRow{SourceIcon: ic, Flash: "could not refresh icon", Timezone: u.Timezone}))
		return
	}
	ic, _ = s.store.SourceIcons.ByID(u.ID, id)
	web.Render(w, r, SettingsIconRow(settingsIconRow{SourceIcon: ic, Timezone: u.Timezone}))
}

// settingsIconDelete removes a domain->icon mapping and its stored object.
func (s *Server) settingsIconDelete(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ic, err := s.store.SourceIcons.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.store.SourceIcons.Delete(u.ID, id); err != nil {
		log.Error("delete source icon", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if ic.IconKey != "" {
		if err := s.files.Delete(r.Context(), ic.IconKey); err != nil {
			log.Error("delete source icon object", "key", ic.IconKey, "err", err)
		}
	}
	s.renderSettingsIconList(w, r, u.ID, u.Timezone)
}

func (s *Server) settingsIconRows(userID int64, tz string) []settingsIconRow {
	icons, _ := s.store.SourceIcons.List(userID)
	rows := make([]settingsIconRow, 0, len(icons))
	for _, ic := range icons {
		rows = append(rows, settingsIconRow{SourceIcon: ic, Timezone: tz})
	}
	return rows
}

func (s *Server) renderSettingsIconList(w http.ResponseWriter, r *http.Request, userID int64, tz string) {
	web.Render(w, r, SettingsIconsList(s.settingsIconRows(userID, tz)))
}

// serveSourceIcon resolves a feed's icon for the current user: their cached
// custom icon for the domain, else the built-in X/YouTube/globe icon.
func (s *Server) serveSourceIcon(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	domain := strings.ToLower(r.PathValue("domain"))
	if !validDomain(domain) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
	if ic, err := s.store.SourceIcons.ByDomain(u.ID, domain); err == nil && ic.IconKey != "" {
		ct, data, err := s.files.Get(r.Context(), ic.IconKey)
		if err == nil {
			w.Header().Set("Content-Type", ct)
			w.Write(data)
			return
		}
		log.Error("load source icon", "key", ic.IconKey, "err", err)
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Write([]byte(builtinIcon(domain)))
}

// autoCacheFeedIcon fetches and caches the favicon for a new feed's home
// domain, so the /icons/{domain} handler serves a real brand icon instead of
// the globe. It only runs when a home url was supplied (the add-feed form
// fills it from page metadata). Failures are non-fatal: the built-in globe is
// shown and the user can add a custom icon in settings.
func (s *Server) autoCacheFeedIcon(ctx context.Context, userID int64, homeURL string) {
	domain := normalizeDomain(homeURL)
	if domain == "" {
		return
	}
	// Keep the built-in X/YouTube brand icons; a favicon adds nothing there.
	if builtinIcon(domain) != builtinIcons["globe"] {
		return
	}
	if _, err := s.store.SourceIcons.ByDomain(userID, domain); err == nil {
		return // user already configured a custom icon for this domain
	}
	meta, err := s.discoverer.PageMeta(ctx, homeURL)
	if err != nil || meta.IconURL == "" {
		return
	}
	ic, err := s.store.SourceIcons.Create(userID, domain, meta.IconURL)
	if err != nil {
		return
	}
	if err := s.fetchAndCacheIcon(ctx, ic); err != nil {
		log.Error("cache auto feed icon", "domain", domain, "err", err)
	}
}

// fetchAndCacheIcon downloads the icon's bytes and stores them in object
// storage, recording the key.
func (s *Server) fetchAndCacheIcon(ctx context.Context, ic store.SourceIcon) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ic.IconURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "nanoflux/0.1")

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "image/") {
		return errors.New("not an image")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxIconBytes))
	if err != nil {
		return err
	}
	key := "icons/" + strconv.FormatInt(ic.UserID, 10) + "/" + ic.Domain
	if err := s.files.Put(ctx, key, ct, data); err != nil {
		return err
	}
	return s.store.SourceIcons.SetIconKey(ic.UserID, ic.ID, key, db.Now())
}

var domainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)

func validDomain(d string) bool {
	return len(d) <= 253 && domainRe.MatchString(d)
}

// normalizeDomain extracts a lowercase hostname from a bare domain or URL.
func normalizeDomain(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// A bare hostname won't parse with a scheme; try both forms.
	for _, candidate := range []string{s, normalizeURL(s)} {
		if u, err := url.Parse(candidate); err == nil && u.Hostname() != "" {
			d := strings.ToLower(u.Hostname())
			if validDomain(d) {
				return d
			}
		}
	}
	return ""
}

const iconColor = "#9aa3b2"

var builtinIcons = map[string]string{
	"globe":     `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="` + iconColor + `" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><line x1="2" y1="12" x2="22" y2="12"/><path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"/></svg>`,
	"x":         `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="16" height="16" fill="` + iconColor + `"><path d="M18.244 2.25h3.308l-7.227 8.26 8.502 11.24H16.17l-5.214-6.817L4.99 21.75H1.68l7.73-8.835L1.254 2.25H8.08l4.713 6.231zm-1.161 17.52h1.833L7.084 4.126H5.117z"/></svg>`,
	"youtube":   `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="16" height="16" fill="` + iconColor + `"><path d="M23.498 6.186a3.016 3.016 0 0 0-2.122-2.136C19.505 3.545 12 3.545 12 3.545s-7.505 0-9.377.505A3.017 3.017 0 0 0 .502 6.186C0 8.07 0 12 0 12s0 3.93.502 5.814a3.016 3.016 0 0 0 2.122 2.136c1.871.505 9.376.505 9.376.505s7.505 0 9.377-.505a3.015 3.015 0 0 0 2.122-2.136C24 15.93 24 12 24 12s0-3.93-.502-5.814zM9.545 15.568V8.432L15.818 12l-6.273 3.568z"/></svg>`,
	"instagram": `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="` + iconColor + `" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="2" y="2" width="20" height="20" rx="5" ry="5"/><path d="M16 11.37A4 4 0 1 1 12.63 8 4 4 0 0 1 16 11.37z"/><line x1="17.5" y1="6.5" x2="17.51" y2="6.5"/></svg>`,
}

func builtinIcon(domain string) string {
	switch {
	case strings.HasSuffix(domain, "youtube.com"), strings.HasSuffix(domain, "youtu.be"):
		return builtinIcons["youtube"]
	case domain == "x.com" || domain == "www.x.com" || domain == "twitter.com" || domain == "www.twitter.com":
		return builtinIcons["x"]
	case domain == "instagram.com" || domain == "www.instagram.com" || domain == "m.instagram.com":
		return builtinIcons["instagram"]
	default:
		return builtinIcons["globe"]
	}
}
