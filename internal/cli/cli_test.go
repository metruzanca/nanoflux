package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/metruzanca/nanoflux/internal/auth"
	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/filestore"
	"github.com/metruzanca/nanoflux/internal/store"
)

type cliHarness struct {
	st     *store.Store
	files  *filestore.Memory
	stdout *bytes.Buffer
	stderr *bytes.Buffer
	exec   func(t *testing.T, args ...string) error
}

func newCLI(t *testing.T) *cliHarness {
	t.Helper()
	sqldb, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqldb.Close() })
	if err := db.Migrate(sqldb); err != nil {
		t.Fatal(err)
	}
	st := store.New(sqldb)
	mem := filestore.NewMemory()
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	root := New("dev", st, func() (filestore.Store, error) { return mem, nil }, stdout, stderr)
	return &cliHarness{
		st:     st,
		files:  mem,
		stdout: stdout,
		stderr: stderr,
		exec: func(t *testing.T, args ...string) error {
			t.Helper()
			root.SetArgs(args)
			stdout.Reset()
			stderr.Reset()
			return root.Execute()
		},
	}
}

func createUser(t *testing.T, st *store.Store, username string) store.User {
	t.Helper()
	hash, _ := auth.HashPassword("secret123")
	u, err := st.Users.Create(username, hash)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestUserList(t *testing.T) {
	h := newCLI(t)
	createUser(t, h.st, "bob")
	createUser(t, h.st, "alice")

	if err := h.exec(t, "user", "list"); err != nil {
		t.Fatal(err)
	}
	out := h.stdout.String()
	for _, want := range []string{"alice", "bob"} {
		if !strings.Contains(out, want) {
			t.Fatalf("list missing %q: %q", want, out)
		}
	}
	if strings.Contains(out, "secret123") {
		t.Fatal("list must not expose password hashes")
	}
}

func TestUserSetAdmin(t *testing.T) {
	h := newCLI(t)
	createUser(t, h.st, "alice")
	createUser(t, h.st, "bob")

	if err := h.exec(t, "user", "set-admin", "alice", "true"); err != nil {
		t.Fatal(err)
	}
	got, err := h.st.Users.ByUsername("alice")
	if err != nil || !got.IsAdmin {
		t.Fatalf("alice not admin after set-admin: %+v err=%v", got, err)
	}
	if !strings.Contains(h.stdout.String(), "alice is now an admin") {
		t.Fatalf("unexpected output: %q", h.stdout.String())
	}
}

func TestUserSetAdminLastAdminGuard(t *testing.T) {
	h := newCLI(t)
	alice := createUser(t, h.st, "alice")
	if err := h.st.Users.SetAdmin(alice.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := h.exec(t, "user", "set-admin", "alice", "false"); err == nil {
		t.Fatal("expected error demoting the last admin")
	}
}

func TestUserResetPassword(t *testing.T) {
	h := newCLI(t)
	u := createUser(t, h.st, "alice")
	token := "sessiontoken"
	if err := h.st.Sessions.Create(u.ID, token, db.Now()); err != nil {
		t.Fatal(err)
	}

	if err := h.exec(t, "user", "reset-password", "alice", "--password", "newpass123"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.Sessions.UserByToken(token); err == nil {
		t.Fatal("sessions should be logged out after a password reset")
	}
	got, err := h.st.Users.ByUsername("alice")
	if err != nil {
		t.Fatal(err)
	}
	if !auth.CheckPassword(got.PasswordHash, "newpass123") {
		t.Fatal("password hash not updated")
	}
}

func TestUserResetPasswordTooShort(t *testing.T) {
	h := newCLI(t)
	createUser(t, h.st, "alice")
	if err := h.exec(t, "user", "reset-password", "alice", "--password", "short"); err == nil {
		t.Fatal("expected error for short password")
	}
}

func TestUserResetPasswordMissingUser(t *testing.T) {
	h := newCLI(t)
	if err := h.exec(t, "user", "reset-password", "nobody", "--password", "newpass123"); err == nil {
		t.Fatal("expected error for unknown user")
	}
}

func TestUserDelete(t *testing.T) {
	h := newCLI(t)
	u := createUser(t, h.st, "alice")
	createUser(t, h.st, "bob")
	if err := h.files.Put(context.Background(), "avatars/1", "image/png", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := h.st.Users.SetAvatarKey(u.ID, "avatars/1"); err != nil {
		t.Fatal(err)
	}

	if err := h.exec(t, "user", "delete", "alice", "--yes"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.Users.ByUsername("alice"); err == nil {
		t.Fatal("alice still present after delete")
	}
	if _, _, err := h.files.Get(context.Background(), "avatars/1"); err == nil {
		t.Fatal("avatar object not purged")
	}
	if !strings.Contains(h.stdout.String(), "deleted user") {
		t.Fatalf("unexpected output: %q", h.stdout.String())
	}
}

func TestUserDeleteRequiresConfirmation(t *testing.T) {
	h := newCLI(t)
	createUser(t, h.st, "alice")
	createUser(t, h.st, "bob")
	// Non-interactive stdin must fail without --yes.
	if err := h.exec(t, "user", "delete", "bob"); err == nil {
		t.Fatal("expected confirmation error without --yes")
	}
	if !strings.Contains(h.stderr.String(), "--yes") {
		t.Fatalf("error should mention --yes: %q", h.stderr.String())
	}
}

func TestUserDeleteLastAdminGuard(t *testing.T) {
	h := newCLI(t)
	u := createUser(t, h.st, "alice")
	if err := h.st.Users.SetAdmin(u.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := h.exec(t, "user", "delete", "alice", "--yes"); err == nil {
		t.Fatal("expected error deleting the last admin")
	}
}

func TestVersion(t *testing.T) {
	h := newCLI(t)
	if err := h.exec(t, "version"); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(h.stdout.String()) != "dev" {
		t.Fatalf("unexpected version output: %q", h.stdout.String())
	}
}
