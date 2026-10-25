package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/log"

	"github.com/metruzanca/nanoflux/internal/auth"
	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/storage"
	"github.com/metruzanca/nanoflux/internal/store"
	"github.com/metruzanca/nanoflux/internal/web"
	"github.com/metruzanca/nanoflux/pluginapi"
)

type adminData struct {
	Stats       adminStats
	Poll        adminPoll
	Storage     adminStorage
	AllowSignup bool
	BannerShown bool
	Users       []adminUserRow
	Backup      adminBackup
	Plugins     []adminPluginRow
}

// adminPoll is the polling health summary shown on /admin. "Behind" means a
// feed's last poll is older than its own interval, so it is waiting even if a
// rate-limit deadline currently keeps the poller from touching it. The per-host
// demand/capacity ratio is the permanent-lateness signal: when a host needs more
// fetches per day than its spacing allows, the backlog cannot drain.
type adminPoll struct {
	Enabled     int
	Behind      int
	OldestTitle string
	OldestRef   string // stored UTC time of the oldest behind feed
	Overloaded  int    // hosts whose demand exceeds their capacity
	Hosts       []adminPollHost
	Unavailable bool
}

// adminPollHost is one host's polling load. Demand and Capacity are whole
// fetches per day; Clear is the estimated time to drain the current backlog at
// the host's spacing.
type adminPollHost struct {
	Host       string
	Feeds      int
	Behind     int
	Spacing    string
	Demand     string
	Capacity   string
	Clear      string
	Overloaded bool
}

// adminStorage is the object-store breakdown shown on /admin: bytes per kind
// (avatars, icons) and per cache plugin folder, plus orphaned cached media that
// no row references.
type adminStorage struct {
	Groups        []storage.Group
	Total         string
	Orphans       string
	OrphanObjects int
}

// adminPluginRow is one loaded plugin shown in the plugins card.
type adminPluginRow struct {
	Name      string
	Version   string
	RawNet    bool
	UserAgent string
	Summary   string
	HasDocs   bool
	// ProvisionLabel is the add-feed create-menu entry when the plugin can
	// create remote feeds; "" when it cannot.
	ProvisionLabel string
	// CanManageFeeds reports whether the plugin manages existing feeds' remote
	// settings and lifecycle.
	CanManageFeeds bool
	// HasSettings reports whether the plugin declares admin-editable settings.
	HasSettings bool
	// Settings are the plugin's declared fields with their stored values, for
	// the settings modal opened from the card.
	Settings []adminPluginSetting
	// Owned is the debug view of the feeds this plugin currently fetches: the
	// registrable domains and the distinct feed URLs. Ownership is derived on
	// every startup, so this is the live answer, not a stored lock.
	Owned adminPluginOwnership
}

// adminPluginOwnership summarizes the feeds whose URL a plugin matches.
type adminPluginOwnership struct {
	Feeds   int
	Domains []string // registrable domains, sorted and unique
	URLs    []string // distinct feed URLs, sorted
}

// adminPluginSetting is one plugin setting field with its current value. A
// password field carries only whether a value is set (never the value itself),
// so a secret is not rendered back into the page.
type adminPluginSetting struct {
	Name        string
	Label       string
	Kind        string
	Placeholder string
	Help        string
	Required    bool
	Value       string // empty for a password field
	Set         bool   // for a password field: a value is stored
	Checked     bool   // for a bool field: the stored value is on
}

// adminBackup is the backup status shown on /admin. Enabled is false when
// automatic backups are not configured.
type adminBackup struct {
	Enabled     bool
	Destination string // "local: /path" or "s3: bucket/prefix"
	Interval    string // humanized, e.g. "24h0m0s"
	Keep        int
	LastRun     string // stored UTC time, "" when never run
	LastArchive string
	LastSize    string
	LastError   string
	Timezone    string
}

type adminStats struct {
	Users   int
	Feeds   int
	Authors int
	Items   int
	Unread  int
	Objects int
	Storage string
}

type adminUserRow struct {
	ID        int64
	Username  string
	IsAdmin   bool
	IsSelf    bool
	CreatedAt string
	Timezone  string
}

