package httpapi

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/charmbracelet/log"

	"github.com/metruzanca/nanoflux/internal/auth"
	"github.com/metruzanca/nanoflux/internal/plugin"
	"github.com/metruzanca/nanoflux/internal/store"
	"github.com/metruzanca/nanoflux/internal/web"
	"github.com/metruzanca/nanoflux/pluginapi"
)

// provisioners exposes the loaded plugins that can create a remote feed (a
// newsletter inbox), for the add-feed menu. Empty when none do.
func (s *Server) provisioners() []plugin.Provisioner {
	if s.plugins == nil {
		return nil
	}
	return s.plugins.Provisioners()
}

// provisionEntries maps the create-capable plugins to the topbar add-menu
// entries (plugin name and "Add a …" label).
func (s *Server) provisionEntries() []provisionEntry {
	ps := s.provisioners()
	out := make([]provisionEntry, 0, len(ps))
	for _, p := range ps {
		out = append(out, provisionEntry{Name: p.F.Meta().Name, Label: p.Label})
	}
	return out
}

// provisionFormData is the "create on the site" form shown in the
// #provision-dialog from the nav "add" menu. It has a title only: a created feed
// becomes its own author (named after the feed), so the dialog needs no author
// choice.
type provisionFormData struct {
	Plugin      string
	Label       string
	Placeholder string
}

// provisionFormFragment renders the create form into the nav add menu's dialog.
// It is opened by the "Add a …" entry the add menu renders.
func (s *Server) provisionFormFragment(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("plugin"))
	p, _ := s.registryProvisioner(name)
	if p == nil {
		renderError(w, r, "that integration is not available")
		return
	}
	label := ""
	for _, pr := range s.provisioners() {
		if pr.F.Meta().Name == name {
			label = pr.Label
			break
		}
	}
	web.Render(w, r, provisionFormFields(provisionFormData{
		Plugin: name, Label: label, Placeholder: "e.g. My Newsletter",
	}))
}

// feedProvision creates a remote feed through a provisioning plugin and stores
// it locally. The plugin owns the create request; the host then stores the
// returned feed URL and polls it with the generic parser. The created feed gets
// its own author, named after the feed title.
func (s *Server) feedProvision(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	name := strings.TrimSpace(r.FormValue("plugin"))
	p, f := s.registryProvisioner(name)
	if p == nil {
		writeFormError(w, r, "provision-error", "that integration is not available")
		return
	}
	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		writeFormError(w, r, "provision-error", "a title is required")
		return
	}
	if s.demoFeedLimitReached(u.ID) {
		writeFormError(w, r, "provision-error", demoAddFeedMessage())
		return
	}
	got, err := p.Provision(r.Context(), pluginapi.ProvisionRequest{Title: title}, s.pluginHosts.For(f))
	if err != nil {
		log.Error("plugin provision", "plugin", name, "err", err)
		writeFormError(w, r, "provision-error", provisionErrorMessage(err))
		return
	}
	feedURL := normalizeURL(got.FeedURL)
	if feedURL == "" {
		writeFormError(w, r, "provision-error", "the integration did not return a feed")
		return
	}
	if s.feedURLExists(u.ID, feedURL) {
		writeFormError(w, r, "provision-error", "you already have this feed")
		return
	}
	feedTitle := got.Title
	if feedTitle == "" {
		feedTitle = title
	}
	// The feed's author is named after the feed itself: the dialog has no page
	// to derive a name from, and the service's name is the natural author.
	author, err := s.store.Authors.Create(u.ID, feedTitle, "", "")
	if err != nil {
		log.Error("create provisioned author", "err", err)
		writeFormError(w, r, "provision-error", "could not create the feed")
		return
	}
	feed, err := s.store.Feeds.CreateWithPlugin(u.ID, author.ID, feedTitle, feedURL, got.HomeURL, "",
		s.pluginNameFor(feedURL), 900)
	if err != nil {
		log.Error("create provisioned feed", "err", err)
		writeFormError(w, r, "provision-error", "could not save the feed")
		return
	}
	if err := s.store.Collections.AssignAuto(u.ID, feed.ID, got.HomeURL, feedURL); err != nil {
		log.Error("assign auto collection", "feed_id", feed.ID, "err", err)
	}
	s.pollFeedNow(feed)

	web.Render(w, r, provisionCreated(feed.ID, feedTitle, got.Fields))
}

