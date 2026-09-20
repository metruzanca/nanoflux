package httpapi

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestSettingsPage(t *testing.T) {
	_, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	body := doGet(h, "/settings", cookie).Body.String()
	for _, want := range []string{
		`hx-post="/settings/avatar"`,
		`hx-post="/settings/icons"`,
		`name="domain"`,
		"custom source icons",
		"profile picture",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("settings page missing %q", want)
		}
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
