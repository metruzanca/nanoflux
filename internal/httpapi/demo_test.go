package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/metruzanca/nanoflux/internal/auth"
	"github.com/metruzanca/nanoflux/internal/config"
	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/demo"
	"github.com/metruzanca/nanoflux/internal/filestore"
	"github.com/metruzanca/nanoflux/internal/store"
)

// newDemoServer builds a test server in demo mode: a seeded "demo" admin (one
// paused feed and one item) plus a demo.Manager wired in.
func newDemoServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	st, a, cfg, fs := newTestStoreServer(t)
	cfg.Demo = config.DemoConfig{Mode: true, User: "demo", TTL: time.Hour, MaxFeeds: 5}

	seed, err := st.Users.Create("demo", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Users.SetAdmin(seed.ID, true); err != nil {
		t.Fatal(err)
	}
	author, err := st.Authors.Create(seed.ID, "Blog", "", "")
	if err != nil {
		t.Fatal(err)
	}
	feed, err := st.Feeds.Create(seed.ID, author.ID, "Blog feed", "https://blog.example/rss.xml", "", "", 900)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Items.Upsert(feed.ID, store.Item{GUID: "p1", Title: "Hello", Link: "https://blog.example/p1", FetchedAt: db.Now()}); err != nil {
		t.Fatal(err)
	}
	// The seed's own feed stays enabled; only clones are paused.

	mgr := demo.New(st, fs, seed.ID, seed.Username, time.Hour, 5)
	a.SetDemoMode(true)
	s := New(st, a, cfg, fs)
	s.SetDemo(mgr)
	return s, s.Handler()
}

func newTestStoreServer(t *testing.T) (*store.Store, *auth.Authenticator, config.Config, filestore.Store) {
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
	return st, auth.New(st), config.Config{}, filestore.NewMemory()
}

// demoSessionCookie posts /demo and returns the session cookie it set.
func demoSessionCookie(t *testing.T, h http.Handler) *http.Cookie {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/demo", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusFound || rr.Header().Get("Location") != "/" {
		t.Fatalf("POST /demo: got %d %q", rr.Code, rr.Header().Get("Location"))
	}
	for _, c := range rr.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			return c
		}
	}
	t.Fatal("no session cookie from /demo")
	return nil
}

// ephemeralUserIDs returns the ids of the store's ephemeral users.
func ephemeralUserIDs(t *testing.T, st *store.Store) []int64 {
	t.Helper()
	rows, err := st.DB().Query(`SELECT id FROM users WHERE is_ephemeral = 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

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
	if strings.Contains(body, "try the demo") {
		t.Fatalf("non-demo install should not offer the demo CTA")
	}
}

func TestLandingShowsDemoCTA(t *testing.T) {
	_, h := newDemoServer(t)
	body := doGetRaw(h, "/").Body.String()
	if !strings.Contains(body, "try the demo") {
		t.Fatalf("demo install should offer the demo CTA: %s", body)
	}
}

func TestDemoStartProvisionsEphemeralUser(t *testing.T) {
	s, h := newDemoServer(t)
	cookie := demoSessionCookie(t, h)

	rr := doGet(h, "/", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("demo home: %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "demo-countdown") {
		t.Fatalf("demo home should show the countdown badge")
	}

	ids := ephemeralUserIDs(t, s.store)
	if len(ids) != 1 {
		t.Fatalf("expected exactly one ephemeral user, got %v", ids)
	}
	u, err := s.store.Users.ByID(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if u.IsAdmin || u.Username == "demo" {
		t.Fatalf("demo user shape wrong: %+v", u)
	}
	feeds, _ := s.store.Feeds.List(u.ID)
	if len(feeds) != 1 || feeds[0].Enabled {
		t.Fatalf("demo should have one paused feed: %+v", feeds)
	}
}

func TestDemoThrottleOnePerIP(t *testing.T) {
	s, h := newDemoServer(t)
	demoSessionCookie(t, h)

	// Second creation from the same IP (httptest's default RemoteAddr) is
	// refused with the busy notice.
	req := httptest.NewRequest(http.MethodPost, "/demo", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusFound || rr.Header().Get("Location") != "/?demo=busy" {
		t.Fatalf("second demo from same IP should be throttled: got %d %q", rr.Code, rr.Header().Get("Location"))
	}
	if ids := ephemeralUserIDs(t, s.store); len(ids) != 1 {
		t.Fatalf("throttle should prevent a second user, got %v", ids)
	}
}

func TestDemoBusyNotice(t *testing.T) {
	_, h := newDemoServer(t)
	rr := doGetRaw(h, "/?demo=busy")
	if !strings.Contains(rr.Body.String(), "a demo is already active from this network") {
		t.Fatalf("busy notice missing: %s", rr.Body.String())
	}
}

func TestDemoStartDisabled(t *testing.T) {
	_, h := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/demo", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("demo off should 404, got %d", rr.Code)
	}
}

func TestDemoSignupDisabled(t *testing.T) {
	_, h := newDemoServer(t)
	rr := doGetRaw(h, "/signup")
	if rr.Code != http.StatusOK {
		t.Fatalf("signup page: %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "disabled") {
		t.Fatalf("signup should be disabled in demo mode: %s", rr.Body.String())
	}
}

func TestExpiredEphemeralSessionRejected(t *testing.T) {
	s, _ := newDemoServer(t)
	st := s.store

	seed, _ := st.Users.ByUsername("demo")
	u, err := st.CloneUser(seed.ID, "old-otter", "hash", db.FormatTime(time.Now().Add(-time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	token, err := s.auth.CreateSessionTTL(u.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.auth.User(mustReqWithToken(token)); err != store.ErrNotFound {
		t.Fatalf("expired ephemeral token should be rejected: %v", err)
	}
}

func TestDemoFeedLimit(t *testing.T) {
	s, h := newDemoServer(t)
	cookie := demoSessionCookie(t, h)

	ids := ephemeralUserIDs(t, s.store)
	ephemeral, _ := s.store.Users.ByID(ids[0])
	feeds, _ := s.store.Feeds.List(ephemeral.ID)
	authorID := feeds[0].AuthorID

	add := func(title string) *httptest.ResponseRecorder {
		form := url.Values{"title": {title}, "feed_url": {"https://" + title + ".example/rss.xml"}}
		req := httptest.NewRequest(http.MethodPost, "/authors/"+itoa(authorID)+"/feeds", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}

	// seed 1 + max 5 = the user may add 5 before the cap bites.
	for i := 0; i < 5; i++ {
		if rr := add("extra" + itoa(int64(i))); rr.Code != http.StatusOK {
			t.Fatalf("add feed %d: %d", i, rr.Code)
		}
	}
	if rr := add("over"); !strings.Contains(rr.Body.String(), "demo limit reached") {
		t.Fatalf("expected demo limit error, got: %s", rr.Body.String())
	}
}

func mustReqWithToken(token string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	return req
}
