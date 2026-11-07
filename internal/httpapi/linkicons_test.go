package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestLinkIconSlugs(t *testing.T) {
	cases := []struct {
		domain string
		want   []string
	}{
		{"reddit.com", []string{"reddit"}},
		{"www.reddit.com", []string{"reddit"}},
		{"old.reddit.com", []string{"old", "reddit"}},
		{"news.ycombinator.com", []string{"ycombinator"}},
		{"github.com", []string{"github"}},
		{"twitter.com", []string{"x"}},
		{"www.twitter.com", []string{"x"}},
		{"youtu.be", []string{"youtube"}},
		{"bsky.app", []string{"bluesky"}},
		{"nixos", []string{"nixos"}},
		{"", nil},
	}
	for _, c := range cases {
		got := linkIconSlugs(c.domain)
		if len(got) != len(c.want) {
			t.Errorf("linkIconSlugs(%q) = %v, want %v", c.domain, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("linkIconSlugs(%q) = %v, want %v", c.domain, got, c.want)
				break
			}
		}
	}
}

func TestParseLinkIconSVG(t *testing.T) {
	paths, err := parseLinkIconSVG([]byte(`<svg role="img" viewBox="0 0 24 24"><title>Reddit</title><path d="M12 0C5.373 0 0 5.373 0 12Z"/></svg>`))
	if err != nil || len(paths) != 1 || !strings.HasPrefix(paths[0], "M12 0") {
		t.Fatalf("valid svg: %v %v", paths, err)
	}

	// Only path data is kept; active elements reject the whole document.
	if _, err := parseLinkIconSVG([]byte(`<svg><script>alert(1)</script><path d="M0 0"/></svg>`)); err == nil {
		t.Fatal("script element should be rejected")
	}
	if _, err := parseLinkIconSVG([]byte(`<svg><foreignObject><path d="M0 0"/></foreignObject></svg>`)); err == nil {
		t.Fatal("foreignObject should be rejected")
	}
	if _, err := parseLinkIconSVG([]byte(`<svg><path d="M0 0&quot; onload=&quot;x"/></svg>`)); err == nil {
		t.Fatal("path data with markup characters should be rejected")
	}
	if _, err := parseLinkIconSVG([]byte(`<svg><title>empty</title></svg>`)); err == nil {
		t.Fatal("svg without path data should be rejected")
	}
	if _, err := parseLinkIconSVG([]byte(`<html><path d="M0 0"/></html>`)); err == nil {
		t.Fatal("non-svg root should be rejected")
	}
}

