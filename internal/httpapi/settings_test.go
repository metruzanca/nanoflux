package httpapi

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/metruzanca/nanoflux/internal/auth"
	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/store"
)

func TestSettingsPage(t *testing.T) {
	_, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	body := doGet(h, "/settings", cookie).Body.String()
	for _, want := range []string{
		`hx-post="/settings/avatar"`,
		`hx-post="/settings/icons"`,
		`hx-post="/settings/timezone"`,
		`hx-post="/settings/theme"`,
		`hx-post="/settings/accent"`,
		`hx-post="/settings/opml"`,
		`hx-post="/settings/mappings"`,
		`/settings/export.opml`,
		`/settings/extension.zip`,
		"browser extension",
		`name="domain"`,
		"custom source icons",
		"url mappings",
		"profile picture",
		"timezone",
		"keyboard shortcuts",
		`<td>j / k</td><td>next / previous item</td>`,
		`<td>/</td><td>focus the search box</td>`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("settings page missing %q", want)
		}
	}
}

func TestSettingsExtensionZip(t *testing.T) {
	_, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	rr := doGet(h, "/settings/extension.zip", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("extension zip: %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("content-type = %q", ct)
	}
	zr, err := zip.NewReader(bytes.NewReader(rr.Body.Bytes()), int64(rr.Body.Len()))
	if err != nil {
		t.Fatalf("not a zip: %v", err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	for _, want := range []string{"manifest.json", "popup.html", "popup.js", "api.js", "icons/icon128.png"} {
		if !names[want] {
			t.Fatalf("zip missing %q; got %v", want, names)
		}
	}
	if names["embed.go"] {
		t.Fatal("zip should not include the embed source")
	}
	// The manifest parses and is the extension's.
	rc, err := zr.Open("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	data, _ := io.ReadAll(rc)
	if !bytes.Contains(data, []byte(`"manifest_version": 3`)) {
		t.Fatalf("unexpected manifest: %s", data)
	}
}

func TestSettingsAvatar(t *testing.T) {
	_, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	rr := uploadForm(h, "/settings/avatar", "avatar", "me.png", png, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("upload avatar: %d %s", rr.Code, rr.Body.String())
	}
	// The stored avatar is served at /avatar.
	img := doGet(h, "/avatar", cookie)
	if img.Code != http.StatusOK || img.Body.String() != string(png) {
		t.Fatalf("avatar bytes: %d %q", img.Code, img.Body.String())
	}
	// Topbar shows the avatar now.
	body := doGet(h, "/settings", cookie).Body.String()
	if !strings.Contains(body, `src="/avatar"`) {
		t.Fatalf("settings page should render the avatar: %s", body)
	}

	// Non-image upload -> 400 with a visible error.
	rr = uploadForm(h, "/settings/avatar", "avatar", "x.txt", []byte("hello"), cookie)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), `role="alert"`) {
		t.Fatalf("non-image upload: %d %s", rr.Code, rr.Body.String())
	}
	// Missing file -> 400.
	rr = doForm(h, "POST", "/settings/avatar", url.Values{}, cookie)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "choose a picture") {
		t.Fatalf("missing upload: %d %s", rr.Code, rr.Body.String())
	}
}

func TestTopbarUserMenu(t *testing.T) {
	_, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	// No profile picture: the topbar shows the default initial-letter avatar.
	body := doGet(h, "/settings", cookie).Body.String()
	for _, want := range []string{
		`class="user-menu"`,
		`id="user-menu-btn"`,
		`avatar-default">A</span>`,
		`href="/settings"`,
		`action="/logout" method="post"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("topbar user menu missing %q", want)
		}
	}

	// Upload a picture: the default avatar is replaced by the real one.
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	rr := uploadForm(h, "/settings/avatar", "avatar", "me.png", png, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("upload avatar: %d %s", rr.Code, rr.Body.String())
	}
	body = doGet(h, "/settings", cookie).Body.String()
	if !strings.Contains(body, `src="/avatar"`) {
		t.Fatalf("topbar should render the avatar image: %s", body)
	}
	if strings.Contains(body, "avatar-default") {
		t.Fatalf("topbar should not show the default avatar: %s", body)
	}
}

// uploadForm posts a multipart form with one file field.
func uploadForm(h http.Handler, path, field, filename string, data []byte, cookie *http.Cookie) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile(field, filename)
	fw.Write(data)
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, path, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestSettingsTimezone(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")

	// Set a valid IANA timezone.
	rr := doForm(h, "POST", "/settings/timezone", url.Values{"timezone": {"America/New_York"}}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("set timezone: %d %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `name="timezone"`) ||
		!strings.Contains(rr.Body.String(), `value="America/New_York"`) {
		t.Fatalf("timezone card should reflect the saved value: %s", rr.Body.String())
	}
	after, _ := s.store.Users.ByID(u.ID)
	if after.Timezone != "America/New_York" {
		t.Fatalf("timezone not persisted: %q", after.Timezone)
	}

	// Invalid timezone -> 400 with a visible error, nothing saved.
	rr = doForm(h, "POST", "/settings/timezone", url.Values{"timezone": {"Mars/Olympus"}}, cookie)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), `role="alert"`) {
		t.Fatalf("invalid timezone: %d %s", rr.Code, rr.Body.String())
	}
	after, _ = s.store.Users.ByID(u.ID)
	if after.Timezone != "America/New_York" {
		t.Fatalf("invalid timezone should not overwrite: %q", after.Timezone)
	}

	// Clearing the timezone restores the server-time default.
	rr = doForm(h, "POST", "/settings/timezone", url.Values{"timezone": {""}}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("clear timezone: %d", rr.Code)
	}
	after, _ = s.store.Users.ByID(u.ID)
	if after.Timezone != "" {
		t.Fatalf("timezone should be cleared: %q", after.Timezone)
	}
}

func TestSettingsTheme(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")

	// Default is dark; the layout renders it on <html>.
	body := doGet(h, "/", cookie).Body.String()
	if !strings.Contains(body, `data-theme="dark"`) {
		t.Fatalf("home should render data-theme=dark by default: %s", body)
	}

	// Switch to light.
	rr := doForm(h, "POST", "/settings/theme", url.Values{"theme": {"light"}}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("set theme: %d %s", rr.Code, rr.Body.String())
	}
	after, _ := s.store.Users.ByID(u.ID)
	if after.Theme != "light" {
		t.Fatalf("theme not persisted: %q", after.Theme)
	}
	body = doGet(h, "/", cookie).Body.String()
	if !strings.Contains(body, `data-theme="light"`) {
		t.Fatalf("home should render data-theme=light: %s", body)
	}

	// Invalid value -> 400, nothing saved.
	rr = doForm(h, "POST", "/settings/theme", url.Values{"theme": {"neon"}}, cookie)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid theme: %d %s", rr.Code, rr.Body.String())
	}
	after, _ = s.store.Users.ByID(u.ID)
	if after.Theme != "light" {
		t.Fatalf("invalid theme should not overwrite: %q", after.Theme)
	}

	// "system" is accepted.
	rr = doForm(h, "POST", "/settings/theme", url.Values{"theme": {"system"}}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("set system theme: %d", rr.Code)
	}
	after, _ = s.store.Users.ByID(u.ID)
	if after.Theme != "system" {
		t.Fatalf("theme not persisted: %q", after.Theme)
	}
}

func TestSettingsAccent(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")

	// Default accent is rendered inline on <html>.
	body := doGet(h, "/", cookie).Body.String()
	if !strings.Contains(body, `style="--accent: #5b8cff;"`) {
		t.Fatalf("home should render the default accent: %s", body)
	}

	// Set a custom accent.
	rr := doForm(h, "POST", "/settings/accent", url.Values{"accent": {"#ff0000"}}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("set accent: %d %s", rr.Code, rr.Body.String())
	}
	after, _ := s.store.Users.ByID(u.ID)
	if after.AccentColor != "#ff0000" {
		t.Fatalf("accent not persisted: %q", after.AccentColor)
	}
	body = doGet(h, "/", cookie).Body.String()
	if !strings.Contains(body, `style="--accent: #ff0000;"`) {
		t.Fatalf("home should render the custom accent: %s", body)
	}

	// Invalid value -> 400, nothing saved.
	rr = doForm(h, "POST", "/settings/accent", url.Values{"accent": {"notacolor"}}, cookie)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid accent: %d %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `role="alert"`) {
		t.Fatalf("invalid accent should render an error: %s", rr.Body.String())
	}
	after, _ = s.store.Users.ByID(u.ID)
	if after.AccentColor != "#ff0000" {
		t.Fatalf("invalid accent should not overwrite: %q", after.AccentColor)
	}

	// Empty resets to the default.
	rr = doForm(h, "POST", "/settings/accent", url.Values{"accent": {""}}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("reset accent: %d %s", rr.Code, rr.Body.String())
	}
	after, _ = s.store.Users.ByID(u.ID)
	if after.AccentColor != "#5b8cff" {
		t.Fatalf("accent should reset to default: %q", after.AccentColor)
	}
}

func TestFeedCreateAutoFavicon(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")

	// A fake site that serves an HTML page with a favicon link plus the icon.
	var iconReq bool
	var base string
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, `<html><head><link rel="icon" href="%s/icon.png"></head><body>hi</body></html>`, base)
		case "/icon.png":
			iconReq = true
			w.Header().Set("Content-Type", "image/png")
			w.Write([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a})
		default:
			http.NotFound(w, r)
		}
	}))
	defer site.Close()
	base = site.URL

	rr := doForm(h, "POST", "/feeds", url.Values{
		"title": {"Site"}, "feed_url": {base + "/rss.xml"}, "home_url": {base},
		"author_id": {"new"}, "author_name": {"Site"},
	}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("create feed: %d %s", rr.Code, rr.Body.String())
	}
	if !iconReq {
		t.Fatal("feed create should have fetched the site favicon")
	}

	domain := normalizeDomain(base)
	icons, _ := s.store.SourceIcons.List(u.ID)
	if len(icons) != 1 || icons[0].Domain != domain || icons[0].IconKey == "" {
		t.Fatalf("expected one cached icon for %q: %+v", domain, icons)
	}

	// The icon is served at /icons/{domain}.
	got := doGet(h, "/icons/"+domain, cookie)
	if got.Code != http.StatusOK || got.Body.String() != string([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}) {
		t.Fatalf("cached icon: %d %q", got.Code, got.Body.String())
	}
}

func TestOpmlExport(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "", "")
	s.store.Feeds.Create(u.ID, a.ID, "Solo", "https://solo.dev/rss.xml", "https://solo.dev", "", 900)
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Grouped", "https://group.dev/rss.xml", "https://group.dev", "", 900)
	c, _ := s.store.Collections.Create(u.ID, "tech")
	s.store.Collections.AddFeed(u.ID, c.ID, f.ID)

	rr := doGet(h, "/settings/export.opml", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("export: %d %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `xmlUrl="https://solo.dev/rss.xml"`) {
		t.Fatalf("export missing ungrouped feed: %s", body)
	}
	if !strings.Contains(body, `text="tech"`) || !strings.Contains(body, `xmlUrl="https://group.dev/rss.xml"`) {
		t.Fatalf("export missing collection group: %s", body)
	}
	if !strings.Contains(body, `<?xml version="1.0"`) {
		t.Fatalf("export should be an xml document: %s", body)
	}
}

func TestOpmlImport(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")

	// A feed server the import validates against.
	feedSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>Imported Feed</title><link>https://imp.dev/</link></channel></rss>`))
	}))
	defer feedSrv.Close()
	s.client = feedSrv.Client()

	opml := `<?xml version="1.0"?>
<opml version="2.0"><head><title>x</title></head><body>
  <outline type="rss" text="Imported Feed" title="Imported Feed" xmlUrl="` + feedSrv.URL + `/feed" htmlUrl="https://imp.dev/"/>
  <outline text="tech">
    <outline type="rss" title="Grouped Feed" xmlUrl="` + feedSrv.URL + `/group"/>
  </outline>
</body></opml>`

	rr := uploadForm(h, "/settings/opml", "file", "feeds.opml", []byte(opml), cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("import: %d %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "imported 2") {
		t.Fatalf("import summary missing: %s", rr.Body.String())
	}
	feeds, _ := s.store.Feeds.List(u.ID)
	if len(feeds) != 2 {
		t.Fatalf("expected 2 imported feeds, got %d", len(feeds))
	}
	// Every imported feed belongs to an author named after its outline.
	authors, _ := s.store.Authors.List(u.ID)
	byName := map[string]store.Author{}
	for _, a := range authors {
		byName[a.Name] = a
	}
	if byName["Imported Feed"].ID == 0 || byName["Grouped Feed"].ID == 0 || len(byName) != 2 {
		t.Fatalf("import should create one author per feed outline: %+v", byName)
	}
	for _, f := range feeds {
		if f.AuthorID == 0 {
			t.Fatalf("imported feed missing author: %+v", f)
		}
	}
	// The grouped feed lands in a "tech" collection; each imported feed also
	// auto-lands in a per-website collection.
	cols, _ := s.store.Collections.List(u.ID)
	var tech store.Collection
	for _, c := range cols {
		if c.Name == "tech" {
			tech = c
		}
	}
	if tech.ID == 0 {
		t.Fatalf("expected a tech collection: %+v", cols)
	}
	if tech.IsAuto {
		t.Fatalf("tech collection should be a manual collection: %+v", tech)
	}
	in, _ := s.store.Collections.Feeds(u.ID, tech.ID)
	if len(in) != 1 {
		t.Fatalf("collection should contain the grouped feed: %+v", in)
	}
	// The imp.dev home url maps to an auto collection for that site.
	var imp store.Collection
	for _, c := range cols {
		if c.IsAuto && c.Name == "imp.dev" {
			imp = c
		}
	}
	if imp.ID == 0 {
		t.Fatalf("expected an imp.dev auto collection: %+v", cols)
	}

	// Re-importing skips the existing feed urls.
	rr = uploadForm(h, "/settings/opml", "file", "feeds.opml", []byte(opml), cookie)
	if !strings.Contains(rr.Body.String(), "skipped 2") {
		t.Fatalf("re-import should skip existing: %s", rr.Body.String())
	}

	// Invalid xml -> 400 with a visible error.
	rr = uploadForm(h, "/settings/opml", "file", "bad.opml", []byte("not xml"), cookie)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), `role="alert"`) {
		t.Fatalf("invalid opml: %d %s", rr.Code, rr.Body.String())
	}
}

func iconServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a})
	}))
}

func TestSettingsIconsFlow(t *testing.T) {
	_, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	iconSrv := iconServer(t)
	defer iconSrv.Close()

	// Add a custom icon for a domain (with the scheme, so normalization is exercised).
	rr := doForm(h, "POST", "/settings/icons", url.Values{
		"domain": {"https://GitHub.com"}, "icon_url": {iconSrv.URL + "/icon.png"},
	}, cookie)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `id="settings-icon-`) {
		t.Fatalf("add icon: %d %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "cached") {
		t.Fatalf("icon should be cached on add: %s", rr.Body.String())
	}

	// /icons/<domain> serves the cached custom bytes, not a built-in SVG.
	body := doGet(h, "/icons/github.com", cookie).Body.String()
	if body != string([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}) {
		t.Fatalf("custom icon bytes not served: %q", body)
	}

	// Built-in icons still work for unknown domains and brands.
	webIcon := doGet(h, "/icons/example.com", cookie)
	if webIcon.Code != http.StatusOK || !strings.Contains(webIcon.Body.String(), "<svg") {
		t.Fatalf("builtin globe: %d", webIcon.Code)
	}
	xIcon := doGet(h, "/icons/x.com", cookie)
	if xIcon.Code != http.StatusOK || !strings.Contains(xIcon.Body.String(), "<svg") {
		t.Fatalf("builtin x icon: %d", xIcon.Code)
	}
	youtubeIcon := doGet(h, "/icons/www.youtube.com", cookie)
	if youtubeIcon.Code != http.StatusOK || !strings.Contains(youtubeIcon.Body.String(), "<svg") {
		t.Fatalf("builtin youtube icon: %d", youtubeIcon.Code)
	}

	// Duplicate domain -> 400 with a visible error.
	rr = doForm(h, "POST", "/settings/icons", url.Values{
		"domain": {"github.com"}, "icon_url": {iconSrv.URL + "/icon.png"},
	}, cookie)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "already exists") {
		t.Fatalf("duplicate icon: %d %s", rr.Code, rr.Body.String())
	}

	// Delete -> list is empty, /icons falls back to built-in.
	ids, err := listIconIDs(t, h, cookie)
	if err != nil {
		t.Fatal(err)
	}
	rr = doForm(h, "POST", "/settings/icons/"+ids[0]+"/delete", url.Values{}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete icon: %d %s", rr.Code, rr.Body.String())
	}
	body = doGet(h, "/icons/github.com", cookie).Body.String()
	if !strings.Contains(body, "<svg") {
		t.Fatalf("expected builtin fallback after delete: %q", body)
	}
}

