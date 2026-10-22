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
	"github.com/metruzanca/nanoflux/internal/imageutil"
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

// settingsAlgorithmData summarizes the magic-sort editor for the settings card:
// the feeds in play, the favorites that rank them, and the manual overrides.
type settingsAlgorithmData struct {
	FeedCount     int
	FavoriteCount int
	Raised        int
	Lowered       int
}

func (s *Server) settingsAlgorithmData(userID int64) settingsAlgorithmData {
	rows, err := s.store.Feeds.ListWithFavorites(userID)
	if err != nil {
		log.Error("load algorithm summary", "user_id", userID, "err", err)
		return settingsAlgorithmData{}
	}
	d := settingsAlgorithmData{FeedCount: len(rows)}
	for _, row := range rows {
		d.FavoriteCount += row.FavoriteCount
		switch {
		case row.Rank > 0:
			d.Raised++
		case row.Rank < 0:
			d.Lowered++
		}
	}
	return d
}

type settingsData struct {
	settingsAvatarData
	Home       settingsHomeData
	Algorithm  settingsAlgorithmData
	Timezone   settingsTimezoneData
	Appearance settingsAppearanceData
	AutoRead   settingsAutoReadData
	Password   settingsPasswordData
	Sessions   settingsSessionsData
	Opml       settingsOpmlData
	Extension  settingsExtensionData
}

// autoReadOption is one radio choice in the auto-read card. Value is the form
// value ("3", "7", "30", "60", or "off"); off maps to 0 days (disabled).
type autoReadOption struct {
	Value string
	Label string
}

// autoReadOptions are the supported auto-read windows. 30 is the schema default.
var autoReadOptions = []autoReadOption{
	{"3", "3 days"},
	{"7", "7 days"},
	{"30", "30 days"},
	{"60", "60 days"},
	{"off", "off"},
}

// themeItems are the combo options for the theme setting.
var themeItems = []comboItem{
	{Value: "dark", Label: "dark"},
	{Value: "light", Label: "light"},
	{Value: "system", Label: "system"},
}

// autoReadItems are the combo options for the auto-read window setting.
var autoReadItems = func() []comboItem {
	out := make([]comboItem, 0, len(autoReadOptions))
	for _, o := range autoReadOptions {
		out = append(out, comboItem{Value: o.Value, Label: o.Label})
	}
	return out
}()

type settingsAutoReadData struct {
	Value string // selected option value, matching autoReadOptions
	Error string
}

// autoReadValue maps a stored day count to its option value; 0 is "off".
func autoReadValue(days int) string {
	if days <= 0 {
		return "off"
	}
	return strconv.Itoa(days)
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
	Username  string // the fallback initial, shown when there is no picture
	Error     string
}

type settingsTimezoneData struct {
	Timezone string
	Items    []comboItem // resolvable IANA zones for the picker
	Error    string
}

// timezoneItems is the timezone picker's option list: timezoneNames filtered to
// the zones this build can actually resolve (tzdata present), with a leading
// "server time" choice. Filtering keeps the picker from offering a zone the
// handler's time.LoadLocation would reject.
func timezoneItems() []comboItem {
	items := []comboItem{{Value: "", Label: "server time"}}
	for _, name := range timezoneNames {
		if _, err := time.LoadLocation(name); err != nil {
			continue
		}
		items = append(items, comboItem{Value: name, Label: name})
	}
	return items
}

type settingsThemeData struct {
	Theme string
}

type settingsAccentData struct {
	Accent string
	Error  string
}

// settingsAppearanceData drives the appearance card: theme and accent (each
// with its own htmx sub-target so a change only re-renders that control) plus
// the unread-visibility toggles and the grid column slider.
type settingsAppearanceData struct {
	Theme            string
	Accent           string
	AccentError      string
	HideUnreadCounts bool
	HideUnreadNav    bool
	GridMaxColumns   int
	GridError        string
}

