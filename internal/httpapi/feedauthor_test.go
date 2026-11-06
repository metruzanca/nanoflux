package httpapi

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestFeedChangeAuthorFragment covers the dialog content: the feed's other
// authors are offered, the current one is excluded, and a feed outside the user
// is a 404.
func TestFeedChangeAuthorFragment(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	current, _ := s.store.Authors.Create(u.ID, "Current", "", "")
	if _, err := s.store.Authors.Create(u.ID, "Other", "", ""); err != nil {
		t.Fatal(err)
	}
	f, _ := s.store.Feeds.Create(u.ID, current.ID, "Movable", "https://movable.example/feed.xml", "", "", 900)

	rr := doGet(h, "/fragments/change-author?feed="+itoa(f.ID), cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("fragment: %d %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "change author") || !strings.Contains(body, `name="author_id"`) {
		t.Fatalf("fragment missing the form: %s", body)
	}
	if !strings.Contains(body, "Other") {
		t.Fatalf("fragment missing the other author: %s", body)
	}
	if strings.Contains(body, ">Current<") {
		t.Fatalf("fragment should not offer the current author: %s", body)
	}

	if rr := doGet(h, "/fragments/change-author?feed=999999", cookie); rr.Code != http.StatusNotFound {
		t.Fatalf("foreign feed fragment: %d", rr.Code)
	}

	// The expanded feed panel carries the button that opens the dialog.
	panel := doGet(h, "/feeds/"+itoa(f.ID)+"/edit-fragment", cookie).Body.String()
	if !strings.Contains(panel, `data-change-author="`+itoa(f.ID)+`"`) {
		t.Fatalf("feed panel missing the change-author button: %s", panel)
	}
}

// TestFeedChangeAuthorMove moves a feed to another author and checks ownership,
// the empty success body the accordion swap relies on, and rejections.
func TestFeedChangeAuthorMove(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	first, _ := s.store.Authors.Create(u.ID, "First", "", "")
	second, _ := s.store.Authors.Create(u.ID, "Second", "", "")
	f, _ := s.store.Feeds.Create(u.ID, first.ID, "Movable", "https://movable.example/feed.xml", "", "", 900)

	// The current author's edit page lists the feed; the other's does not.
	firstEdit := doGet(h, "/authors/"+itoa(first.ID)+"/edit", cookie).Body.String()
	if !strings.Contains(firstEdit, "Movable") {
		t.Fatal("the feed should start on the first author")
	}

	rr := doForm(h, http.MethodPost, "/feeds/"+itoa(f.ID)+"/author", url.Values{
		"author_id": {itoa(second.ID)},
	}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("move feed: %d %s", rr.Code, rr.Body.String())
	}
	if rr.Body.Len() != 0 {
		t.Fatalf("success should return an empty body for the outerHTML swap, got %q", rr.Body.String())
	}
	got, err := s.store.Feeds.ByID(u.ID, f.ID)
	if err != nil || got.AuthorID != second.ID {
		t.Fatalf("feed author = %v, %v; want %d", got.AuthorID, err, second.ID)
	}
	if strings.Contains(doGet(h, "/authors/"+itoa(first.ID)+"/edit", cookie).Body.String(), "Movable") {
		t.Fatal("the moved feed should be gone from the first author's page")
	}
	if !strings.Contains(doGet(h, "/authors/"+itoa(second.ID)+"/edit", cookie).Body.String(), "Movable") {
		t.Fatal("the moved feed should appear on the second author's page")
	}

	// The same author is rejected, leaving the feed put.
	if rr := doForm(h, http.MethodPost, "/feeds/"+itoa(f.ID)+"/author", url.Values{
		"author_id": {itoa(second.ID)},
	}, cookie); rr.Code != http.StatusBadRequest {
		t.Fatalf("same-author move: %d %s", rr.Code, rr.Body.String())
	}

	// Another user's author is rejected: Authors.ByID is user-scoped.
	bob, _ := s.store.Users.Create("bob", "hash")
	foreign, _ := s.store.Authors.Create(bob.ID, "Foreign", "", "")
	rr = doForm(h, http.MethodPost, "/feeds/"+itoa(f.ID)+"/author", url.Values{
		"author_id": {itoa(foreign.ID)},
	}, cookie)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("foreign-author move: %d %s", rr.Code, rr.Body.String())
	}
	if rr.Header().Get("HX-Retarget") != "#change-author-dialog" {
		t.Fatalf("error should retarget the dialog, got %q", rr.Header().Get("HX-Retarget"))
	}
	got, _ = s.store.Feeds.ByID(u.ID, f.ID)
	if got.AuthorID != second.ID {
		t.Fatalf("rejected move changed the author to %d", got.AuthorID)
	}

	// A missing selection is rejected too.
	if rr := doForm(h, http.MethodPost, "/feeds/"+itoa(f.ID)+"/author", url.Values{}, cookie); rr.Code != http.StatusBadRequest {
		t.Fatalf("empty-author move: %d %s", rr.Code, rr.Body.String())
	}
}