// registryProvisioner resolves a create-capable plugin by its name or label.
func (s *Server) registryProvisioner(name string) (pluginapi.Provisioner, pluginapi.Fetcher) {
	if s.plugins == nil {
		return nil, nil
	}
	if p, f := s.plugins.ProvisionerByName(name); p != nil {
		return p, f
	}
	for _, pr := range s.provisioners() {
		if pr.Label == name {
			return pr.P, pr.F
		}
	}
	return nil, nil
}

// provisionErrorMessage maps a provision failure to a short user-facing reason.
func provisionErrorMessage(err error) string {
	if rl, ok := err.(*pluginapi.RateLimit); ok {
		return "the service is rate-limiting requests (HTTP " + strconv.Itoa(rl.Status) + ") — wait a bit and try again"
	}
	if se, ok := err.(*pluginapi.StatusError); ok {
		return "the service returned an error (HTTP " + strconv.Itoa(se.Code) + ")"
	}
	return "could not create the feed — see server logs"
}

// pluginAdminAction runs a management action on a feed's remote side and
// re-renders the feed's plugin panel with the result.
func (s *Server) pluginAdminAction(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	feed, err := s.store.Feeds.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	fa, f := s.feedAdmin(feed.FeedURL)
	if fa == nil {
		writeFormError(w, r, pluginPanelErrorID(feed.ID), "this feed is not managed by a plugin")
		return
	}
	fields := map[string]string{
		"title": feed.Title,
		"icon":  r.FormValue("icon"),
	}
	res, err := fa.Action(r.Context(), pluginapi.FeedActionRequest{
		FeedURL: feed.FeedURL, Action: r.FormValue("action"), Fields: fields,
	}, s.pluginHosts.For(f))
	if err != nil {
		log.Error("plugin feed action", "feed_id", id, "action", r.FormValue("action"), "err", err)
		writeFormError(w, r, pluginPanelErrorID(feed.ID), provisionErrorMessage(err))
		return
	}
	// A remote delete the user asked for during local delete is performed by
	// feedDelete; an explicit delete action here drops the local feed too.
	if res.Deleted {
		cacheKeys, err := s.store.Feeds.Delete(u.ID, id)
		if err != nil {
			log.Error("delete feed after remote delete", "feed_id", id, "err", err)
			writeFormError(w, r, pluginPanelErrorID(feed.ID), "the feed was deleted remotely but could not be removed locally")
			return
		}
		s.purgeObjects(r.Context(), cacheKeys)
		w.Header().Set("HX-Redirect", "/authors/"+strconv.FormatInt(feed.AuthorID, 10))
		w.WriteHeader(http.StatusNoContent)
		return
	}
	panel, _ := s.feedPanel(feed, res.Message)
	web.Render(w, r, feedPluginPanel(panel))
}

// feedAdmin returns the plugin managing a feed URL, or (nil, nil).
func (s *Server) feedAdmin(feedURL string) (pluginapi.FeedAdmin, pluginapi.Fetcher) {
	if s.plugins == nil {
		return nil, nil
	}
	u, err := url.Parse(feedURL)
	if err != nil {
		return nil, nil
	}
	return s.plugins.MatchFeedAdmin(u)
}

// feedPanelData drives the "managed by a plugin" card on a feed's page.
type feedPanelData struct {
	FeedID  int64
	Title   string
	Fields  []pluginapi.Field
	Message string
}

// feedPanel assembles the panel for a feed, or returns ok=false when no loaded
// plugin manages it. message is an optional action confirmation.
func (s *Server) feedPanel(feed store.Feed, message string) (feedPanelData, bool) {
	fa, _ := s.feedAdmin(feed.FeedURL)
	if fa == nil {
		return feedPanelData{}, false
	}
	return feedPanelData{
		FeedID:  feed.ID,
		Title:   feed.Title,
		Fields:  fa.FeedFields(feed.FeedURL),
		Message: message,
	}, true
}

// pluginPanelErrorID is the DOM id of a feed panel's error container.
func pluginPanelErrorID(feedID int64) string {
	return "feed-plugin-error-" + strconv.FormatInt(feedID, 10)
}

// deleteRemoteRequested reports whether the feed-delete form opted in to also
// deleting the remote feed. The checkbox is off by default, so an absent field
// means no.
func deleteRemoteRequested(r *http.Request) bool {
	return r.FormValue("delete_remote") == "1"
}
