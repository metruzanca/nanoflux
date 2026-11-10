package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/store"
)

// TestItemsWithoutTitleShowExternalID covers feeds that omit an item title
// (image-only posts, some RSS bridges): the row and the modal must render the
// item's external id instead of an empty link.
func TestItemsWithoutTitleShowExternalID(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, err := s.store.Users.ByUsername("alice")
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.store.Authors.Create(u.ID, "Example Blog", "", "")
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.store.Feeds.Create(u.ID, a.ID, "Example Blog", "https://bridge.example/feed/x", "", "", 900)
	if err != nil {
		t.Fatal(err)
	}
	const externalID = "832737045877309440"
	if _, err := s.store.Items.Upsert(f.ID, store.Item{
		GUID:      externalID,
		Link:      "https://links.example/p/1",
		FetchedAt: db.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	itemID, err := s.store.Items.ByFeedIdentity(f.ID, externalID)
	if err != nil {
		t.Fatal(err)
	}

	row := doGet(h, "/authors/"+itoa(a.ID), cookie)
	if row.Code != http.StatusOK {
		t.Fatalf("author page: %d %s", row.Code, row.Body.String())
	}
	if !strings.Contains(row.Body.String(), ">"+externalID+"</a>") {
		t.Fatalf("row should render the external id as its title: %s", row.Body.String())
	}

	modal := doGet(h, "/items/"+itoa(itemID)+"/view", cookie)
	if modal.Code != http.StatusOK {
		t.Fatalf("item view: %d %s", modal.Code, modal.Body.String())
	}
	if !strings.Contains(modal.Body.String(), "<h1>"+externalID+"</h1>") {
		t.Fatalf("modal should render the external id as its title: %s", modal.Body.String())
	}
}