func listIconIDs(t *testing.T, h http.Handler, cookie *http.Cookie) ([]string, error) {
	t.Helper()
	body := doGet(h, "/settings", cookie).Body.String()
	var ids []string
	for _, part := range strings.Split(body, `id="settings-icon-`) {
		if i := strings.IndexByte(part, '"'); i > 0 {
			id := part[:i]
			if id != "" && isAllDigits(id) {
				ids = append(ids, id)
			}
		}
	}
	if len(ids) == 0 {
		t.Fatalf("no icon rows found in %q", body)
	}
	return ids, nil
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func TestSettingsChangePassword(t *testing.T) {
	s, h := newTestServer(t)
	u, _ := s.store.Users.ByUsername("alice")
	other := "other-token"
	if err := s.store.Sessions.Create(u.ID, other, db.FormatTime(time.Now().Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, h)

	// Wrong current password rejected.
	rr := doForm(h, "POST", "/settings/password", url.Values{
		"current_password": {"nope"}, "new_password": {"newpass123"}, "confirm_password": {"newpass123"},
	}, cookie)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "current password is incorrect") {
		t.Fatalf("wrong current pw: %d %s", rr.Code, rr.Body.String())
	}

	// Mismatched confirm rejected.
	rr = doForm(h, "POST", "/settings/password", url.Values{
		"current_password": {"secret"}, "new_password": {"newpass123"}, "confirm_password": {"different"},
	}, cookie)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("mismatched confirm: %d", rr.Code)
	}

	// Success rotates the hash and logs out other sessions.
	rr = doForm(h, "POST", "/settings/password", url.Values{
		"current_password": {"secret"}, "new_password": {"newpass123"}, "confirm_password": {"newpass123"},
	}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("change password: %d %s", rr.Code, rr.Body.String())
	}
	got, _ := s.store.Users.ByID(u.ID)
	if !auth.CheckPassword(got.PasswordHash, "newpass123") {
		t.Fatal("password hash not updated")
	}
	if _, err := s.store.Sessions.UserByToken(other); err == nil {
		t.Fatal("other session should be revoked")
	}
	// The current session survives.
	if _, err := s.store.Sessions.UserByToken(cookie.Value); err != nil {
		t.Fatalf("current session should survive: %v", err)
	}
}

