package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/metruzanca/nanoflux/internal/auth"
	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/store"
)

// adminSession creates a far-future session for an existing user and returns
// the cookie to attach to requests.
func adminSession(t *testing.T, s *Server, username string) *http.Cookie {
	t.Helper()
	u, err := s.store.Users.ByUsername(username)
	if err != nil {
		t.Fatal(err)
	}
	token := "admintoken-" + username
	if err := s.store.Sessions.Create(u.ID, token, db.FormatTime(time.Now().Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: auth.SessionCookieName, Value: token}
}

func createUser(t *testing.T, s *Server, username string) store.User {
	t.Helper()
	hash, _ := auth.HashPassword("secret123")
	u, err := s.store.Users.Create(username, hash)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestAdminRequiresAdmin(t *testing.T) {
	s, h := newTestServer(t)
	root := createUser(t, s, "root")
	if err := s.store.Users.SetAdmin(root.ID, true); err != nil {
		t.Fatal(err)
	}
	cookie := adminSession(t, s, "alice")
	rr := doGet(h, "/admin", cookie)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("non-admin GET /admin: got %d, want 403", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "forbidden") {
		t.Fatalf("403 should render a visible page: %s", rr.Body.String())
	}
}

func TestAdminPage(t *testing.T) {
	s, h := newTestServer(t)
	root := createUser(t, s, "root")
	if err := s.store.Users.SetAdmin(root.ID, true); err != nil {
		t.Fatal(err)
	}
	createUser(t, s, "bob")

	cookie := adminSession(t, s, "root")
	rr := doGet(h, "/admin", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("admin GET /admin: got %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{"admin", "root", "bob", "alice", "you"} {
		if !strings.Contains(body, want) {
			t.Fatalf("admin page missing %q", want)
		}
	}
}

func TestAdminTopbarShowsAdminLink(t *testing.T) {
	s, h := newTestServer(t)
	root := createUser(t, s, "root")
	if err := s.store.Users.SetAdmin(root.ID, true); err != nil {
		t.Fatal(err)
	}
	rr := doGet(h, "/", adminSession(t, s, "root"))
	if !strings.Contains(rr.Body.String(), `href="/admin"`) {
		t.Fatal("topbar should link to /admin for admins")
	}
}

func TestAdminResetPassword(t *testing.T) {
	s, h := newTestServer(t)
	root := createUser(t, s, "root")
	if err := s.store.Users.SetAdmin(root.ID, true); err != nil {
		t.Fatal(err)
	}
	bob := createUser(t, s, "bob")
	token := "bob-session"
	if err := s.store.Sessions.Create(bob.ID, token, db.FormatTime(time.Now().Add(time.Hour))); err != nil {
		t.Fatal(err)
	}

	rr := doForm(h, "POST", "/admin/users/"+itoa(bob.ID)+"/reset-password",
		url.Values{"password": {"newpass123"}}, adminSession(t, s, "root"))
	if rr.Code != http.StatusOK {
		t.Fatalf("reset-password: got %d, want 200 (body %s)", rr.Code, rr.Body.String())
	}
	if _, err := s.store.Sessions.UserByToken(token); err == nil {
		t.Fatal("bob's session should be revoked after password reset")
	}
	got, err := s.store.Users.ByID(bob.ID)
	if err != nil || !auth.CheckPassword(got.PasswordHash, "newpass123") {
		t.Fatalf("password not reset: %+v err=%v", got, err)
	}
}

func TestAdminResetPasswordErrors(t *testing.T) {
	s, h := newTestServer(t)
	root := createUser(t, s, "root")
	if err := s.store.Users.SetAdmin(root.ID, true); err != nil {
		t.Fatal(err)
	}
	bob := createUser(t, s, "bob")
	cookie := adminSession(t, s, "root")

	rr := doForm(h, "POST", "/admin/users/"+itoa(bob.ID)+"/reset-password",
		url.Values{"password": {"short"}}, cookie)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "8 characters") {
		t.Fatalf("short password: got %d %s", rr.Code, rr.Body.String())
	}
}

func TestAdminSetAdmin(t *testing.T) {
	s, h := newTestServer(t)
	root := createUser(t, s, "root")
	if err := s.store.Users.SetAdmin(root.ID, true); err != nil {
		t.Fatal(err)
	}
	bob := createUser(t, s, "bob")
	cookie := adminSession(t, s, "root")

	rr := doForm(h, "POST", "/admin/users/"+itoa(bob.ID)+"/set-admin",
		url.Values{"admin": {"true"}}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("set-admin true: got %d (body %s)", rr.Code, rr.Body.String())
	}
	got, err := s.store.Users.ByID(bob.ID)
	if err != nil || !got.IsAdmin {
		t.Fatalf("bob should be admin: %+v err=%v", got, err)
	}
}

func TestAdminSetAdminLastAdminGuard(t *testing.T) {
	s, h := newTestServer(t)
	root := createUser(t, s, "root")
	if err := s.store.Users.SetAdmin(root.ID, true); err != nil {
		t.Fatal(err)
	}
	rr := doForm(h, "POST", "/admin/users/"+itoa(root.ID)+"/set-admin",
		url.Values{"admin": {"false"}}, adminSession(t, s, "root"))
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "last admin") {
		t.Fatalf("demote last admin: got %d %s", rr.Code, rr.Body.String())
	}
}

func TestAdminDeleteUser(t *testing.T) {
	s, h := newTestServer(t)
	root := createUser(t, s, "root")
	if err := s.store.Users.SetAdmin(root.ID, true); err != nil {
		t.Fatal(err)
	}
	bob := createUser(t, s, "bob")
	if err := s.files.Put(context.Background(), "avatars/"+itoa(bob.ID), "image/png", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := s.store.Users.SetAvatarKey(bob.ID, "avatars/"+itoa(bob.ID)); err != nil {
		t.Fatal(err)
	}

	rr := doForm(h, "POST", "/admin/users/"+itoa(bob.ID)+"/delete", url.Values{}, adminSession(t, s, "root"))
	if rr.Code != http.StatusOK {
		t.Fatalf("delete user: got %d (body %s)", rr.Code, rr.Body.String())
	}
	if _, err := s.store.Users.ByID(bob.ID); err == nil {
		t.Fatal("bob still present after delete")
	}
	if _, _, err := s.files.Get(context.Background(), "avatars/"+itoa(bob.ID)); err == nil {
		t.Fatal("bob's avatar object not purged")
	}
}

func TestAdminDeleteSelfGuard(t *testing.T) {
	s, h := newTestServer(t)
	root := createUser(t, s, "root")
	if err := s.store.Users.SetAdmin(root.ID, true); err != nil {
		t.Fatal(err)
	}
	cookie := adminSession(t, s, "root")

	rr := doForm(h, "POST", "/admin/users/"+itoa(root.ID)+"/delete", url.Values{}, cookie)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "own account") {
		t.Fatalf("delete self: got %d %s", rr.Code, rr.Body.String())
	}
}