func (s *Server) settingsPage(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	web.Render(w, r, basePage("settings", u, settingsPage(u, settingsData{
		settingsAvatarData: settingsAvatarData{HasAvatar: u.HasAvatar, Username: u.Username},
		Home:               s.settingsHomeData(u.ID, ""),
		Algorithm:          s.settingsAlgorithmData(u.ID),
		Timezone:           settingsTimezoneData{Timezone: u.Timezone, Items: timezoneItems()},
		Appearance:         settingsAppearanceData{Theme: u.Theme, Accent: u.AccentColor, HideUnreadCounts: u.HideUnreadCounts, HideUnreadNav: u.HideUnreadNav, GridMaxColumns: store.ClampGridColumns(u.GridMaxColumns)},
		AutoRead:           settingsAutoReadData{Value: autoReadValue(u.AutoReadAfterDays)},
		Sessions:           s.settingsSessionsData(u, auth.Token(r)),
		Extension:          settingsExtensionData{ServerURL: requestBaseURL(r)},
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
		web.Render(w, r, settingsPasswordCard(d))
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
	if err := s.store.Sessions.DeleteByHash(token); err != nil {
		log.Error("revoke session", "err", err)
		http.NotFound(w, r)
		return
	}
	if token == store.HashSessionToken(auth.Token(r)) {
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
	currentHash := store.HashSessionToken(currentToken)
	for _, se := range sessions {
		rows = append(rows, settingsSessionRow{
			Token:     se.Token,
			IsCurrent: se.Token == currentHash,
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
		web.Render(w, r, appearanceTheme(settingsThemeData{Theme: u.Theme}))
		return
	}
	if theme == "" {
		theme = "dark"
	}
	if err := s.store.Users.SetTheme(u.ID, theme); err != nil {
		log.Error("set theme", "err", err)
		w.WriteHeader(http.StatusBadRequest)
		web.Render(w, r, appearanceTheme(settingsThemeData{Theme: u.Theme}))
		return
	}
	web.Render(w, r, appearanceTheme(settingsThemeData{Theme: theme}))
}

// settingsAutoRead stores the user's auto-read window and, when enabled,
// sweeps immediately so the change is visible without waiting for the daily
// background pass.
func (s *Server) settingsAutoRead(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	value := strings.TrimSpace(r.FormValue("auto_read_after_days"))
	render := func(v string, errMsg string) {
		if errMsg != "" {
			w.WriteHeader(http.StatusBadRequest)
		}
		web.Render(w, r, settingsAutoRead(settingsAutoReadData{Value: v, Error: errMsg}))
	}
	days := 0
	switch value {
	case "off":
		days = 0
	case "3", "7", "30", "60":
		n, err := strconv.Atoi(value)
		if err != nil {
			render(autoReadValue(u.AutoReadAfterDays), "choose an option")
			return
		}
		days = n
	default:
		render(autoReadValue(u.AutoReadAfterDays), "choose an option")
		return
	}
	if err := s.store.Users.SetAutoReadAfterDays(u.ID, days); err != nil {
		log.Error("set auto-read", "err", err)
		render(autoReadValue(u.AutoReadAfterDays), "could not save")
		return
	}
	if days > 0 {
		if _, err := s.store.Items.MarkOlderThanRead(u.ID, days); err != nil {
			log.Error("auto-read sweep on save", "err", err)
		}
	}
	render(autoReadValue(days), "")
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
		web.Render(w, r, appearanceAccent(settingsAccentData{Accent: u.AccentColor, Error: "enter a hex color like #5b8cff"}))
		return
	}
	if err := s.store.Users.SetAccentColor(u.ID, accent); err != nil {
		log.Error("set accent color", "err", err)
		w.WriteHeader(http.StatusBadRequest)
		web.Render(w, r, appearanceAccent(settingsAccentData{Accent: u.AccentColor, Error: "could not save accent color"}))
		return
	}
	web.Render(w, r, appearanceAccent(settingsAccentData{Accent: accent}))
}

// settingsAppearanceUnreadCounts toggles the numeric unread/authors badges in
// the top navigation.
func (s *Server) settingsAppearanceUnreadCounts(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	hide := r.FormValue("hide_unread_counts") == "1"
	if err := s.store.Users.SetHideUnreadCounts(u.ID, hide); err != nil {
		log.Error("set hide unread counts", "err", err)
		w.WriteHeader(http.StatusBadRequest)
		web.Render(w, r, appearanceUnreadCounts(settingsAppearanceData{HideUnreadCounts: u.HideUnreadCounts}))
		return
	}
	web.Render(w, r, appearanceUnreadCounts(settingsAppearanceData{HideUnreadCounts: hide}))
}

// settingsAppearanceUnreadNav toggles whether the "unread" nav item moves from
// the top navigation into the user menu. Because that changes the shared topbar
// markup (not a fragment on this page), the response asks htmx for a full page
// refresh so every rendered navbar is correct.
func (s *Server) settingsAppearanceUnreadNav(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	hide := r.FormValue("hide_unread_nav") == "1"
	if err := s.store.Users.SetHideUnreadNav(u.ID, hide); err != nil {
		log.Error("set hide unread nav", "err", err)
		w.WriteHeader(http.StatusBadRequest)
		web.Render(w, r, appearanceUnreadNav(settingsAppearanceData{HideUnreadNav: u.HideUnreadNav}))
		return
	}
	w.Header().Set("HX-Refresh", "true")
	web.Render(w, r, appearanceUnreadNav(settingsAppearanceData{HideUnreadNav: hide}))
}

// settingsAppearanceGridColumns stores the maximum column count for grid-mode
// item lists. The slider offers 2..6; anything else is rejected.
func (s *Server) settingsAppearanceGridColumns(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	current := store.ClampGridColumns(u.GridMaxColumns)
	raw := strings.TrimSpace(r.FormValue("grid_max_columns"))
	n, err := strconv.Atoi(raw)
	if err != nil || n < 2 || n > 6 {
		w.WriteHeader(http.StatusBadRequest)
		web.Render(w, r, appearanceGridColumns(settingsAppearanceData{GridMaxColumns: current, GridError: "choose between 2 and 6 columns"}))
		return
	}
	if err := s.store.Users.SetGridMaxColumns(u.ID, n); err != nil {
		log.Error("set grid max columns", "err", err)
		w.WriteHeader(http.StatusBadRequest)
		web.Render(w, r, appearanceGridColumns(settingsAppearanceData{GridMaxColumns: current, GridError: "could not save"}))
		return
	}
	web.Render(w, r, appearanceGridColumns(settingsAppearanceData{GridMaxColumns: n}))
}

// settingsTimezone stores the user's IANA timezone for relative timestamps.
func (s *Server) settingsTimezone(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	tz := strings.TrimSpace(r.FormValue("timezone"))
	render := func(errMsg string) {
		if errMsg != "" {
			w.WriteHeader(http.StatusBadRequest)
		}
		web.Render(w, r, settingsTimezone(settingsTimezoneData{Timezone: tz, Items: timezoneItems(), Error: errMsg}))
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
		web.Render(w, r, settingsAvatar(settingsAvatarData{HasAvatar: hasAvatar, Username: u.Username, Error: errMsg}))
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
	setImageHeaders(w)
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Write(data)
}

// serveSourceIcon resolves a feed's icon for the current user: their cached
// auto-fetched icon for the domain, else a plugin-supplied site icon, else the
// generic globe.
func (s *Server) serveSourceIcon(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	domain := strings.ToLower(r.PathValue("domain"))
	if !validDomain(domain) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
	setImageHeaders(w)
	if ic, err := s.store.SourceIcons.ByDomain(u.ID, domain); err == nil && ic.IconKey != "" {
		ct, data, err := s.files.Get(r.Context(), ic.IconKey)
		if err == nil {
			w.Header().Set("Content-Type", ct)
			w.Write(data)
			return
		}
		log.Error("load source icon", "key", ic.IconKey, "err", err)
	}
	ct, data := s.builtinIcon(domain)
	w.Header().Set("Content-Type", ct)
	w.Write(data)
}

// autoCacheFeedIcon fetches and caches the favicon for a new feed's home
// domain, so the /icons/{domain} handler serves a real icon instead of the
// generic fallback. It only runs when a home url was supplied (the add-feed
// form fills it from page metadata). Failures are non-fatal: the fallback is
// shown.
func (s *Server) autoCacheFeedIcon(ctx context.Context, userID int64, homeURL string) {
	domain := normalizeDomain(homeURL)
	if domain == "" {
		return
	}
	// A plugin-supplied site icon is preferred; a favicon adds nothing there.
	if _, _, ok := s.pluginIcon(domain); ok {
		return
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
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxIconBytes))
	if err != nil {
		return err
	}
	ct, ok := imageutil.Sniff(resp.Header.Get("Content-Type"), data)
	if !ok {
		return errors.New("not an image")
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

const globeIcon = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="` + iconColor + `" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><line x1="2" y1="12" x2="22" y2="12"/><path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"/></svg>`

// pluginIcon returns a plugin-supplied site icon for domain, if one is loaded.
func (s *Server) pluginIcon(domain string) (string, []byte, bool) {
	if s.plugins == nil {
		return "", nil, false
	}
	mime, data, ok := s.plugins.Icon("https://" + domain + "/")
	if !ok {
		return "", nil, false
	}
	if mime == "" {
		mime = "image/svg+xml"
	}
	return mime, data, true
}

// builtinIcon returns the icon used when no cached or plugin icon exists.
func (s *Server) builtinIcon(domain string) (string, []byte) {
	if mime, data, ok := s.pluginIcon(domain); ok {
		return mime, data
	}
	return "image/svg+xml", []byte(globeIcon)
}