func TestAuthorLinkIcons(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")

	var hits int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		switch r.URL.Path {
		case "/icons/reddit.svg":
			w.Header().Set("Content-Type", "image/svg+xml")
			io.WriteString(w, `<svg viewBox="0 0 24 24"><title>Reddit</title><path d="M12 0L1 1"/></svg>`)
		case "/icons/twitch.svg":
			w.Header().Set("Content-Type", "image/svg+xml")
			io.WriteString(w, `<svg viewBox="0 0 24 24"><title>Twitch</title><path d="M2 2L3 3"/></svg>`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fake.Close)
	s.client = fake.Client()
	s.linkIconBase = fake.URL + "/icons/"

	s.store.AuthorLinks.Create(u.ID, a.ID, "Twitch", "https://twitch.tv/example")
	s.store.AuthorLinks.Create(u.ID, a.ID, "", "https://www.reddit.com/r/golang")
	s.store.AuthorLinks.Create(u.ID, a.ID, "", "https://example.com/page")

	page := doGet(h, "/authors/"+itoa(a.ID), cookie).Body.String()
	if !strings.Contains(page, `class="link-icon"`) {
		t.Fatalf("author page should render inline link icons: %s", page)
	}
	// A labeled link keeps its name and gets the icon before it.
	if !strings.Contains(page, `class="link-label">Twitch</span></a>`) {
		t.Fatalf("labeled link should keep its label: %s", page)
	}
	// An unlabeled link with an icon is icon-only, named by its domain.
	if !strings.Contains(page, `aria-label="reddit.com"`) || strings.Contains(page, ">reddit.com</a>") {
		t.Fatalf("unlabeled icon link should not repeat the domain: %s", page)
	}
	// A confirmed miss keeps the domain text fallback.
	if !strings.Contains(page, ">example.com</a>") {
		t.Fatalf("unlabeled link without an icon should fall back to the domain: %s", page)
	}

	// Hits and misses are both cached.
	ic, err := s.store.LinkIcons.Get("reddit.com")
	if err != nil || !ic.Found || ic.Slug != "reddit" {
		t.Fatalf("reddit cache row = %+v, %v", ic, err)
	}
	ic, err = s.store.LinkIcons.Get("example.com")
	if err != nil || ic.Found {
		t.Fatalf("example cache row = %+v, %v", ic, err)
	}

	// A second render reads the cache and makes no outbound requests.
	before := atomic.LoadInt32(&hits)
	doGet(h, "/authors/"+itoa(a.ID), cookie)
	if after := atomic.LoadInt32(&hits); after != before {
		t.Fatalf("cached render made %d new requests", after-before)
	}
}

func TestAdminLinkIcons(t *testing.T) {
	s, h := newTestServer(t)
	root := createUser(t, s, "root")
	if err := s.store.Users.SetAdmin(root.ID, true); err != nil {
		t.Fatal(err)
	}
	cookie := adminSession(t, s, "root")

	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/icons/reddit.svg" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/svg+xml")
		io.WriteString(w, `<svg viewBox="0 0 24 24"><path d="M12 0L1 1"/></svg>`)
	}))
	t.Cleanup(fake.Close)
	s.client = fake.Client()
	s.linkIconBase = fake.URL + "/icons/"

	rr := doForm(h, "POST", "/admin/link-icons", url.Values{
		"domain": {"www.reddit.com"},
		"slug":   {"reddit"},
	}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("save mapping: %d %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "reddit.com") || !strings.Contains(body, `class="link-icon-lg"`) {
		t.Fatalf("admin card should show the saved association: %s", body)
	}

	// The main page renders the card too.
	page := doGet(h, "/admin", cookie).Body.String()
	if !strings.Contains(page, `id="admin-link-icons-card"`) || !strings.Contains(page, "reddit.com") {
		t.Fatalf("admin page missing link icons card: %s", page)
	}

	// Recheck keeps the manual slug.
	rr = doForm(h, "POST", "/admin/link-icons/recheck", url.Values{"domain": {"reddit.com"}}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("recheck: %d %s", rr.Code, rr.Body.String())
	}

	// A wrong manual slug is reported without replacing the saved row.
	rr = doForm(h, "POST", "/admin/link-icons", url.Values{
		"domain": {"reddit.com"},
		"slug":   {"not-a-real-icon"},
	}, cookie)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "no icon named") {
		t.Fatalf("bad manual slug: %d %s", rr.Code, rr.Body.String())
	}
	if ic, _ := s.store.LinkIcons.Get("reddit.com"); !ic.Found || ic.Slug != "reddit" {
		t.Fatalf("bad override should not clobber the row: %+v", ic)
	}

	// Delete forgets the association.
	rr = doForm(h, "POST", "/admin/link-icons/delete", url.Values{"domain": {"reddit.com"}}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rr.Code, rr.Body.String())
	}
	if _, err := s.store.LinkIcons.Get("reddit.com"); err == nil {
		t.Fatal("deleted association still present")
	}

	// Non-admins are rejected (admin POSTs answer with a 400 error fragment).
	alice := sessionCookie(t, h)
	rr = doForm(h, "POST", "/admin/link-icons", url.Values{
		"domain": {"github.com"},
		"slug":   {"github"},
	}, alice)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("non-admin should be rejected: %d", rr.Code)
	}
}