// adminOnly guards admin routes: only users with the admin flag may pass.
// Everyone else gets a visible 403 (a page for GET, an error fragment for htmx
// POSTs) so the denial is never silent.
func (s *Server) adminOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := auth.UserFrom(r)
		if ok && u.IsAdmin {
			next.ServeHTTP(w, r)
			return
		}
		log.Warn("admin route denied", "path", r.URL.Path)
		if r.Method != http.MethodGet {
			writeFormError(w, r, "admin-error", "admin only")
			return
		}
		w.WriteHeader(http.StatusForbidden)
		web.Render(w, r, basePage("forbidden", u, forbiddenPage("admin only")))
	})
}

func (s *Server) adminPage(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	d := adminData{
		Stats:   s.adminStats(r.Context()),
		Poll:    s.adminPollData(),
		Storage: s.adminStorageData(r.Context()),
		Users:   s.adminUserRows(u.ID, u.Timezone),
		Backup:  s.adminBackupData(u.Timezone),
		Plugins: s.adminPluginRows(),
	}
	d.AllowSignup = s.allowSignup()
	if d.AllowSignup {
		dismissed, err := s.store.Users.BannerDismissed(u.ID)
		if err != nil {
			log.Error("admin: banner dismissed", "user_id", u.ID, "err", err)
		}
		d.BannerShown = !dismissed
	}
	web.Render(w, r, basePage("admin", u, adminPage(u, d)))
}

// adminBackupData summarizes the automatic-backup configuration and the most
// recent run.
func (s *Server) adminBackupData(tz string) adminBackup {
	bc := s.cfg.Backup
	d := adminBackup{
		Enabled:  bc.Enabled(),
		Interval: bc.Interval.String(),
		Keep:     bc.Keep,
		Timezone: tz,
	}
	switch {
	case bc.UsesS3():
		d.Destination = "s3: " + bc.S3.Bucket
		if bc.S3.Prefix != "" {
			d.Destination += "/" + bc.S3.Prefix
		}
	case bc.Dir != "":
		d.Destination = "local: " + bc.Dir
	}
	if s.backups != nil {
		st := s.backups.Status()
		if !st.LastRun.IsZero() {
			d.LastRun = db.FormatTime(st.LastRun)
		}
		d.LastArchive = st.LastArchive
		d.LastError = st.LastError
		if st.LastBytes > 0 {
			d.LastSize = web.FormatBytes(st.LastBytes)
		}
	}
	return d
}

// adminBackupNow triggers an immediate snapshot and re-renders the backup card.
func (s *Server) adminBackupNow(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	if s.backups == nil {
		writeFormError(w, r, "admin-backup-error", "automatic backups are not configured")
		return
	}
	if _, err := s.backups.Snapshot(r.Context()); err != nil {
		log.Error("admin: manual backup", "err", err)
		writeFormError(w, r, "admin-backup-error", "backup failed — see server logs")
		return
	}
	web.Render(w, r, AdminBackupCard(s.adminBackupData(u.Timezone)))
}

func (s *Server) adminStats(ctx context.Context) adminStats {
	st := adminStats{}
	st.Users, _ = s.store.Users.Count()
	st.Feeds, _ = s.store.Feeds.Count()
	st.Authors, _ = s.store.Authors.Count()
	st.Items, _ = s.store.Items.Count()
	st.Unread, _ = s.store.Items.CountAllUnread()
	stat, err := s.files.Stat(ctx)
	if err != nil {
		log.Warn("admin: file storage stat", "err", err)
		st.Storage = "unavailable"
		return st
	}
	st.Objects = stat.Objects
	st.Storage = web.FormatBytes(stat.Bytes)
	return st
}

// adminPollData assembles the polling health card from every enabled feed. It
// uses the live poller for effective host spacing (which includes a learned
// rate-limit window), falling back to the configured default when no poller is
// attached (CLI or tests).
func (s *Server) adminPollData() adminPoll {
	rows, err := s.store.Feeds.ListAll()
	if err != nil {
		log.Warn("admin: poll stats", "err", err)
		return adminPoll{Unavailable: true}
	}
	feeds := make([]store.Feed, 0, len(rows))
	for _, f := range rows {
		feeds = append(feeds, f.Feed)
	}
	spacingFor := func(rawURL string) time.Duration {
		if s.poller != nil {
			return s.poller.EffectiveSpacing(rawURL)
		}
		return s.cfg.PollHostSpacing
	}
	return buildAdminPoll(time.Now().UTC(), feeds, spacingFor)
}

