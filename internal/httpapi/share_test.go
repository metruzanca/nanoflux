package httpapi

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestShareAddPage(t *testing.T) {
	_, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	body := doGet(h, "/add?url="+url.QueryEscape("https://example.com/post"), cookie).Body.String()
	if !strings.Contains(body, `id="share-preview"`) ||
		!strings.Contains(body, `hx-post="/fragments/feed-preview"`) ||
		!strings.Contains(body, `value="https://example.com/post"`) {
		t.Fatalf("share page should trigger the preview flow: %s", body)
	}
}

func TestShareAddExtractsURLFromText(t *testing.T) {
	_, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	body := doGet(h, "/add?text="+url.QueryEscape("check this out https://example.com/x."), cookie).Body.String()
	if !strings.Contains(body, `value="https://example.com/x"`) {
		t.Fatalf("share page should extract the url from text: %s", body)
	}
}

func TestShareAddNoURLRedirects(t *testing.T) {
	_, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	rr := doGet(h, "/add", cookie)
	if rr.Code != http.StatusFound || rr.Header().Get("Location") != "/authors" {
		t.Fatalf("shapless /add: %d %q", rr.Code, rr.Header().Get("Location"))
	}
}

func TestShareAddLoginReturn(t *testing.T) {
	_, h := newTestServer(t)

	// Logged out: /add redirects to login carrying the return path.
	rr := doGetRaw(h, "/add?url="+url.QueryEscape("https://example.com/post"))
	if rr.Code != http.StatusFound || !strings.Contains(rr.Header().Get("Location"), "next=") {
		t.Fatalf("logged-out /add should redirect to login with next: %d %q", rr.Code, rr.Header().Get("Location"))
	}

	// Logging in with next returns there.
	next := "/add?url=" + url.QueryEscape("https://example.com/post")
	login := doForm(h, "POST", "/login", url.Values{
		"username": {"alice"}, "password": {"secret"}, "next": {next},
	}, nil)
	if login.Code != http.StatusFound || login.Header().Get("Location") != next {
		t.Fatalf("login should return to next: %d %q", login.Code, login.Header().Get("Location"))
	}
}

func TestSafeNext(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/add?url=x", "/add?url=x"},
		{"//evil.com", ""},
		{"https://evil.com", ""},
		{"/a\\b", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := safeNext(c.in); got != c.want {
			t.Errorf("safeNext(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFeedCreateRedirectHeader(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "", "")

	rr := doForm(h, "POST", "/feeds", url.Values{
		"title": {"Blog"}, "feed_url": {"https://b.dev/rss.xml"},
		"author_id": {itoa(a.ID)}, "redirect": {"1"},
	}, cookie)
	if rr.Code != http.StatusNoContent || rr.Header().Get("HX-Redirect") != "/authors/"+itoa(a.ID) {
		t.Fatalf("redirect add should HX-Redirect to the author page: %d %q", rr.Code, rr.Header().Get("HX-Redirect"))
	}
	feeds, _ := s.store.Feeds.List(u.ID)
	if len(feeds) != 1 {
		t.Fatalf("feed should be created: %+v", feeds)
	}
}
