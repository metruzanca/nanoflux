package auth

import (
	"net/http"
	"testing"
	"time"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/store"
)

func newAuthStore(t *testing.T) (*Authenticator, *store.Store) {
	t.Helper()
	sqldb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { sqldb.Close() })
	if err := db.Migrate(sqldb); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(sqldb)
	return New(st), st
}

func sessionExpiry(t *testing.T, st *store.Store, userID int64) string {
	t.Helper()
	ss, err := st.Sessions.ListUserSessions(userID)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(ss) == 0 {
		t.Fatal("no sessions")
	}
	return ss[0].ExpiresAt
}

// TestTouchThrottle verifies that a session's sliding expiry is only written
// once per interval, and written again when throttling is off.
func TestTouchThrottle(t *testing.T) {
	a, st := newAuthStore(t)
	u, err := st.Users.Create("bob", "x")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	tok, err := a.CreateSession(u.ID)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	req, _ := http.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: tok})

	a.SetTouchInterval(time.Hour)
	if _, err := a.User(req); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	// Freeze the expiry, then resolve again inside the window. The throttle
	// must leave the frozen value untouched.
	frozen := db.FormatTime(time.Now().Add(time.Hour))
	if err := st.Sessions.Touch(tok, frozen); err != nil {
		t.Fatalf("freeze: %v", err)
	}
	if _, err := a.User(req); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := sessionExpiry(t, st, u.ID); got != frozen {
		t.Fatalf("throttled resolve changed expiry: got %q want %q", got, frozen)
	}

	// With throttling off, the resolve slides it again.
	a.SetTouchInterval(0)
	if _, err := a.User(req); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := sessionExpiry(t, st, u.ID); got == frozen {
		t.Fatal("unthrottled resolve did not update expiry")
	}
}
