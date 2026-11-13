package httpapi

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestAddDialogsOpenAnywhere covers the shared add dialogs: their shells live
// in the layout, so every page can open them, and each form is fetched from a
// fragment route instead of navigating to the index that used to own it.
func TestAddDialogsOpenAnywhere(t *testing.T) {
	_, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	// A page other than /authors, /collections or /lists carries the shells and
	// the menu entries that fill them.
	body := doGet(h, "/", cookie).Body.String()
	for _, want := range []string{
		`id="add-feed-dialog"`,
		`id="create-author-dialog"`,
		`id="add-collection-dialog"`,
		`id="add-list-dialog"`,
		`data-action="addFeed"`,
		`data-action="addAuthor"`,
		`data-action="addCollection"`,
		`data-action="addList"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("home page missing shared add dialog %q: %s", want, body)
		}
	}

	fragments := map[string]string{
		"/fragments/add-feed":       `hx-post="/fragments/feed-preview"`,
		"/fragments/add-author":     `id="add-author-error"`,
		"/fragments/add-collection": `hx-post="/collections"`,
		"/fragments/add-list":       `hx-post="/lists"`,
	}
	for path, want := range fragments {
		rr := doGet(h, path, cookie)
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), want) {
			t.Fatalf("%s: %d %s", path, rr.Code, rr.Body.String())
		}
		// Signed-out callers must not reach the forms.
		if rr := doGet(h, path, nil); rr.Code != http.StatusFound ||
			!strings.HasPrefix(rr.Header().Get("Location"), "/login") {
			t.Fatalf("%s should require a session: %d %q", path, rr.Code, rr.Header().Get("Location"))
		}
	}
}

// TestCreateListAndCollectionRedirects checks that creating either from a
// shared dialog lands on the new item's page (the form's result container
// exists on every page, so htmx can resolve the save target before the
// redirect).
func TestCreateListAndCollectionRedirects(t *testing.T) {
	_, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	rr := doForm(h, "POST", "/lists", url.Values{"name": {"Reading"}}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("create list: %d %s", rr.Code, rr.Body.String())
	}
	if loc := rr.Header().Get("HX-Redirect"); !strings.HasPrefix(loc, "/lists/") {
		t.Fatalf("create list should redirect to the new list, got %q", loc)
	}

	rr = doForm(h, "POST", "/collections", url.Values{"name": {"Dev"}}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("create collection: %d %s", rr.Code, rr.Body.String())
	}
	if loc := rr.Header().Get("HX-Redirect"); !strings.HasPrefix(loc, "/collections/") {
		t.Fatalf("create collection should redirect to the new collection, got %q", loc)
	}
}
