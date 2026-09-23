package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/metruzanca/nanoflux/internal/auth"
	"github.com/metruzanca/nanoflux/internal/backup"
	"github.com/metruzanca/nanoflux/internal/config"
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

func TestAdminBackupCard(t *testing.T) {
	s, h := newTestServer(t)
	root := createUser(t, s, "root")
	if err := s.store.Users.SetAdmin(root.ID, true); err != nil {
		t.Fatal(err)
	}
	cookie := adminSession(t, s, "root")

	// Disabled by default: the card explains how to turn backups on and offers
	// no button.
	body := doGet(h, "/admin", cookie).Body.String()
	if !strings.Contains(body, "automatic backups are off") {
		t.Fatalf("admin page should show backups are off: %s", body)
	}
	if strings.Contains(body, `hx-post="/admin/backup"`) {
		t.Fatal("no back-up-now button when backups are disabled")
	}

	// Enabled with a local destination: the card shows the destination and a
	// button, and a manual run writes an archive.
	dir := t.TempDir()
	s.cfg.Backup = config.BackupConfig{Interval: 24 * time.Hour, Keep: 7, Dir: dir}
	s.SetBackupRunner(backup.NewRunner(s.store.DB(), backup.Config{Interval: 24 * time.Hour, Keep: 7}, &backup.LocalDestination{Dir: dir}))

	body = doGet(h, "/admin", cookie).Body.String()
	if !strings.Contains(body, "local: "+dir) || !strings.Contains(body, `hx-post="/admin/backup"`) {
		t.Fatalf("enabled backup card malformed: %s", body)
	}

	rr := doForm(h, "POST", "/admin/backup", url.Values{}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("back up now: %d %s", rr.Code, rr.Body.String())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("expected one archive written, got %d", len(entries))
	}
	if !strings.Contains(rr.Body.String(), "nanoflux-") {
		t.Fatalf("card should show the last archive: %s", rr.Body.String())
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

func TestAdminSignupBannerAndDismiss(t *testing.T) {
	s, h := newTestServer(t)
	root := createUser(t, s, "root")
	if err := s.store.Users.SetAdmin(root.ID, true); err != nil {
		t.Fatal(err)
	}
	cookie := adminSession(t, s, "root")

	body := doGet(h, "/admin", cookie).Body.String()
	if !strings.Contains(body, "signups are open to anyone") {
		t.Fatal("banner should show while signups are on")
	}

	rr := doForm(h, "POST", "/admin/settings/signup-banner-dismiss", url.Values{}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("dismiss banner: got %d (body %s)", rr.Code, rr.Body.String())
	}
	body = doGet(h, "/admin", cookie).Body.String()
	if strings.Contains(body, "signups are open to anyone") {
		t.Fatal("banner should be gone after dismiss")
	}
}

func TestAdminSetSignup(t *testing.T) {
	s, h := newTestServer(t)
	root := createUser(t, s, "root")
	if err := s.store.Users.SetAdmin(root.ID, true); err != nil {
		t.Fatal(err)
	}
	cookie := adminSession(t, s, "root")

	rr := doForm(h, "POST", "/admin/settings/signup", url.Values{"allow": {"false"}}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("disable signups: got %d (body %s)", rr.Code, rr.Body.String())
	}
	allow, err := s.store.Settings.AllowSignup()
	if err != nil || allow {
		t.Fatalf("allow_signup = %v, %v; want false", allow, err)
	}
	// Banner must not show once signups are disabled.
	if strings.Contains(doGet(h, "/admin", cookie).Body.String(), "signups are open") {
		t.Fatal("banner should not show when signups are disabled")
	}
}

func TestAdminStats(t *testing.T) {
	s, h := newTestServer(t)
	root := createUser(t, s, "root")
	if err := s.store.Users.SetAdmin(root.ID, true); err != nil {
		t.Fatal(err)
	}
	// Seed an object so file-storage stats show up.
	if err := s.files.Put(context.Background(), "avatars/2", "image/png", []byte("0123456789")); err != nil {
		t.Fatal(err)
	}
	body := doGet(h, "/admin", adminSession(t, s, "root")).Body.String()
	for _, want := range []string{"users", "feeds", "authors", "items", "unread", "objects", "storage", "10 B"} {
		if !strings.Contains(body, want) {
			t.Fatalf("admin stats missing %q", want)
		}
	}
}

func TestSignupGating(t *testing.T) {
	s, h := newTestServer(t)

	// Default: signup open, login links to it.
	if body := doGetRaw(h, "/signup").Body.String(); !strings.Contains(body, "create account") {
		t.Fatal("signup page should render by default")
	}
	if body := doGetRaw(h, "/login").Body.String(); !strings.Contains(body, `href="/signup"`) {
		t.Fatal("login should link to signup while open")
	}

	if err := s.store.Settings.SetAllowSignup(false); err != nil {
		t.Fatal(err)
	}
	body := doGetRaw(h, "/signup").Body.String()
	if !strings.Contains(body, "signups are disabled") {
		t.Fatal("signup page should show disabled message")
	}
	body = doGetRaw(h, "/login").Body.String()
	if strings.Contains(body, `href="/signup"`) {
		t.Fatal("login must not link to signup when disabled")
	}
	rr := doForm(h, "POST", "/signup", url.Values{"username": {"eve"}, "password": {"longenough"}}, nil)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("signup when disabled: got %d, want 403", rr.Code)
	}
}