func TestSettingsSessionsRevoke(t *testing.T) {
	s, h := newTestServer(t)
	u, _ := s.store.Users.ByUsername("alice")
	if err := s.store.Sessions.Create(u.ID, "ghost", db.FormatTime(time.Now().Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, h)

	body := doGet(h, "/settings", cookie).Body.String()
	if !strings.Contains(body, "this device") || !strings.Contains(body, "ghost") {
		t.Fatal("settings should list sessions")
	}

	rr := doForm(h, "POST", "/settings/sessions/ghost/revoke", url.Values{}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", rr.Code, rr.Body.String())
	}
	if _, err := s.store.Sessions.UserByToken("ghost"); err == nil {
		t.Fatal("ghost session should be gone")
	}

	// Revoking the current session logs out.
	rr = doForm(h, "POST", "/settings/sessions/"+cookie.Value+"/revoke", url.Values{}, cookie)
	if rr.Code != http.StatusFound || rr.Header().Get("Location") != "/login" {
		t.Fatalf("revoke current: %d %s", rr.Code, rr.Header().Get("Location"))
	}
}

func TestLoginRateLimit(t *testing.T) {
	_, h := newTestServer(t)
	for i := 0; i < 5; i++ {
		rr := doForm(h, "POST", "/login", url.Values{"username": {"alice"}, "password": {"wrong"}}, nil)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: got %d", i, rr.Code)
		}
	}
	rr := doForm(h, "POST", "/login", url.Values{"username": {"alice"}, "password": {"secret"}}, nil)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("6th attempt with correct password: got %d, want 429", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "too many attempts") {
		t.Fatalf("429 should show a message: %s", rr.Body.String())
	}
}

