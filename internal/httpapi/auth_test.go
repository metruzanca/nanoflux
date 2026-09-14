package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/metruzanca/rss/internal/auth"
	"github.com/metruzanca/rss/internal/config"
	"github.com/metruzanca/rss/internal/db"
	"github.com/metruzanca/rss/internal/store"
)

func newTestServer(t *testing.T) (*Server, http.Handler) {
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
	hash, _ := auth.HashPassword("secret")
	if _, err := st.Users.Create("alice", hash); err != nil {
		t.Fatal(err)
	}
	a := auth.New(st)
	s := New(st, a, config.Config{})
	return s, s.Handler()
}

func login(t *testing.T, h http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"username": {"alice"}, "password": {"secret"}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestLoginFlow(t *testing.T) {
	_, h := newTestServer(t)

	if rr := login(t, h); rr.Code != http.StatusFound {
		t.Fatalf("login: got %d, want 302", rr.Code)
	}

	cookies := login(t, h).Result().Cookies()
	var sess *http.Cookie
	for _, c := range cookies {
		if c.Name == auth.SessionCookieName {
			sess = c
		}
	}
	if sess == nil {
		t.Fatal("no session cookie set")
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(sess)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET / with session: got %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "hi, alice") {
		t.Fatalf("home page missing username: %s", rr.Body.String())
	}
}

func TestLoginRejectsBadPassword(t *testing.T) {
	_, h := newTestServer(t)
	form := url.Values{"username": {"alice"}, "password": {"wrong"}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("bad login: got %d, want 401", rr.Code)
	}
}

func TestUnauthenticatedRedirects(t *testing.T) {
	_, h := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusFound || rr.Header().Get("Location") != "/login" {
		t.Fatalf("unauthenticated GET /: got %d %q", rr.Code, rr.Header().Get("Location"))
	}
}

func TestLogout(t *testing.T) {
	s, h := newTestServer(t)
	rr := login(t, h)
	sess := rr.Result().Cookies()
	var token string
	for _, c := range sess {
		if c.Name == auth.SessionCookieName {
			token = c.Value
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, req)
	if rr2.Code != http.StatusFound || rr2.Header().Get("Location") != "/login" {
		t.Fatalf("logout: got %d %q", rr2.Code, rr2.Header().Get("Location"))
	}

	// Session is gone from the DB.
	if _, err := s.store.Sessions.UserByToken(token); err == nil {
		t.Fatal("session still valid after logout")
	}
}

func TestBearerTokenAuth(t *testing.T) {
	_, h := newTestServer(t)
	rr := login(t, h)
	var token string
	for _, c := range rr.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			token = c.Value
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, req)
	if rr2.Code != http.StatusOK {
		t.Fatalf("bearer auth: got %d, want 200", rr2.Code)
	}
}
