package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/metruzanca/nanoflux/internal/auth"
	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/poller"
	"github.com/metruzanca/nanoflux/internal/store"
)

// TestCollectionRefreshQueuesMemberFeeds asserts "refresh all" marks exactly the
// collection's feeds due and lets the poller fetch them, rather than fetching in
// the request. The member feeds start not-due, so only the queued deadline can
// make them eligible.
func TestCollectionRefreshQueuesMemberFeeds(t *testing.T) {
	s, h := newTestServer(t)
	p := poller.New(s.store, time.Minute, 1)
	p.SetHostSpacing(0) // drain both feeds in one cycle; they share a test host
	s.SetPoller(p)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Author", "", "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<?xml version="1.0"?><rss version="2.0"><channel><title>Blog</title><item><guid>g1</guid><title>Fresh</title><link>https://posts.example/1</link></item></channel></rss>`)
	}))
	defer srv.Close()

	f1, _ := s.store.Feeds.Create(u.ID, a.ID, "One", srv.URL+"/one", "", "", 86400)
	f2, _ := s.store.Feeds.Create(u.ID, a.ID, "Two", srv.URL+"/two", "", "", 86400)
	outside, _ := s.store.Feeds.Create(u.ID, a.ID, "Outside", srv.URL+"/outside", "", "", 86400)
	c, _ := s.store.Collections.Create(u.ID, "Dev")
	for _, f := range []store.Feed{f1, f2} {
		if err := s.store.Collections.AddFeed(u.ID, c.ID, f.ID); err != nil {
			t.Fatal(err)
		}
	}
	// Seed a recent poll so none of the feeds is due on its own.
	for _, f := range []store.Feed{f1, f2, outside} {
		if err := s.store.Feeds.SetPollMeta(f.ID, "", "", db.Now(), ""); err != nil {
			t.Fatal(err)
		}
	}
	if due, _ := s.store.Feeds.ListDue(db.Now()); len(due) != 0 {
		t.Fatalf("feeds should not be due before the refresh: %+v", due)
	}

	body := doGet(h, "/collections/"+itoa(c.ID), cookie).Body.String()
	if !strings.Contains(body, `hx-post="/collections/`+itoa(c.ID)+`/refresh"`) {
		t.Fatalf("collection page should offer a refresh-all button: %s", body)
	}

	rr := doForm(h, "POST", "/collections/"+itoa(c.ID)+"/refresh", url.Values{}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("refresh all: %d %s", rr.Code, rr.Body.String())
	}
	if note := rr.Body.String(); !strings.Contains(note, "queued 2 feeds for a refresh") {
		t.Fatalf("refresh-all note should report the queued feeds: %s", note)
	}

	for _, f := range []store.Feed{f1, f2} {
		got, _ := s.store.Feeds.ByID(u.ID, f.ID)
		if got.NextPollAt == "" || !got.NextPollScheduled {
			t.Fatalf("feed %d should be queued as scheduled and due, got next_poll_at=%q scheduled=%v",
				f.ID, got.NextPollAt, got.NextPollScheduled)
		}
	}
	if got, _ := s.store.Feeds.ByID(u.ID, outside.ID); got.NextPollAt != "" || got.NextPollScheduled {
		t.Fatalf("a feed outside the collection must not be queued: %+v", got)
	}

	if _, err := p.PollDue(context.Background()); err != nil {
		t.Fatalf("poll due: %v", err)
	}
	for _, f := range []store.Feed{f1, f2} {
		unread, _ := s.store.Items.CountUnread(u.ID, f.ID)
		if unread != 1 {
			t.Fatalf("queued feed %d should have polled its post, unread = %d", f.ID, unread)
		}
		got, _ := s.store.Feeds.ByID(u.ID, f.ID)
		if got.NextPollAt != "" || got.NextPollScheduled {
			t.Fatalf("a successful poll should clear the queued deadline on feed %d: %+v", f.ID, got)
		}
	}
	if unread, _ := s.store.Items.CountUnread(u.ID, outside.ID); unread != 0 {
		t.Fatalf("a feed outside the collection must not be fetched, unread = %d", unread)
	}
}

