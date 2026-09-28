package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/metruzanca/nanoflux/internal/plugin"
	"github.com/metruzanca/nanoflux/pluginapi"
)

// stubProvisioner is a native plugin that can create a feed and manage it.
type stubProvisioner struct{ deleted bool }

func (stubProvisioner) Meta() pluginapi.Meta {
	return pluginapi.Meta{Name: "mailsite", APIVersion: pluginapi.APIVersion, ProvisionLabel: "newsletter"}
}
func (stubProvisioner) Match(u *url.URL, cap pluginapi.Capability) bool {
	switch cap {
	case pluginapi.CapProvision, pluginapi.CapFeedAdmin, pluginapi.CapDocs:
		return u == nil || u.Hostname() == "mails.example"
	}
	return false
}
func (stubProvisioner) Discover(context.Context, string, pluginapi.Host) ([]pluginapi.Candidate, error) {
	return nil, pluginapi.ErrUnsupportedCapability
}
func (stubProvisioner) Fetch(context.Context, pluginapi.FetchRequest, pluginapi.Host) (pluginapi.Result, error) {
	return pluginapi.Result{}, pluginapi.ErrUnsupportedCapability
}
func (stubProvisioner) Provision(context.Context, pluginapi.ProvisionRequest, pluginapi.Host) (pluginapi.Provisioned, error) {
	return pluginapi.Provisioned{
		FeedURL: "https://mails.example/feeds/pub1.xml",
		Title:   "My Newsletter",
		HomeURL: "https://mails.example/feeds/pub1",
		Fields:  []pluginapi.Field{{Name: "email", Label: "subscribe", Value: "pub1@mails.example"}},
	}, nil
}
func (stubProvisioner) Settings(string) []pluginapi.Field {
	return []pluginapi.Field{{Name: "email", Label: "subscribe", Value: "pub1@mails.example"}}
}
func (stubProvisioner) Action(_ context.Context, req pluginapi.FeedActionRequest, _ pluginapi.Host) (pluginapi.FeedActionResult, error) {
	if req.Action == "delete" {
		return pluginapi.FeedActionResult{Message: "deleted", Deleted: true}, nil
	}
	return pluginapi.FeedActionResult{Message: "done"}, nil
}

func withProvisioner(t *testing.T, s *Server, p pluginapi.Fetcher) {
	t.Helper()
	reg := plugin.NewRegistry()
	reg.RegisterNative(p)
	s.SetPlugins(reg, plugin.NewHosts(s.client, plugin.NewCooldown()))
}

// TestProvisionCreateFlow creates a feed through a provisioning plugin and
// asserts the subscribe address and feed URL are stored.
func TestProvisionCreateFlow(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	withProvisioner(t, s, stubProvisioner{})

	rr := doForm(h, "POST", "/feeds/provision", url.Values{
		"plugin":      {"mailsite"},
		"title":       {"My Newsletter"},
		"author_id":   {"new"},
		"author_name": {"Mails"},
	}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("provision: %d %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "pub1@mails.example") {
		t.Fatalf("missing subscribe address in %s", body)
	}

	feeds, err := s.store.Feeds.ListAll()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range feeds {
		if f.FeedURL == "https://mails.example/feeds/pub1.xml" {
			found = true
		}
	}
	if !found {
		t.Fatalf("provisioned feed not stored: %+v", feeds)
	}
}

// TestProvisionDefaultsAuthorName verifies a create with no author name names
// the new author after the feed title, so the provision form never dead-ends on
// a required field it does not need.
func TestProvisionDefaultsAuthorName(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	withProvisioner(t, s, stubProvisioner{})

	rr := doForm(h, "POST", "/feeds/provision", url.Values{
		"plugin":    {"mailsite"},
		"title":     {"My Newsletter"},
		"author_id": {"new"},
	}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("provision: %d %s", rr.Code, rr.Body.String())
	}
	authors, _ := s.store.Authors.List(1)
	if len(authors) != 1 || authors[0].Name != "My Newsletter" {
		t.Fatalf("authors = %+v", authors)
	}
}

// recordingProvisioner records the remote actions it is asked to perform.
type recordingProvisioner struct {
	stubProvisioner
	actions []string
}

func (r *recordingProvisioner) Action(_ context.Context, req pluginapi.FeedActionRequest, _ pluginapi.Host) (pluginapi.FeedActionResult, error) {
	r.actions = append(r.actions, req.Action)
	return r.stubProvisioner.Action(context.Background(), req, nil)
}

// TestFeedDeleteRemoteOptIn verifies the delete form's opt-in checkbox drives a
// remote delete: off by default, on only when posted.
func TestFeedDeleteRemoteOptIn(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	rec := &recordingProvisioner{}
	withProvisioner(t, s, rec)

	a, _ := s.store.Authors.Create(1, "Mails", "", "")
	f, _ := s.store.Feeds.CreateWithPlugin(1, a.ID, "N",
		"https://mails.example/feeds/pub1.xml", "", "", "", 900)
	rr := doForm(h, "POST", "/feeds/"+itoa(f.ID)+"/delete", url.Values{}, cookie)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("delete: %d", rr.Code)
	}
	if len(rec.actions) != 0 {
		t.Fatalf("remote delete ran without opt-in: %v", rec.actions)
	}

	f2, _ := s.store.Feeds.CreateWithPlugin(1, a.ID, "N2",
		"https://mails.example/feeds/pub2.xml", "", "", "", 900)
	doForm(h, "POST", "/feeds/"+itoa(f2.ID)+"/delete", url.Values{"delete_remote": {"1"}}, cookie)
	if len(rec.actions) != 1 || rec.actions[0] != "delete" {
		t.Fatalf("remote delete not called: %v", rec.actions)
	}
}

// TestFeedPluginPanelShown asserts the feed page renders the managed panel with
// the subscribe address, and that a remote delete drops the local feed.
func TestFeedPluginPanelShown(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	withProvisioner(t, s, stubProvisioner{})

	a, _ := s.store.Authors.Create(1, "Mails", "", "")
	f, err := s.store.Feeds.CreateWithPlugin(1, a.ID, "My Newsletter",
		"https://mails.example/feeds/pub1.xml", "https://mails.example/feeds/pub1", "", "", 900)
	if err != nil {
		t.Fatal(err)
	}

	rr := doGet(h, "/feeds/"+itoa(f.ID), cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("feed page: %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "pub1@mails.example") {
		t.Fatalf("panel missing subscribe address: %s", rr.Body.String())
	}

	// A remote delete removes the local feed too.
	rr = doForm(h, "POST", "/feeds/"+itoa(f.ID)+"/plugin-admin", url.Values{"action": {"delete"}}, cookie)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("plugin-admin delete: %d %s", rr.Code, rr.Body.String())
	}
	if _, err := s.store.Feeds.ByID(1, f.ID); err == nil {
		t.Fatal("feed still exists after remote delete")
	}
}
