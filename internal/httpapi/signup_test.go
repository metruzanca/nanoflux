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
	rr := doGet(h, "/signup", nil)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "create account") {
		t.Fatalf("signup page: %d", rr.Code)
	}
	// The form carries the hidden timezone field app.js fills from the browser.
	if !strings.Contains(rr.Body.String(), `name="timezone"`) {
		t.Fatalf("signup page missing the timezone field: %s", rr.Body.String())
	}

	// Too-short password rejected.
	if rr := doForm(h, "POST", "/signup", url.Values{
		"username": {"bob"}, "password": {"short"},
	}, nil); rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "8 characters") {
		t.Fatalf("short password: %d", rr.Code)
	}

	// Valid signup creates a user, logs in, and redirects home.
	rr = doForm(h, "POST", "/signup", url.Values{
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

// TestSignupTimezone asserts the browser-submitted timezone seeds the new
// account, and an unknown one is ignored rather than rejecting the signup.
func TestSignupTimezone(t *testing.T) {
	s, h := newTestServer(t)

	if rr := doForm(h, "POST", "/signup", url.Values{
		"username": {"bob"}, "password": {"longenough"}, "timezone": {"America/New_York"},
	}, nil); rr.Code != http.StatusFound {
		t.Fatalf("signup: %d %s", rr.Code, rr.Body.String())
	}
	bob, err := s.store.Users.ByUsername("bob")
	if err != nil {
		t.Fatalf("bob not created: %v", err)
	}
	if bob.Timezone != "America/New_York" {
		t.Fatalf("timezone = %q, want America/New_York", bob.Timezone)
	}

	// An unknown timezone must not break signup; the account keeps the default.
	if rr := doForm(h, "POST", "/signup", url.Values{
		"username": {"carol"}, "password": {"longenough"}, "timezone": {"Mars/Olympus"},
	}, nil); rr.Code != http.StatusFound {
		t.Fatalf("signup with bad tz: %d %s", rr.Code, rr.Body.String())
	}
	carol, _ := s.store.Users.ByUsername("carol")
	if carol.Timezone != "" {
		t.Fatalf("invalid timezone should be ignored, got %q", carol.Timezone)
	}
}
