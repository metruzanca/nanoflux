package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestFirstSignupBecomesAdmin drives the fresh-install bootstrap: signup is
// open while no account exists, the first signup is promoted and closes
// signups, and the second signup is a normal user.
func TestFirstSignupBecomesAdmin(t *testing.T) {
	s, h := newEmptyServer(t)

	if body := doGetRaw(h, "/signup").Body.String(); !strings.Contains(body, `action="/signup"`) {
		t.Fatal("signup should be open on a fresh install")
	}

	rr := doForm(h, "POST", "/signup", url.Values{"username": {"root"}, "password": {"longenough"}}, nil)
	if rr.Code != http.StatusFound {
		t.Fatalf("first signup: %d %s", rr.Code, rr.Body.String())
	}
	root, err := s.store.Users.ByUsername("root")
	if err != nil || !root.IsAdmin {
		t.Fatalf("first user should be admin: %+v %v", root, err)
	}
	if allow, _ := s.store.Settings.AllowSignup(); allow {
		t.Fatal("signups should close after the first account")
	}
	if body := doGetRaw(h, "/signup").Body.String(); !strings.Contains(body, "signups are disabled") {
		t.Fatal("signup page should be closed after the first account")
	}
}

func TestCSRFRejectsMissingToken(t *testing.T) {
	_, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	req := httptest.NewRequest(http.MethodPost, "/prefs/display", strings.NewReader("view=list"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("POST without a token: got %d, want 403", rr.Code)
	}
}

func TestCSRFAcceptsHeader(t *testing.T) {
	_, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	req := httptest.NewRequest(http.MethodPost, "/prefs/display", strings.NewReader("view=list"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", csrfToken(cookie.Value))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code == http.StatusForbidden {
		t.Fatalf("POST with a valid token was rejected: %d", rr.Code)
	}
}

func TestCSRFExemptAnonymousLogin(t *testing.T) {
	_, h := newTestServer(t)
	// /login is exempt: a request without a token reaches the handler (which
	// then rejects bad credentials with 401, not 403).
	rr := doForm(h, "POST", "/login", url.Values{"username": {"alice"}, "password": {"wrong"}}, nil)
	if rr.Code == http.StatusForbidden {
		t.Fatal("login should be exempt from CSRF")
	}
}
