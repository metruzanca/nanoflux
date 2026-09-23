package httpapi

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/store"
)

// homeFixture creates a user with two collections; the first has an unread
// item, the second is quiet. Returns alice, the collections.
func homeFixture(t *testing.T, s *Server) (u store.User, cats, quiet store.Collection) {
	t.Helper()
	u, _ = s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://b.dev/rss.xml", "", "", 900)
	cats, _ = s.store.Collections.Create(u.ID, "cats")
	quiet, _ = s.store.Collections.Create(u.ID, "quiet")
	s.store.Collections.AddFeed(u.ID, cats.ID, f.ID)
	s.store.Collections.AddFeed(u.ID, quiet.ID, f.ID)
	s.store.Items.Upsert(f.ID, store.Item{GUID: "g1", Title: "Unread cat", Link: "https://b.dev/1", FetchedAt: db.Now()})
	return u, cats, quiet
}

func TestHomeFallsBackToUnreadWithoutConfig(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	homeFixture(t, s)

	body := doGet(h, "/", cookie).Body.String()
	if !strings.Contains(body, "unread (1)") || !strings.Contains(body, "Unread cat") {
		t.Fatalf("home without config should be the unread list: %s", body)
	}
}

func TestHomeDashboardRendersPinnedSections(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, cats, _ := homeFixture(t, s)

	s.store.Users.SetHomeConfig(u.ID, `[{"kind":"collection","ref_id":`+itoa(cats.ID)+`}]`)

	body := doGet(h, "/", cookie).Body.String()
	if !strings.Contains(body, `id="home-sections"`) || !strings.Contains(body, ">cats<") {
		t.Fatalf("home should render the pinned section: %s", body)
	}
	if !strings.Contains(body, "Unread cat") {
		t.Fatalf("pinned section should show its unread item: %s", body)
	}
	if !strings.Contains(body, `href="/collections/`+itoa(cats.ID)+`"`) {
		t.Fatalf("section heading should link to the collection: %s", body)
	}
}

func TestHomeHidesEmptyCollections(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _, quiet := homeFixture(t, s)

	// Mark everything read so the pinned collection yields no unread.
	s.store.Items.MarkAllRead(u.ID, 0)
	s.store.Users.SetHomeConfig(u.ID, `[{"kind":"collection","ref_id":`+itoa(quiet.ID)+`}]`)

	body := doGet(h, "/", cookie).Body.String()
	if strings.Contains(body, `id="home-sections"`) {
		t.Fatalf("empty pinned section should not render a dashboard: %s", body)
	}
	if !strings.Contains(body, "unread") {
		t.Fatalf("empty dashboard should fall back to unread: %s", body)
	}
}

func TestHomeConfigScopedToUser(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _, _ := homeFixture(t, s)

	other, _ := s.store.Users.Create("bob", "hash")
	oa, _ := s.store.Authors.Create(other.ID, "Bob", "", "")
	of, _ := s.store.Feeds.Create(other.ID, oa.ID, "BobFeed", "https://bob.dev/rss.xml", "", "", 900)
	oc, _ := s.store.Collections.Create(other.ID, "bobcol")
	s.store.Collections.AddFeed(other.ID, oc.ID, of.ID)
	s.store.Items.Upsert(of.ID, store.Item{GUID: "b1", Title: "Bob item", Link: "https://bob.dev/1", FetchedAt: db.Now()})

	s.store.Users.SetHomeConfig(u.ID, `[{"kind":"collection","ref_id":`+itoa(oc.ID)+`}]`)
	body := doGet(h, "/", cookie).Body.String()
	if strings.Contains(body, "Bob item") {
		t.Fatalf("another user's collection leaked into the dashboard: %s", body)
	}
}

func TestUnreadPage(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, cats, _ := homeFixture(t, s)
	s.store.Users.SetHomeConfig(u.ID, `[{"kind":"collection","ref_id":`+itoa(cats.ID)+`}]`)

	body := doGet(h, "/unread", cookie).Body.String()
	if !strings.Contains(body, "unread (1)") || !strings.Contains(body, "Unread cat") {
		t.Fatalf("/unread should render the full unread list: %s", body)
	}
}

