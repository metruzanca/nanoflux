package cli

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/metruzanca/nanoflux/internal/auth"
	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/filestore"
	"github.com/metruzanca/nanoflux/internal/store"
)

type cliHarness struct {
	st     *store.Store
	sqldb  *sql.DB
	env    Env
	files  *filestore.Memory
	stdout *bytes.Buffer
	stderr *bytes.Buffer
	root   *cobra.Command
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
	env := Env{DBPath: ":memory:", FileStoreDir: "", Files: func() (filestore.Store, error) { return mem, nil }}
	return &cliHarness{
		st:     st,
		sqldb:  sqldb,
		env:    env,
		files:  mem,
		stdout: stdout,
		stderr: stderr,
		root:   New("dev", st, env, stdout, stderr),
	}
}

// newCLIAt drives the CLI against a real file database and disk filestore, for
// backup/restore tests. The harness's close() releases the DB handle so file
// operations can proceed.
func newCLIAt(t *testing.T, dbPath, fileStoreDir string) *cliHarness {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	sqldb, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(sqldb); err != nil {
		sqldb.Close()
		t.Fatal(err)
	}
	st := store.New(sqldb)
	mem := filestore.NewMemory()
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	env := Env{DBPath: dbPath, FileStoreDir: fileStoreDir, Files: func() (filestore.Store, error) { return mem, nil }}
	h := &cliHarness{
		st:     st,
		sqldb:  sqldb,
		env:    env,
		files:  mem,
		stdout: stdout,
		stderr: stderr,
		root:   New("dev", st, env, stdout, stderr),
	}
	t.Cleanup(func() { sqldb.Close() })
	return h
}

func (h *cliHarness) exec(t *testing.T, args ...string) error {
	t.Helper()
	h.root.SetArgs(args)
	h.stdout.Reset()
	h.stderr.Reset()
	return h.root.Execute()
}

// restoreRoot returns a command tree whose only job is to run `restore`
// against the file DB; it never opens that DB itself.
func restoreRoot(t *testing.T, dbPath, fileStoreDir string) *cobra.Command {
	t.Helper()
	sqldb, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqldb.Close() })
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	env := Env{DBPath: dbPath, FileStoreDir: fileStoreDir, Files: nil}
	return New("dev", store.New(sqldb), env, stdout, stderr)
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

func TestUserCreate(t *testing.T) {
	h := newCLI(t)
	if err := h.exec(t, "user", "create", "bob", "--password", "secret123"); err != nil {
		t.Fatal(err)
	}
	got, err := h.st.Users.ByUsername("bob")
	if err != nil {
		t.Fatal(err)
	}
	if got.IsAdmin {
		t.Fatal("created user should not be admin by default")
	}
	if !strings.Contains(h.stdout.String(), "created user \"bob\"") {
		t.Fatalf("unexpected output: %q", h.stdout.String())
	}
}

func TestUserCreateAdmin(t *testing.T) {
	h := newCLI(t)
	if err := h.exec(t, "user", "create", "root", "--password", "secret123", "--admin"); err != nil {
		t.Fatal(err)
	}
	got, err := h.st.Users.ByUsername("root")
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsAdmin {
		t.Fatal("--admin should grant the admin flag")
	}
}

func TestUserCreateErrors(t *testing.T) {
	h := newCLI(t)
	createUser(t, h.st, "alice")
	if err := h.exec(t, "user", "create", "alice", "--password", "secret123"); err == nil {
		t.Fatal("expected error for existing user")
	}
	if err := h.exec(t, "user", "create", "bob", "--password", "short"); err == nil {
		t.Fatal("expected error for short password")
	}
}

func TestFeedList(t *testing.T) {
	h := newCLI(t)
	u := createUser(t, h.st, "alice")
	if _, err := h.st.Feeds.Create(u.ID, 0, "Example Blog", "https://example.com/feed.xml", "https://example.com", "nsfw description here", 900); err != nil {
		t.Fatal(err)
	}

	if err := h.exec(t, "feed", "list"); err != nil {
		t.Fatal(err)
	}
	out := h.stdout.String()
	for _, want := range []string{"alice", "Example Blog"} {
		if !strings.Contains(out, want) {
			t.Fatalf("feed list missing %q: %q", want, out)
		}
	}
	if strings.Contains(out, "nsfw description") {
		t.Fatal("feed list must not show feed descriptions/content")
	}
}

func TestFeedListMarksErrors(t *testing.T) {
	h := newCLI(t)
	u := createUser(t, h.st, "alice")
	f, err := h.st.Feeds.Create(u.ID, 0, "Broken", "https://broken.dev/feed.xml", "", "", 900)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.Feeds.SetPollMeta(f.ID, "", "", db.Now(), "timeout"); err != nil {
		t.Fatal(err)
	}
	if err := h.exec(t, "feed", "list"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.stdout.String(), "error") {
		t.Fatalf("feed list should mark the errored feed: %q", h.stdout.String())
	}
}

func TestBackupRestoreRoundtrip(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "data", "rss.db")
	fsDir := filepath.Join(dir, "filestore")

	h := newCLIAt(t, dbPath, fsDir)
	u := createUser(t, h.st, "alice")
	if _, err := h.st.Feeds.Create(u.ID, 0, "Example", "https://example.com/feed.xml", "", "", 900); err != nil {
		t.Fatal(err)
	}
	blob := filepath.Join(fsDir, "avatars", "1")
	if err := os.MkdirAll(filepath.Dir(blob), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blob, []byte("avatar"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.st.Users.SetAvatarKey(u.ID, "avatars/1"); err != nil {
		t.Fatal(err)
	}

	if err := h.exec(t, "backup", "--out", dir); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.stdout.String(), "backup written to") {
		t.Fatalf("unexpected backup output: %q", h.stdout.String())
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "nanoflux-*.tar.gz"))
	if len(matches) != 1 {
		t.Fatalf("expected one archive, got %v", matches)
	}
	archive := matches[0]

	// Release the DB handle, then wipe everything to prove restore rebuilds it.
	h.sqldb.Close()
	os.Remove(dbPath)
	os.RemoveAll(fsDir)

	root := restoreRoot(t, dbPath, fsDir)
	root.SetArgs([]string{"restore", archive})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}

	sqldb, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer sqldb.Close()
	if err := db.Migrate(sqldb); err != nil {
		t.Fatal(err)
	}
	st := store.New(sqldb)
	if _, err := st.Users.ByUsername("alice"); err != nil {
		t.Fatalf("alice missing after restore: %v", err)
	}
	restored, err := os.ReadFile(filepath.Join(fsDir, "avatars", "1"))
	if err != nil || string(restored) != "avatar" {
		t.Fatalf("filestore not restored: %q err=%v", restored, err)
	}
}

func TestBackupRejectsUnreadable(t *testing.T) {
	h := newCLI(t)
	if err := h.exec(t, "restore", "/nonexistent/backup.tar.gz"); err == nil {
		t.Fatal("expected error for missing archive")
	}
}
