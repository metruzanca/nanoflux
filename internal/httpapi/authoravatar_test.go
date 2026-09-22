package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestAuthorAvatarRefresh(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	img := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(img)
	}))
	defer upstream.Close()

	u, _ := s.store.Users.ByUsername("alice")
	a, err := s.store.Authors.Create(u.ID, "Metru", upstream.URL+"/avatar.png", "")
	if err != nil {
		t.Fatal(err)
	}
	base := "/authors/" + itoa(a.ID)

	// Not cached yet: the avatar endpoint 404s and the row falls back to /img.
	if rr := doGet(h, base+"/avatar", cookie); rr.Code != http.StatusNotFound {
		t.Fatalf("uncached avatar: %d", rr.Code)
	}
	rows := doGet(h, "/authors", cookie).Body.String()
	if !strings.Contains(rows, "/img?u="+url.QueryEscape(upstream.URL+"/avatar.png")) {
		t.Fatalf("uncached author should fall back to the /img proxy: %s", rows)
	}

	// Refetch caches the avatar; the card shows it as cached.
	rr := doForm(h, "POST", base+"/avatar-refresh", url.Values{}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("refresh: %d %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "cached") {
		t.Fatalf("refresh should show cached status: %s", rr.Body.String())
	}

	// The cached avatar is served with the upstream content type.
	got := doGet(h, base+"/avatar", cookie)
	if got.Code != http.StatusOK {
		t.Fatalf("cached avatar: %d", got.Code)
	}
	if ct := got.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("content-type = %q", ct)
	}
	if string(got.Body.Bytes()) != string(img) {
		t.Fatalf("cached bytes mismatch")
	}

	// The author row now renders the cached endpoint instead of the proxy.
	rows = doGet(h, "/authors", cookie).Body.String()
	if !strings.Contains(rows, base+"/avatar?v=") {
		t.Fatalf("author row should render the cached avatar: %s", rows)
	}
	if strings.Contains(rows, "/img?u=") {
		t.Fatalf("author row should not use the /img proxy once cached: %s", rows)
	}

	// A second refetch overwrites the same object: still exactly one blob, and
	// the deterministic key never changes.
	if rr := doForm(h, "POST", base+"/avatar-refresh", url.Values{}, cookie); rr.Code != http.StatusOK {
		t.Fatalf("second refresh: %d %s", rr.Code, rr.Body.String())
	}
	st, _ := s.files.Stat(context.Background())
	if st.Objects != 1 {
		t.Fatalf("objects after refetch = %d, want 1", st.Objects)
	}
	a, _ = s.store.Authors.ByID(u.ID, a.ID)
	if want := "author-avatars/" + itoa(u.ID) + "/" + itoa(a.ID); a.AvatarKey != want {
		t.Fatalf("avatar key = %q, want %q", a.AvatarKey, want)
	}
}

func TestAuthorAvatarRefreshErrors(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html>"))
	}))
	defer bad.Close()

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", bad.URL, "")
	rr := doForm(h, "POST", "/authors/"+itoa(a.ID)+"/avatar-refresh", url.Values{}, cookie)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("non-image refresh: %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "could not fetch avatar") || !strings.Contains(rr.Body.String(), `role="alert"`) {
		t.Fatalf("error should be visible in the swapped card: %s", rr.Body.String())
	}

	// No avatar url set -> 400 with a visible message.
	b, _ := s.store.Authors.Create(u.ID, "NoAvatar", "", "")
	rr = doForm(h, "POST", "/authors/"+itoa(b.ID)+"/avatar-refresh", url.Values{}, cookie)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "set an avatar url first") {
		t.Fatalf("missing avatar url: %d %s", rr.Code, rr.Body.String())
	}
}

func TestAuthorAvatarPurgedOnDeleteAndURLChange(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	imgA := []byte{0x89, 'P', 'N', 'G', 'A'}
	imgB := []byte{0x89, 'P', 'N', 'G', 'B'}
	newImg := []byte{0x89, 'P', 'N', 'G', 'C'}
	serve := func(data []byte) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "image/png")
			w.Write(data)
		}))
	}
	upstreamA := serve(imgA)
	defer upstreamA.Close()
	upstreamB := serve(imgB)
	defer upstreamB.Close()
	upstreamC := serve(newImg)
	defer upstreamC.Close()

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", upstreamA.URL, "")

	// Deleting an author purges its cached object.
	doForm(h, "POST", "/authors/"+itoa(a.ID)+"/avatar-refresh", url.Values{}, cookie)
	doForm(h, "POST", "/authors/"+itoa(a.ID)+"/delete", url.Values{}, cookie)
	if st, _ := s.files.Stat(context.Background()); st.Objects != 0 {
		t.Fatalf("objects after author delete = %d, want 0", st.Objects)
	}

	// Changing avatar_url on update replaces the object at the same key: the
	// stale blob is purged, then the new source is cached in place.
	a, _ = s.store.Authors.Create(u.ID, "Metru", upstreamB.URL, "")
	doForm(h, "POST", "/authors/"+itoa(a.ID)+"/avatar-refresh", url.Values{}, cookie)
	doForm(h, "POST", "/authors/"+itoa(a.ID)+"/edit", url.Values{
		"name":       {"Metru"},
		"avatar_url": {upstreamC.URL + "/new.png"},
	}, cookie)
	st, _ := s.files.Stat(context.Background())
	if st.Objects != 1 {
		t.Fatalf("objects after url change = %d, want 1", st.Objects)
	}
	got := doGet(h, "/authors/"+itoa(a.ID)+"/avatar", cookie)
	if string(got.Body.Bytes()) != string(newImg) {
		t.Fatalf("avatar not replaced after url change: %x", got.Body.Bytes())
	}
}