// buildAdminPoll computes the per-host polling load. A feed is behind when its
// last poll is older than its own interval; a never-polled feed is measured
// from its creation time so a new feed is not reported as overdue immediately.
// Demand is the sum of each enabled feed's required polls per day; capacity is
// what the host's spacing allows. demand > capacity means the host is
// permanently behind, not merely catching up after downtime.
func buildAdminPoll(now time.Time, feeds []store.Feed, spacingFor func(string) time.Duration) adminPoll {
	type hostAgg struct {
		feeds   int
		demand  float64
		behind  int
		spacing time.Duration
	}
	byHost := map[string]*hostAgg{}
	var out adminPoll
	var oldest time.Time
	for _, f := range feeds {
		if !f.Enabled || f.IsSystem {
			continue
		}
		out.Enabled++
		host := store.RegistrableDomain(f.FeedURL)
		if host == "" {
			host = f.FeedURL
		}
		h := byHost[host]
		if h == nil {
			h = &hostAgg{spacing: spacingFor(f.FeedURL)}
			byHost[host] = h
		}
		h.feeds++
		// The form enforces a 60s minimum; clamp defensively so demand cannot
		// divide by zero or count a nonsensical interval as infinite load.
		interval := f.PollIntervalSec
		if interval < 60 {
			interval = 60
		}
		h.demand += 86400 / float64(interval)

		ref := f.LastPolledAt
		if ref == "" {
			ref = f.CreatedAt
		}
		refTime, err := db.ParseTime(ref)
		if err != nil {
			continue
		}
		if now.After(refTime.Add(time.Duration(f.PollIntervalSec) * time.Second)) {
			h.behind++
			out.Behind++
			if oldest.IsZero() || refTime.Before(oldest) {
				oldest = refTime
				out.OldestTitle = f.Title
				out.OldestRef = ref
			}
		}
	}

	for host, h := range byHost {
		capacity := 0.0
		if h.spacing > 0 {
			capacity = 86400 / h.spacing.Seconds()
		}
		overloaded := capacity > 0 && h.demand > capacity
		if overloaded {
			out.Overloaded++
		}
		if !overloaded && h.behind == 0 {
			continue
		}
		row := adminPollHost{
			Host:       host,
			Feeds:      h.feeds,
			Behind:     h.behind,
			Spacing:    formatPollSpacing(h.spacing),
			Demand:     formatPollCount(h.demand),
			Overloaded: overloaded,
		}
		if capacity > 0 {
			row.Capacity = formatPollCount(capacity)
		} else {
			row.Capacity = "unlimited"
		}
		if h.spacing > 0 && h.behind > 0 {
			row.Clear = formatPollClear(time.Duration(h.behind) * h.spacing)
		}
		out.Hosts = append(out.Hosts, row)
	}

	sort.Slice(out.Hosts, func(i, j int) bool {
		a, b := out.Hosts[i], out.Hosts[j]
		if a.Overloaded != b.Overloaded {
			return a.Overloaded
		}
		if a.Behind != b.Behind {
			return a.Behind > b.Behind
		}
		return a.Host < b.Host
	})
	if len(out.Hosts) > 8 {
		out.Hosts = out.Hosts[:8]
	}
	return out
}

// formatPollSpacing renders a host spacing compactly.
func formatPollSpacing(d time.Duration) string {
	switch {
	case d <= 0:
		return "off"
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds()+0.5)) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes()+0.5)) + "m"
	default:
		return strconv.Itoa(int(d.Hours()+0.5)) + "h"
	}
}

// formatPollCount renders a daily request rate as a whole number.
func formatPollCount(f float64) string {
	return strconv.Itoa(int(f + 0.5))
}

// formatPollClear renders an estimated drain time.
func formatPollClear(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes()+0.5)) + "m"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d.Hours()+0.5)) + "h"
	default:
		return strconv.Itoa(int(d.Hours()/24+0.5)) + "d"
	}
}

// adminStorageData breaks the object store down by kind and cache plugin, and
// counts orphaned cached media (bytes no item or enclosure references).
func (s *Server) adminStorageData(ctx context.Context) adminStorage {
	rep, err := storage.Audit(ctx, s.files, s.store)
	if err != nil {
		log.Warn("admin: storage audit", "err", err)
		return adminStorage{Total: "unavailable"}
	}
	groups := rep.Groups
	if len(groups) > 12 {
		groups = groups[:12]
	}
	return adminStorage{
		Groups:        groups,
		Total:         web.FormatBytes(rep.TotalBytes),
		Orphans:       web.FormatBytes(rep.OrphanBytes),
		OrphanObjects: rep.OrphanObjects,
	}
}

