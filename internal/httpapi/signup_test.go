package httpapi

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestSignupFlow(t *testing.T) {
	s, h := newTestServer(t)

	// Signup page renders.
	if rr := doGet(h, "/signup", nil); rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "create account") {
		t.Fatalf("signup page: %d", rr.Code)
	}

	// Too-short password rejected.
	if rr := doForm(h, "POST", "/signup", url.Values{
		"username": {"bob"}, "password": {"short"},
	}, nil); rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "8 characters") {
		t.Fatalf("short password: %d", rr.Code)
	}

	// Valid signup creates a user, logs in, and redirects home.
	rr := doForm(h, "POST", "/signup", url.Values{
		"username": {"bob"}, "password": {"longenough"},
	}, nil)
	if rr.Code != http.StatusFound {
		t.Fatalf("signup: %d %s", rr.Code, rr.Body.String())
	}
	var cookie *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == "rss_session" {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no session cookie after signup")
	}

	// Session works: authenticated request passes and new user has data.
	if rr := doGet(h, "/", cookie); rr.Code != http.StatusOK {
		t.Fatalf("GET / after signup: %d", rr.Code)
	}
	if _, err := s.store.Users.ByUsername("bob"); err != nil {
		t.Fatalf("bob not created: %v", err)
	}

	// Duplicate username rejected.
	if rr := doForm(h, "POST", "/signup", url.Values{
		"username": {"bob"}, "password": {"longenough"},
	}, nil); rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "taken") {
		t.Fatalf("duplicate signup: %d", rr.Code)
	}

	// Logged-in user is redirected away from signup.
	if rr := doGet(h, "/signup", cookie); rr.Code != http.StatusFound {
		t.Fatalf("signup while logged in: %d", rr.Code)
	}
}

func TestSignupDoesNotClobberBootstrapUser(t *testing.T) {
	_, h := newTestServer(t)
	if rr := doForm(h, "POST", "/signup", url.Values{
		"username": {"alice"}, "password": {"longenough"},
	}, nil); rr.Code != http.StatusBadRequest {
		t.Fatalf("signup with existing username: %d", rr.Code)
	}
}
