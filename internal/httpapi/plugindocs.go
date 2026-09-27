package httpapi

import (
	"net/http"
	"net/url"

	"github.com/charmbracelet/log"

	"github.com/metruzanca/nanoflux/internal/plugin"
	"github.com/metruzanca/nanoflux/internal/web"
	"github.com/metruzanca/nanoflux/pluginapi"
)

// pluginDocsFragment renders a plugin's documentation, as Markdown converted to
// HTML, into the shared #plugin-docs-dialog. It is reachable by any signed-in
// user (not just admins): the feed edit page offers the same docs next to its
// filter rules, where a non-admin most needs them.
//
// The plugin is named by the ?plugin= query. An unknown plugin or one without
// docs renders a short explanation instead of a bare error, so the dialog is
// never an empty box.
func (s *Server) pluginDocsFragment(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("plugin")
	if name == "" {
		web.Render(w, r, pluginDocsDialog("", "", "no plugin selected"))
		return
	}
	if s.plugins == nil {
		web.Render(w, r, pluginDocsDialog(name, "", "the plugin system is not available"))
		return
	}
	docs, err := s.plugins.Docs(name)
	if err != nil {
		// A miss (unknown plugin, or one without docs) is a normal outcome for
		// a URL the user can edit; only log anything unexpected.
		if err != pluginapi.ErrUnsupportedCapability && err != plugin.ErrNotFound {
			log.Error("plugin docs", "plugin", name, "err", err)
		}
		web.Render(w, r, pluginDocsDialog(name, "", "this plugin has no documentation"))
		return
	}
	body := web.Markdown(docs)
	if body == "" {
		web.Render(w, r, pluginDocsDialog(name, "", "this plugin has no documentation"))
		return
	}
	web.Render(w, r, pluginDocsDialog(name, body, ""))
}

// pluginDocNameFor reports the name of the loaded plugin that documents the
// feed URL, or "" when none does. Unlike the owning plugin, this is matched on
// CapDocs, so a feed fetched by the generic parser (a reddit .rss) still finds
// the plugin that explains its categories.
func (s *Server) pluginDocNameFor(feedURL string) string {
	if s.plugins == nil || s.plugins.Empty() {
		return ""
	}
	u, err := url.Parse(feedURL)
	if err != nil {
		return ""
	}
	return s.plugins.MatchDocs(u)
}