func (s *Server) adminUserRows(selfID int64, tz string) []adminUserRow {
	users, err := s.store.Users.List()
	if err != nil {
		log.Error("admin: list users", "err", err)
		return nil
	}
	rows := make([]adminUserRow, 0, len(users))
	for _, u := range users {
		rows = append(rows, adminUserRow{
			ID:        u.ID,
			Username:  u.Username,
			IsAdmin:   u.IsAdmin,
			IsSelf:    u.ID == selfID,
			CreatedAt: u.CreatedAt,
			Timezone:  tz,
		})
	}
	return rows
}

// adminPluginRows describes the plugins currently loaded by the registry. It
// returns nil when no plugin system is attached.
func (s *Server) adminPluginRows() []adminPluginRow {
	if s.plugins == nil {
		return nil
	}
	infos := s.plugins.Infos()
	rows := make([]adminPluginRow, 0, len(infos))
	for _, in := range infos {
		row := adminPluginRow{
			Name:           in.Name,
			Version:        in.Version,
			RawNet:         in.RawNet,
			UserAgent:      in.UserAgent,
			Summary:        in.Summary,
			HasDocs:        in.HasDocs,
			ProvisionLabel: in.ProvisionLabel,
			CanManageFeeds: in.CanManageFeeds,
			HasSettings:    in.HasSettings,
		}
		if in.HasSettings {
			row.Settings = s.adminPluginSettings(in.Name)
		}
		rows = append(rows, row)
	}
	s.fillPluginOwnership(rows)
	return rows
}

// fillPluginOwnership matches every stored feed against the loaded registry and
// records, per plugin, which feeds it would fetch right now. This replaces the
// old stored-domain reset view: ownership is always derived, and this is the
// debugging surface for seeing what each plugin actually handles.
func (s *Server) fillPluginOwnership(rows []adminPluginRow) {
	if s.plugins == nil || s.store == nil || len(rows) == 0 {
		return
	}
	feeds, err := s.store.Feeds.ListAll()
	if err != nil {
		log.Error("admin: list feeds for plugin ownership", "err", err)
		return
	}
	index := make(map[string]int, len(rows))
	for i, r := range rows {
		index[r.Name] = i
	}
	domains := make([]map[string]bool, len(rows))
	urls := make([]map[string]bool, len(rows))
	for i := range rows {
		domains[i] = map[string]bool{}
		urls[i] = map[string]bool{}
	}
	for _, f := range feeds {
		u, err := url.Parse(f.FeedURL)
		if err != nil {
			continue
		}
		p := s.plugins.Match(u, pluginapi.CapFetch)
		if p == nil {
			continue
		}
		i, ok := index[p.Meta().Name]
		if !ok {
			continue
		}
		rows[i].Owned.Feeds++
		urls[i][f.FeedURL] = true
		if d := store.RegistrableDomain(f.FeedURL); d != "" {
			domains[i][d] = true
		}
	}
	for i := range rows {
		rows[i].Owned.Domains = sortedKeys(domains[i])
		rows[i].Owned.URLs = sortedKeys(urls[i])
	}
}

// sortedKeys returns a map's keys sorted.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// adminPluginSettings builds the settings form for one configurable plugin: its
// declared schema paired with the stored values. A "password" field reports only
// whether a value is set, so the secret is never sent to the browser.
func (s *Server) adminPluginSettings(name string) []adminPluginSetting {
	if s.plugins == nil {
		return nil
	}
	schema := s.plugins.SettingsSchema(name)
	names := make([]string, 0, len(schema))
	for _, f := range schema {
		names = append(names, f.Name)
	}
	values := s.store.Settings.PluginSettings(name, names)
	out := make([]adminPluginSetting, 0, len(schema))
	for _, f := range schema {
		row := adminPluginSetting{
			Name: f.Name, Label: f.Label, Kind: f.Kind,
			Placeholder: f.Placeholder, Help: f.Help, Required: f.Required,
		}
		if f.Kind == "password" {
			row.Set = values[f.Name] != ""
		} else if f.Kind == "bool" {
			row.Checked = settingBool(values[f.Name])
		} else {
			row.Value = values[f.Name]
		}
		out = append(out, row)
	}
	return out
}

