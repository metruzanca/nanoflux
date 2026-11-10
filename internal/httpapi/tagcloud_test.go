package httpapi

import (
	"strings"
	"testing"

	"github.com/metruzanca/nanoflux/internal/store"
)

func TestAuthorTagCloud(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, author, _ := savedViewFixture(t, s)

	// Off by default: the author page renders no tag cloud.
	authorURL := "/authors/" + itoa(author.ID)
	body := doGet(h, authorURL, cookie).Body.String()
	if strings.Contains(body, `class="tag-cloud"`) {
		t.Fatalf("tag cloud should be off by default: %s", body)
	}

	if err := s.store.Users.SetShowTagCloud(u.ID, true); err != nil {
		t.Fatal(err)
	}
	body = doGet(h, authorURL, cookie).Body.String()
	for _, want := range []string{
		`class="tag-cloud"`,
		`class="tag-cloud-preview"`,
		`u/example-poster`,
		`u/someone-else`,
		`(1)`,
		`?tab=unread&amp;sort=newest&amp;f=tag%3Ashow%3Au%2Fexample-poster`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("author tag cloud missing %q: %s", want, body)
		}
	}

	// Clicking a cloud tag filters the current author list and leaves the other
	// tag's post out.
	filtered := doGet(h, authorURL+"?tab=unread&sort=newest&f=tag%3Ashow%3Au%2Fexample-poster", cookie).Body.String()
	if !strings.Contains(filtered, "Go post") || strings.Contains(filtered, "Other post") {
		t.Fatalf("tag filter should keep only the matching post: %s", filtered)
	}

	// A view-scoped page shows only the selected view's tags.
	view, err := s.store.SavedViews.Create(u.ID, store.SavedView{
		AuthorID: author.ID, Title: "Example posts",
		Filters: []string{"tag:show:u/example-poster"},
	})
	if err != nil {
		t.Fatal(err)
	}
	body = doGet(h, authorURL+"?view="+itoa(view.ID), cookie).Body.String()
	if !strings.Contains(body, "u/example-poster") {
		t.Fatalf("view-scoped cloud should keep the view tag: %s", body)
	}
	if strings.Contains(body, "u/someone-else") {
		t.Fatalf("view-scoped cloud should not include tags outside the view: %s", body)
	}
}
