package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

func TestLandingPageAnonymous(t *testing.T) {
	_, h := newTestServer(t)
	rr := doGetRaw(h, "/")
	if rr.Code != http.StatusOK {
		t.Fatalf("landing: %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "mkt-hero") || !strings.Contains(body, "log in") {
		t.Fatalf("landing missing hero/login: %s", body)
	}
	if strings.Contains(body, "create an account") {
		t.Fatalf("closed signup should not offer account creation")
	}
}

func TestLandingOffersSignupOnFreshInstall(t *testing.T) {
	_, h := newEmptyServer(t)
	body := doGetRaw(h, "/").Body.String()
	if !strings.Contains(body, "create an account") {
		t.Fatalf("fresh install should offer account creation: %s", body)
	}
}