// TestCollectionRefreshSkipsPausedAndRateLimited asserts the bulk action leaves
// paused feeds and feeds inside a rate-limit window alone, and reports both.
func TestCollectionRefreshSkipsPausedAndRateLimited(t *testing.T) {
	s, h := newTestServer(t)
	s.SetPoller(poller.New(s.store, time.Minute, 1))
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Author", "", "")

	f1, _ := s.store.Feeds.Create(u.ID, a.ID, "One", "https://one.example/feed", "", "", 86400)
	f2, _ := s.store.Feeds.Create(u.ID, a.ID, "Two", "https://two.example/feed", "", "", 86400)
	f3, _ := s.store.Feeds.Create(u.ID, a.ID, "Three", "https://three.example/feed", "", "", 86400)
	c, _ := s.store.Collections.Create(u.ID, "Dev")
	for _, f := range []store.Feed{f1, f2, f3} {
		if err := s.store.Collections.AddFeed(u.ID, c.ID, f.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.store.Feeds.SetEnabled(u.ID, f1.ID, false); err != nil {
		t.Fatal(err)
	}
	future := db.FormatTime(time.Now().Add(time.Hour))
	if err := s.store.Feeds.SetNextPollAt(f2.ID, future); err != nil {
		t.Fatal(err)
	}
	if err := s.store.Feeds.SetNextPollScheduled(f2.ID, false); err != nil {
		t.Fatal(err)
	}

	rr := doForm(h, "POST", "/collections/"+itoa(c.ID)+"/refresh", url.Values{}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("refresh all: %d %s", rr.Code, rr.Body.String())
	}
	note := rr.Body.String()
	for _, want := range []string{"queued 1 feed for a refresh", "1 paused", "1 rate limited"} {
		if !strings.Contains(note, want) {
			t.Fatalf("refresh-all note missing %q: %s", want, note)
		}
	}

	if got, _ := s.store.Feeds.ByID(u.ID, f1.ID); got.NextPollAt != "" || got.NextPollScheduled {
		t.Fatalf("a paused feed must not be queued: %+v", got)
	}
	got2, _ := s.store.Feeds.ByID(u.ID, f2.ID)
	if got2.NextPollAt != future || got2.NextPollScheduled {
		t.Fatalf("a rate-limited feed must keep its retry deadline: %+v", got2)
	}
	got3, _ := s.store.Feeds.ByID(u.ID, f3.ID)
	if got3.NextPollAt == "" || !got3.NextPollScheduled {
		t.Fatalf("the eligible feed should be queued: %+v", got3)
	}
}

// TestCollectionRefreshButtonRequiresFeeds asserts an empty collection has
// nothing to refresh and does not render the button.
func TestCollectionRefreshButtonRequiresFeeds(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	c, _ := s.store.Collections.Create(u.ID, "Empty")

	body := doGet(h, "/collections/"+itoa(c.ID), cookie).Body.String()
	if strings.Contains(body, `hx-post="/collections/`+itoa(c.ID)+`/refresh"`) {
		t.Fatalf("an empty collection should not offer refresh all: %s", body)
	}
}

// TestCollectionRefreshRejectsOtherUsersCollection asserts the bulk action is
// scoped to the signed-in user.
func TestCollectionRefreshRejectsOtherUsersCollection(t *testing.T) {
	s, h := newTestServer(t)
	s.SetPoller(poller.New(s.store, time.Minute, 1))
	_ = sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	c, _ := s.store.Collections.Create(u.ID, "Dev")

	hash, _ := auth.HashPassword("other")
	if _, err := s.store.Users.Create("bob", hash); err != nil {
		t.Fatal(err)
	}
	form := url.Values{"username": {"bob"}, "password": {"other"}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	lrr := httptest.NewRecorder()
	h.ServeHTTP(lrr, req)
	var bob *http.Cookie
	for _, ck := range lrr.Result().Cookies() {
		if ck.Name == "rss_session" {
			bob = ck
		}
	}
	if bob == nil {
		t.Fatal("no session cookie for bob")
	}

	rr := doForm(h, "POST", "/collections/"+itoa(c.ID)+"/refresh", url.Values{}, bob)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("another user's collection should 404, got %d %s", rr.Code, rr.Body.String())
	}
}