func TestLoginRateLimitClearsOnSuccess(t *testing.T) {
	_, h := newTestServer(t)
	for i := 0; i < 4; i++ {
		doForm(h, "POST", "/login", url.Values{"username": {"alice"}, "password": {"wrong"}}, nil)
	}
	// A successful login below the threshold resets the counter.
	if rr := doForm(h, "POST", "/login", url.Values{"username": {"alice"}, "password": {"secret"}}, nil); rr.Code != http.StatusFound {
		t.Fatalf("successful login: %d", rr.Code)
	}
	if rr := doForm(h, "POST", "/login", url.Values{"username": {"alice"}, "password": {"wrong"}}, nil); rr.Code == http.StatusTooManyRequests {
		t.Fatal("limiter should have been cleared after success")
	}
}

func TestSecureCookieDetection(t *testing.T) {
	_, h := newTestServer(t)

	// Plain HTTP -> not Secure.
	rr := login(t, h)
	for _, c := range rr.Result().Cookies() {
		if c.Name == auth.SessionCookieName && c.Secure {
			t.Fatal("plain HTTP cookie should not be Secure")
		}
	}

	// X-Forwarded-Proto: https -> Secure.
	form := url.Values{"username": {"alice"}, "password": {"secret"}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Forwarded-Proto", "https")
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, req)
	for _, c := range rr2.Result().Cookies() {
		if c.Name == auth.SessionCookieName && !c.Secure {
			t.Fatal("cookie behind https proxy should be Secure")
		}
	}
}
