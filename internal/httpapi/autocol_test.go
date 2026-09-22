package httpapi

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/metruzanca/nanoflux/internal/store"
)

// autoCollection finds the auto collection named name for the user, or fails.
func autoCollection(t *testing.T, s *Server, userID int64, name string) store.Collection {
	t.Helper()
	cols, _ := s.store.Collections.List(userID)
	for _, c := range cols {
		if c.IsAuto && c.Name == name {
			return c
		}
	}
	t.Fatalf("no auto collection named %q: %+v", name, cols)
	return store.Collection{}
}

func TestFeedCreateAutoCollection(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")

	rr := doForm(h, "POST", "/feeds", url.Values{
		"title":     {"Channel"},
		"feed_url":  {"https://www.youtube.com/feeds/videos.xml?channel_id=UCx"},
		"home_url":  {"https://www.youtube.com/channel/UCx"},
		"author_id": {"new"}, "author_name": {"Channel"},
	}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("create feed: %d %s", rr.Code, rr.Body.String())
	}

	c := autoCollection(t, s, u.ID, "youtube.com")
	feeds, _ := s.store.Collections.Feeds(u.ID, c.ID)
	if len(feeds) != 1 {
		t.Fatalf("youtube.com auto collection should contain the feed: %+v", feeds)
	}
}

func TestFeedCreateAutoCollectionFallsBackToFeedURL(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")

	// No home_url; the host is derived from the feed url. Subdomains collapse.
	rr := doForm(h, "POST", "/feeds", url.Values{
		"title":     {"Channel"},
		"feed_url":  {"https://music.youtube.com/feeds/videos.xml"},
		"author_id": {"new"}, "author_name": {"Channel"},
	}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("create feed: %d %s", rr.Code, rr.Body.String())
	}

	autoCollection(t, s, u.ID, "youtube.com")
}

func TestFeedUpdateMovesAutoCollection(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")

	rr := doForm(h, "POST", "/feeds", url.Values{
		"title":     {"Blog"},
		"feed_url":  {"https://www.youtube.com/feeds/videos.xml?channel_id=UCx"},
		"home_url":  {"https://www.youtube.com/channel/UCx"},
		"author_id": {"new"}, "author_name": {"Channel"},
	}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("create feed: %d %s", rr.Code, rr.Body.String())
	}
	feeds, _ := s.store.Feeds.List(u.ID)
	f := feeds[0]

	// Move the feed to example.com; it should leave youtube.com and land there.
	rr = doForm(h, "POST", "/feeds/"+itoa(f.ID)+"/edit", url.Values{
		"title": {"Blog"}, "feed_url": {"https://example.com/rss.xml"},
		"home_url": {"https://example.com"}, "poll_interval_sec": {"900"},
		"author_id": {itoa(f.AuthorID)},
	}, cookie)
	if rr.Code != http.StatusFound {
		t.Fatalf("edit feed: %d %s", rr.Code, rr.Body.String())
	}

	ex := autoCollection(t, s, u.ID, "example.com")
	exFeeds, _ := s.store.Collections.Feeds(u.ID, ex.ID)
	if len(exFeeds) != 1 || exFeeds[0].ID != f.ID {
		t.Fatalf("example.com auto collection should contain the feed: %+v", exFeeds)
	}
	yt := autoCollection(t, s, u.ID, "youtube.com")
	ytFeeds, _ := s.store.Collections.Feeds(u.ID, yt.ID)
	if len(ytFeeds) != 0 {
		t.Fatalf("feed should leave the youtube.com auto collection: %+v", ytFeeds)
	}
}