// adminSavePluginSettings validates and stores a plugin's settings, then pushes
// them to the plugin so its behavior (and URL matching) updates immediately. The
// card re-renders with the result.
func (s *Server) adminSavePluginSettings(w http.ResponseWriter, r *http.Request) {
	if s.plugins == nil {
		writeFormError(w, r, "admin-plugins-error", "plugins are not loaded")
		return
	}
	name := r.PathValue("name")
	if s.plugins.ByName(name) == nil {
		writeFormError(w, r, "admin-plugins-error", "no such plugin")
		return
	}
	schema := s.plugins.SettingsSchema(name)
	if len(schema) == 0 {
		writeFormError(w, r, "admin-plugins-error", "that plugin has no settings")
		return
	}
	errTarget := "plugin-settings-error-" + name
	values := make(map[string]string, len(schema))
	for _, f := range schema {
		v := strings.TrimSpace(r.FormValue("setting_" + f.Name))
		// A bool always stores a definite "1"/"0": an unchecked box submits
		// nothing, so a missing value means off.
		if f.Kind == "bool" {
			if settingBool(v) {
				values[f.Name] = "1"
			} else {
				values[f.Name] = "0"
			}
			continue
		}
		if f.Required && v == "" {
			writeFormError(w, r, errTarget, f.Label+" is required")
			return
		}
		// A write-only password field is left unchanged when blank, so the
		// stored secret survives a save that did not re-enter it.
		if f.Kind == "password" && v == "" {
			if r.FormValue("clear_"+f.Name) == "1" {
				values[f.Name] = ""
			}
			continue
		}
		values[f.Name] = v
	}
	if err := s.store.Settings.SetPluginSettings(name, values); err != nil {
		log.Error("admin: save plugin settings", "plugin", name, "err", err)
		writeFormError(w, r, "admin-plugins-error", "could not save the settings")
		return
	}
	s.plugins.Configure(name, s.store.Settings.PluginSettings(name, settingNames(schema)))
	log.Info("admin: saved plugin settings", "plugin", name)
	web.Render(w, r, AdminPluginsCard(s.adminPluginRows()))
}

// settingNames extracts the field names from a settings schema.
func settingNames(schema []pluginapi.SettingField) []string {
	out := make([]string, 0, len(schema))
	for _, f := range schema {
		out = append(out, f.Name)
	}
	return out
}

// settingBool reports whether a stored or submitted setting value is "on". A
// bool field is stored as "1"/"0"; older or hand-written values may be "on",
// "true", or "yes".
func settingBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "on", "true", "yes":
		return true
	}
	return false
}

// adminInstanceData rebuilds the data the instance settings card needs, for
// re-rendering the card after a mutation.
func (s *Server) adminInstanceData(u store.User, ctx context.Context) adminData {
	d := adminData{Stats: s.adminStats(ctx)}
	d.AllowSignup = s.allowSignup()
	if d.AllowSignup {
		dismissed, err := s.store.Users.BannerDismissed(u.ID)
		if err != nil {
			log.Error("admin: banner dismissed", "user_id", u.ID, "err", err)
		}
		d.BannerShown = !dismissed
	}
	return d
}

// adminSetSignup toggles the global signup setting from the instance card.
func (s *Server) adminSetSignup(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	// The signup switch's hidden mirror submits "1" when on (see switchField);
	// the banner's "disable signups" button posts "false". Accept both.
	v := r.FormValue("allow")
	allow := v == "1" || v == "true"
	if err := s.store.Settings.SetAllowSignup(allow); err != nil {
		log.Error("admin: set allow_signup", "err", err)
		writeFormError(w, r, "admin-instance-error", "could not update setting")
		return
	}
	web.Render(w, r, AdminInstanceCard(s.adminInstanceData(u, r.Context())))
}

// adminDismissSignupBanner marks the signup banner as dismissed for the current
// admin (a per-user preference).
func (s *Server) adminDismissSignupBanner(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	if err := s.store.Users.SetBannerDismissed(u.ID, true); err != nil {
		log.Error("admin: dismiss banner", "user_id", u.ID, "err", err)
		writeFormError(w, r, "admin-instance-error", "could not dismiss")
		return
	}
	web.Render(w, r, AdminInstanceCard(s.adminInstanceData(u, r.Context())))
}