func TestSettingsHomeCard(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, cats, _ := homeFixture(t, s)

	body := doGet(h, "/settings", cookie).Body.String()
	if !strings.Contains(body, `id="settings-home-card"`) || !strings.Contains(body, "pin a collection") {
		t.Fatalf("settings page missing the home card: %s", body)
	}

	rr := doForm(h, "POST", "/settings/home", url.Values{
		"action": {"add"}, "add_collection": {itoa(cats.ID)},
	}, cookie)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), ">cats<") {
		t.Fatalf("pin collection: %d %s", rr.Code, rr.Body.String())
	}
	after, _ := s.store.Users.ByID(u.ID)
	if !strings.Contains(after.HomeConfig, itoa(cats.ID)) {
		t.Fatalf("home config should record the pinned collection: %q", after.HomeConfig)
	}

	rr = doForm(h, "POST", "/settings/home", url.Values{
		"action": {"remove"}, "collection_id": {itoa(cats.ID)},
	}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("remove: %d %s", rr.Code, rr.Body.String())
	}
	after, _ = s.store.Users.ByID(u.ID)
	if strings.Contains(after.HomeConfig, itoa(cats.ID)) {
		t.Fatalf("home config should no longer contain the collection: %q", after.HomeConfig)
	}
}

// TestSettingsHomePickerIncludesAuto asserts the pin-collection picker offers
// auto collections too, not just user-created ones, and that pinning one
// renders it on the home dashboard.
func TestSettingsHomePickerIncludesAuto(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _, _ := homeFixture(t, s)
	auto, err := s.store.Collections.EnsureAuto(u.ID, "b.dev")
	if err != nil {
		t.Fatal(err)
	}

	body := doGet(h, "/settings", cookie).Body.String()
	if !strings.Contains(body, `value="`+itoa(auto.ID)+`"`) {
		t.Fatalf("pin picker should offer the auto collection: %s", body)
	}

	rr := doForm(h, "POST", "/settings/home", url.Values{
		"action": {"add"}, "add_collection": {itoa(auto.ID)},
	}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("pin auto collection: %d %s", rr.Code, rr.Body.String())
	}
	if after, _ := s.store.Users.ByID(u.ID); !strings.Contains(after.HomeConfig, itoa(auto.ID)) {
		t.Fatalf("home config should record the pinned auto collection: %q", after.HomeConfig)
	}
}

func TestHomeSectionRenderMode(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, cats, _ := homeFixture(t, s)

	// A config without a mode defaults to the list view.
	s.store.Users.SetHomeConfig(u.ID, `[{"kind":"collection","ref_id":`+itoa(cats.ID)+`}]`)
	body := doGet(h, "/", cookie).Body.String()
	if strings.Contains(body, `class="items masonry"`) {
		t.Fatalf("list-mode section should not carry the masonry class: %s", body)
	}

	// Grid mode puts the masonry class on that section's item list.
	s.store.Users.SetHomeConfig(u.ID, `[{"kind":"collection","ref_id":`+itoa(cats.ID)+`,"mode":"grid"}]`)
	body = doGet(h, "/", cookie).Body.String()
	if !strings.Contains(body, `class="items masonry"`) {
		t.Fatalf("grid-mode section should carry the masonry class: %s", body)
	}
}

func TestSettingsHomeRenderMode(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, cats, _ := homeFixture(t, s)
	s.store.Users.SetHomeConfig(u.ID, `[{"kind":"collection","ref_id":`+itoa(cats.ID)+`}]`)

	// The settings card offers a per-section render-method select.
	body := doGet(h, "/settings", cookie).Body.String()
	if !strings.Contains(body, `name="mode"`) || !strings.Contains(body, `>grid</option>`) {
		t.Fatalf("settings home card should offer a render-method select: %s", body)
	}

	rr := doForm(h, "POST", "/settings/home", url.Values{
		"action": {"mode"}, "collection_id": {itoa(cats.ID)}, "mode": {"grid"},
	}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("set mode: %d %s", rr.Code, rr.Body.String())
	}
	after, _ := s.store.Users.ByID(u.ID)
	if !strings.Contains(after.HomeConfig, `"mode":"grid"`) {
		t.Fatalf("home config should record the grid mode: %q", after.HomeConfig)
	}
	// And the home page now renders the section as a grid.
	if home := doGet(h, "/", cookie).Body.String(); !strings.Contains(home, `class="items masonry"`) {
		t.Fatalf("home should render the section as a grid: %s", home)
	}
}

func TestHomeRequiresAuth(t *testing.T) {
	_, h := newTestServer(t)
	if rr := doGet(h, "/", nil); rr.Code != http.StatusFound {
		t.Fatalf("unauthenticated home should redirect to login, got %d", rr.Code)
	}
}