func TestAutoCollectionDelete(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")

	rr := doForm(h, "POST", "/feeds", url.Values{
		"title":     {"Channel"},
		"feed_url":  {"https://www.youtube.com/feeds/videos.xml?channel_id=UCx"},
		"author_id": {"new"}, "author_name": {"Channel"},
	}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("create feed: %d %s", rr.Code, rr.Body.String())
	}
	c := autoCollection(t, s, u.ID, "youtube.com")

	// The auto collection page offers a delete form.
	body := doGet(h, "/collections/"+itoa(c.ID), cookie).Body.String()
	if !strings.Contains(body, `action="/collections/`+itoa(c.ID)+`/delete"`) {
		t.Fatalf("auto collection page should offer delete: %s", body)
	}

	// Deleting removes the collection but keeps the feed and its author.
	rr = doForm(h, "POST", "/collections/"+itoa(c.ID)+"/delete", url.Values{}, cookie)
	if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/collections" {
		t.Fatalf("auto delete should redirect to /collections, got %d %s", rr.Code, rr.Body.String())
	}
	if _, err := s.store.Collections.ByID(u.ID, c.ID); err == nil {
		t.Fatalf("auto collection should be deleted")
	}
	feeds, _ := s.store.Feeds.List(u.ID)
	if len(feeds) != 1 {
		t.Fatalf("deleting an auto collection must not delete its feeds: %+v", feeds)
	}

	// It is gone from the index, and a later feed on that site recreates it.
	body = doGet(h, "/collections", cookie).Body.String()
	if strings.Contains(body, ">youtube.com<") {
		t.Fatalf("deleted auto collection should not be listed: %s", body)
	}
	doForm(h, "POST", "/feeds", url.Values{
		"title":     {"Channel 2"},
		"feed_url":  {"https://www.youtube.com/feeds/videos.xml?channel_id=UCy"},
		"author_id": {"new"}, "author_name": {"Channel 2"},
	}, cookie)
	autoCollection(t, s, u.ID, "youtube.com")
}

func TestAutoCollectionAddRemoveFeedBlocked(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")

	rr := doForm(h, "POST", "/feeds", url.Values{
		"title":     {"Channel"},
		"feed_url":  {"https://www.youtube.com/feeds/videos.xml?channel_id=UCx"},
		"author_id": {"new"}, "author_name": {"Channel"},
	}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("create feed: %d %s", rr.Code, rr.Body.String())
	}
	c := autoCollection(t, s, u.ID, "youtube.com")
	feeds, _ := s.store.Feeds.List(u.ID)

	rr = doForm(h, "POST", "/collections/"+itoa(c.ID)+"/add-feed", url.Values{
		"feed_id": {itoa(feeds[0].ID)},
	}, cookie)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("add-feed to auto collection should be 400, got %d %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "managed automatically") {
		t.Fatalf("add-feed error should explain: %s", rr.Body.String())
	}

	rr = doForm(h, "POST", "/collections/"+itoa(c.ID)+"/remove-feed/"+itoa(feeds[0].ID), url.Values{}, cookie)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("remove-feed from auto collection should be 400, got %d %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "managed automatically") {
		t.Fatalf("remove-feed error should explain: %s", rr.Body.String())
	}

	// Membership survives the blocked remove.
	in, _ := s.store.Collections.Feeds(u.ID, c.ID)
	if len(in) != 1 {
		t.Fatalf("auto collection membership should be untouched: %+v", in)
	}
}

func TestAutoCollectionHiddenFromFeedEditCheckboxes(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")

	// A youtube feed creates the auto collection; a manual collection also exists.
	rr := doForm(h, "POST", "/feeds", url.Values{
		"title":     {"Channel"},
		"feed_url":  {"https://www.youtube.com/feeds/videos.xml?channel_id=UCx"},
		"author_id": {"new"}, "author_name": {"Channel"},
	}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("create feed: %d %s", rr.Code, rr.Body.String())
	}
	manual, _ := s.store.Collections.Create(u.ID, "Dev")
	feeds, _ := s.store.Feeds.List(u.ID)
	f := feeds[0]

	body := doGet(h, "/feeds/"+itoa(f.ID)+"/edit", cookie).Body.String()
	if !strings.Contains(body, "Dev") {
		t.Fatalf("manual collection should appear in edit checkboxes: %s", body)
	}
	yt := autoCollection(t, s, u.ID, "youtube.com")
	if strings.Contains(body, `name="collections" value="`+itoa(yt.ID)+`"`) {
		t.Fatalf("auto collection should not appear in edit checkboxes: %s", body)
	}

	// Editing the feed must not disturb the auto membership.
	doForm(h, "POST", "/feeds/"+itoa(f.ID)+"/edit", url.Values{
		"title": {"Channel"}, "feed_url": {"https://www.youtube.com/feeds/videos.xml?channel_id=UCx"},
		"home_url": {"https://www.youtube.com/channel/UCx"}, "poll_interval_sec": {"900"},
		"author_id": {itoa(f.AuthorID)}, "collections": {itoa(manual.ID)},
	}, cookie)
	c := autoCollection(t, s, u.ID, "youtube.com")
	in, _ := s.store.Collections.Feeds(u.ID, c.ID)
	if len(in) != 1 || in[0].ID != f.ID {
		t.Fatalf("auto membership should survive a manual edit: %+v", in)
	}
}

