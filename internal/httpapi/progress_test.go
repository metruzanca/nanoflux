package httpapi

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/store"
)

// TestItemProgressResumesYouTube covers the full plugin-backed path: a saved
// position is rendered into the embed URL and the player controls, and the
// progress endpoints store, clear, and drop a finished video.
func TestItemProgressResumesYouTube(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	useVideoPlugin(t, s)

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "bigboxSWE", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "bigboxSWE", "https://www.youtube.com/feeds/videos.xml?channel_id=UCx", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{
		GUID: "yt:video:H0KAi8AWsnM", Title: "Video",
		Link: "https://www.youtube.com/watch?v=H0KAi8AWsnM", DurationSec: 300, FetchedAt: db.Now(),
	})
	items, _ := s.store.Items.List(u.ID, store.ItemFilter{})
	item := items[0]

	rr := doForm(h, "POST", "/items/"+itoa(item.ID)+"/progress", url.Values{
		"position": {"90"}, "duration": {"300"},
	}, cookie)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("save progress = %d, want 204", rr.Code)
	}
	saved, err := s.store.Progress.Get(u.ID, "plugin:youtube:H0KAi8AWsnM")
	if err != nil || saved.PositionSec != 90 {
		t.Fatalf("stored progress = %+v, %v", saved, err)
	}

	body := doGet(h, "/items/"+itoa(item.ID)+"/view", cookie).Body.String()
	for _, want := range []string{
		"start=90", "enablejsapi=1",
		`data-progress-key="plugin:youtube:H0KAi8AWsnM"`,
		`data-progress-player="youtube"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("item view missing %q: %s", want, body)
		}
	}
	// Resume is seamless: the modal shows no position label or reset control.
	if strings.Contains(body, "resume at") || strings.Contains(body, "data-progress-reset") {
		t.Fatalf("resume should not render a label or reset control: %s", body)
	}

	// A finished video drops its row so it starts over next time.
	rr = doForm(h, "POST", "/items/"+itoa(item.ID)+"/progress", url.Values{
		"position": {"290"}, "duration": {"300"},
	}, cookie)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("finished progress = %d, want 204", rr.Code)
	}
	if _, err := s.store.Progress.Get(u.ID, "plugin:youtube:H0KAi8AWsnM"); err == nil {
		t.Fatal("finished video should not keep a saved position")
	}
}

// TestItemProgressNativeEnclosure covers the host-owned native adapter: an item
// with a plain video enclosure is resumable with an item-scoped key, no plugin.
func TestItemProgressNativeEnclosure(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Video", "https://v.dev/rss.xml", "", "", 900)
	if _, err := s.store.Items.Upsert(f.ID, store.Item{
		GUID: "g1", Title: "Clip", Link: "https://v.dev/1", FetchedAt: db.Now(),
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	itemID, _ := s.store.Items.ByFeedIdentity(f.ID, "g1")
	s.store.Items.ReplaceEnclosures(itemID, []store.Enclosure{
		{URL: "https://v.dev/clip.mp4", MIMEType: "video/mp4", Kind: "video"},
	})

	rr := doForm(h, "POST", "/items/"+itoa(itemID)+"/progress", url.Values{
		"position": {"120"}, "duration": {"600"},
	}, cookie)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("save progress = %d, want 204", rr.Code)
	}
	saved, err := s.store.Progress.Get(u.ID, "item:"+itoa(itemID))
	if err != nil || saved.PositionSec != 120 {
		t.Fatalf("stored progress = %+v, %v", saved, err)
	}
	body := doGet(h, "/items/"+itoa(itemID)+"/view", cookie).Body.String()
	if !strings.Contains(body, `data-progress-player="native"`) ||
		!strings.Contains(body, `data-progress-resume="120"`) {
		t.Fatalf("native player missing progress attributes: %s", body)
	}
}