// adminResetPassword sets a new random password for a user, logs out all of
// their sessions, and shows the generated password once so the admin can pass
// it on.
func (s *Server) adminResetPassword(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	userID, ok := adminUserID(w, r)
	if !ok {
		return
	}
	target, err := s.store.Users.ByID(userID)
	if err != nil {
		writeFormError(w, r, adminErrorTarget(userID), "no such user")
		return
	}
	pw, err := randomPassword()
	if err != nil {
		log.Error("admin: generate password", "err", err)
		writeFormError(w, r, adminErrorTarget(userID), "could not reset password")
		return
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		log.Error("admin: hash password", "err", err)
		writeFormError(w, r, adminErrorTarget(userID), "could not reset password")
		return
	}
	if err := s.store.Users.ResetPassword(userID, hash); err != nil {
		log.Error("admin: reset password", "user_id", userID, "err", err)
		writeFormError(w, r, adminErrorTarget(userID), "could not reset password")
		return
	}
	if err := s.store.Sessions.DeleteUserSessions(userID); err != nil {
		log.Error("admin: revoke sessions", "user_id", userID, "err", err)
	}
	notice := "new password for " + target.Username + ": " + pw + " (shown once, share it with them)"
	web.Render(w, r, AdminUserList(s.adminUserRows(u.ID, u.Timezone), notice))
}

// randomPassword returns a URL-safe temporary password from crypto/rand.
func randomPassword() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// adminSetAdmin grants or revokes the admin flag. The last remaining admin
// cannot be demoted.
func (s *Server) adminSetAdmin(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	userID, ok := adminUserID(w, r)
	if !ok {
		return
	}
	admin := r.FormValue("admin") == "true"
	target, err := s.store.Users.ByID(userID)
	if err != nil {
		writeFormError(w, r, adminErrorTarget(userID), "no such user")
		return
	}
	if !admin && target.IsAdmin {
		admins, err := s.store.Users.CountAdmins()
		if err != nil {
			log.Error("admin: count admins", "err", err)
			writeFormError(w, r, adminErrorTarget(userID), "could not update admin flag")
			return
		}
		if admins <= 1 {
			writeFormError(w, r, adminErrorTarget(userID), "cannot remove the last admin")
			return
		}
	}
	if err := s.store.Users.SetAdmin(userID, admin); err != nil {
		log.Error("admin: set admin", "user_id", userID, "err", err)
		writeFormError(w, r, adminErrorTarget(userID), "could not update admin flag")
		return
	}
	web.Render(w, r, AdminUserList(s.adminUserRows(u.ID, u.Timezone), ""))
}

// adminDeleteUser removes a user and their data, purging avatar/icon blobs
// from object storage. The current user and the last admin are protected.
func (s *Server) adminDeleteUser(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	userID, ok := adminUserID(w, r)
	if !ok {
		return
	}
	if userID == u.ID {
		writeFormError(w, r, adminErrorTarget(userID), "cannot delete your own account")
		return
	}
	target, err := s.store.Users.ByID(userID)
	if err != nil {
		writeFormError(w, r, adminErrorTarget(userID), "no such user")
		return
	}
	if target.IsAdmin {
		admins, err := s.store.Users.CountAdmins()
		if err != nil {
			log.Error("admin: count admins", "err", err)
			writeFormError(w, r, adminErrorTarget(userID), "could not delete user")
			return
		}
		if admins <= 1 {
			writeFormError(w, r, adminErrorTarget(userID), "cannot delete the last admin")
			return
		}
	}
	// Purge object-storage blobs best-effort: the DB row is the source of
	// truth, so a flaky store must not block deletion.
	keys, err := s.store.Users.ListObjectKeys(userID)
	if err == nil {
		for _, k := range keys {
			if err := s.files.Delete(r.Context(), k); err != nil {
				log.Warn("admin: purge object", "key", k, "err", err)
			}
		}
	}
	if err := s.store.Users.Delete(userID); err != nil {
		log.Error("admin: delete user", "user_id", userID, "err", err)
		writeFormError(w, r, adminErrorTarget(userID), "could not delete user")
		return
	}
	web.Render(w, r, AdminUserList(s.adminUserRows(u.ID, u.Timezone), ""))
}

// adminUserID parses the {id} path value, writing a 400 error fragment and
// returning false when it is missing or not a number.
func adminUserID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeFormError(w, r, "admin-error", "invalid user")
		return 0, false
	}
	return id, true
}

func adminErrorTarget(userID int64) string {
	return "admin-user-" + strconv.FormatInt(userID, 10) + "-error"
}