func TestCollectionsPageHidesDeleteForAuto(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")

	doForm(h, "POST", "/feeds", url.Values{
		"title":     {"Channel"},
		"feed_url":  {"https://www.youtube.com/feeds/videos.xml?channel_id=UCx"},
		"author_id": {"new"}, "author_name": {"Channel"},
	}, cookie)

	body := doGet(h, "/collections", cookie).Body.String()
	if !strings.Contains(body, "youtube.com") {
		t.Fatalf("auto collection should be listed: %s", body)
	}
	if !strings.Contains(body, `class="badge"`) {
		t.Fatalf("auto collection should carry an auto badge: %s", body)
	}
	c := autoCollection(t, s, u.ID, "youtube.com")
	// The index row has no inline delete (delete lives on the collection page),
	// but the auto collection page does offer one.
	if strings.Contains(body, "/collections/"+itoa(c.ID)+"/delete") {
		t.Fatalf("collections index should not offer an inline delete: %s", body)
	}
	cbody := doGet(h, "/collections/"+itoa(c.ID), cookie).Body.String()
	if !strings.Contains(cbody, "/collections/"+itoa(c.ID)+"/delete") {
		t.Fatalf("auto collection page should offer delete: %s", cbody)
	}

	// The auto collection page hides the manual add-feed/remove controls.
	if strings.Contains(cbody, "add feed") {
		t.Fatalf("auto collection page should not offer add feed: %s", cbody)
	}
	if strings.Contains(cbody, "/remove-feed/") {
		t.Fatalf("auto collection page should not offer remove feed: %s", cbody)
	}
}

func TestOpmlExportExcludesAutoCollections(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")

	// One feed lands in a youtube.com auto collection; the other is grouped in
	// a manual "tech" collection.
	doForm(h, "POST", "/feeds", url.Values{
		"title":     {"Channel"},
		"feed_url":  {"https://www.youtube.com/feeds/videos.xml?channel_id=UCx"},
		"author_id": {"new"}, "author_name": {"Channel"},
	}, cookie)
	manual, _ := s.store.Collections.Create(u.ID, "tech")
	feeds, _ := s.store.Feeds.List(u.ID)
	var grouped store.Feed
	for _, f := range feeds {
		if f.Title == "Channel" {
			continue
		}
		grouped = f
	}
	if grouped.ID == 0 {
		a, _ := s.store.Authors.Create(u.ID, "Grouped", "", "")
		grouped, _ = s.store.Feeds.Create(u.ID, a.ID, "Grouped", "https://group.dev/rss.xml", "https://group.dev", "", 900)
	}
	s.store.Collections.AddFeed(u.ID, manual.ID, grouped.ID)

	body := doGet(h, "/settings/export.opml", cookie).Body.String()
	if strings.Contains(body, `text="youtube.com"`) {
		t.Fatalf("export should not include auto collections as groups: %s", body)
	}
	if !strings.Contains(body, `text="tech"`) {
		t.Fatalf("export should include manual collections: %s", body)
	}
	// The youtube feed falls back to an ungrouped outline.
	if !strings.Contains(body, `xmlUrl="https://www.youtube.com/feeds/videos.xml?channel_id=UCx"`) {
		t.Fatalf("export should still include the youtube feed ungrouped: %s", body)
	}
}
