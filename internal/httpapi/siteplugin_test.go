package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/metruzanca/nanoflux/internal/plugin"
	"github.com/metruzanca/nanoflux/pluginapi"
)

// The HTTP layer's contract with site plugins is tested with small plugin
// fakes. The real implementations live in their own plugin modules and test
// their own behavior there.

// fakeSitePlugin implements the URL policy, discovery, decoration and proxy
// bypass behavior the httpapi tests need, using reddit URL shapes as fixtures.
// The real implementation lives in its own plugin module.
type fakeSitePlugin struct{}

func (fakeSitePlugin) Meta() pluginapi.Meta {
	return pluginapi.Meta{Name: "reddit", APIVersion: pluginapi.APIVersion}
}

func (fakeSitePlugin) Match(u *url.URL, cap pluginapi.Capability) bool {
	if u == nil {
		return false
	}
	switch cap {
	case pluginapi.CapBypassProxy:
		return fakeMediaHost(u.Hostname())
	case pluginapi.CapDiscover, pluginapi.CapURLPolicy, pluginapi.CapDecorate,
		pluginapi.CapSharedKey, pluginapi.CapRender, pluginapi.CapDocs:
		return fakeSiteHost(u.Hostname())
	default:
		return false
	}
}

func (fakeSitePlugin) Discover(_ context.Context, pageURL string, _ pluginapi.Host) ([]pluginapi.Candidate, error) {
	c, ok := fakeDeriveSite(pageURL)
	if !ok {
		return nil, pluginapi.ErrUnsupportedCapability
	}
	return []pluginapi.Candidate{c}, nil
}

func (fakeSitePlugin) Fetch(context.Context, pluginapi.FetchRequest, pluginapi.Host) (pluginapi.Result, error) {
	return pluginapi.Result{}, pluginapi.ErrUnsupportedCapability
}

func (fakeSitePlugin) CanonicalizeFeedURL(raw string) string {
	if c, ok := fakeDeriveSite(raw); ok {
		return c.FeedURL
	}
	return raw
}

func (fakeSitePlugin) FeedToken(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if name, ok := fakeUserURL(u); ok {
		return "u/" + name
	}
	if sub, ok := fakeSectionURL(u); ok {
		return "r/" + sub
	}
	return ""
}

func (fakeSitePlugin) SharedKeys(_ context.Context, req pluginapi.SharedKeyRequest, _ pluginapi.Host) ([]pluginapi.ItemSharedKey, error) {
	var out []pluginapi.ItemSharedKey
	for i, it := range req.Items {
		if strings.HasPrefix(it.GUID, "t3_") {
			out = append(out, pluginapi.ItemSharedKey{Index: i, SharedKey: "reddit:" + it.GUID})
		}
	}
	return out, nil
}

var fakeLinkAnchorRe = regexp.MustCompile(`href="([^"]+)"[^>]*>\s*\[link\]\s*</a>`)

func (fakeSitePlugin) Decorate(_ context.Context, req pluginapi.DecorateRequest) ([]pluginapi.Decorated, error) {
	out := make([]pluginapi.Decorated, 0, len(req.Items))
	for i, it := range req.Items {
		var sub, user string
		for _, c := range it.Categories {
			switch {
			case strings.HasPrefix(c, "r/") && sub == "":
				sub = c
			case strings.HasPrefix(c, "u/") && user == "":
				user = c
			}
		}
		d := pluginapi.Decorated{Index: i}
		d.Kind, d.ThumbURL = fakeSiteKind(it)
		if sub != "" && user != "" {
			d.Attribution = []pluginapi.SourcePart{
				{Text: sub, Token: sub, URL: "https://www.reddit.com/" + sub + "/"},
				{Text: "by"},
				{Text: user, Token: user, URL: "https://www.reddit.com/user/" + strings.TrimPrefix(user, "u/") + "/"},
			}
			if m := fakeLinkAnchorRe.FindStringSubmatch(it.Summary); len(m) == 2 {
				d.DedupeKey = strings.ToLower(user) + "|" + m[1]
			}
		}
		out = append(out, d)
	}
	return out, nil
}

func (fakeSitePlugin) Render(context.Context, pluginapi.RenderRequest, pluginapi.Host) (pluginapi.Media, error) {
	return pluginapi.Media{}, nil
}

// fakeSiteKind classifies an item from its stored thumbnail, matching the
// fixture plugin's view-time classification closely enough for the row tests.
func fakeSiteKind(it pluginapi.Item) (pluginapi.ItemKind, string) {
	u, err := url.Parse(it.ImageURL)
	if err != nil || u.Host == "" {
		return pluginapi.KindText, ""
	}
	switch u.Hostname() {
	case "external-preview.redd.it":
		return pluginapi.KindLink, ""
	case "preview.redd.it":
		if u.Query().Get("crop") != "1:1,smart" {
			return pluginapi.KindText, ""
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) != 1 {
			return pluginapi.KindGallery, ""
		}
		id, ext, ok := strings.Cut(parts[0], ".")
		if !ok || id == "" || ext == "" {
			return pluginapi.KindGallery, ""
		}
		if ext == "jpeg" {
			ext = "jpg"
		}
		return pluginapi.KindGallery, "https://i.redd.it/" + id + "." + ext
	}
	return pluginapi.KindText, ""
}

func fakeSiteHost(host string) bool {
	host = strings.ToLower(host)
	return host == "reddit.com" || strings.HasSuffix(host, ".reddit.com")
}

func fakeMediaHost(host string) bool {
	host = strings.ToLower(host)
	for _, h := range []string{"redd.it", "redditmedia.com", "redditstatic.com"} {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}

func fakeUserURL(u *url.URL) (string, bool) {
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) >= 2 && (parts[0] == "u" || parts[0] == "user") && parts[1] != "" {
		return parts[1], true
	}
	return "", false
}

func fakeSectionURL(u *url.URL) (string, bool) {
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) >= 2 && parts[0] == "r" && parts[1] != "" {
		return strings.TrimSuffix(parts[1], ".rss"), true
	}
	return "", false
}

func fakeDeriveSite(raw string) (pluginapi.Candidate, bool) {
	u, err := url.Parse(raw)
	if err != nil || !fakeSiteHost(u.Hostname()) {
		return pluginapi.Candidate{}, false
	}
	if name, ok := fakeUserURL(u); ok {
		home := "https://www.reddit.com/user/" + name
		return pluginapi.Candidate{
			FeedURL:    home + "/submitted.rss",
			Title:      "u/" + name,
			AuthorName: name,
			HomeURL:    home,
			Derived:    true,
		}, true
	}
	if sub, ok := fakeSectionURL(u); ok {
		home := "https://www.reddit.com/r/" + sub
		return pluginapi.Candidate{
			FeedURL: home + "/.rss",
			Title:   "r/" + sub,
			HomeURL: home,
			Derived: true,
		}, true
	}
	return pluginapi.Candidate{}, false
}

// fakeVideoPlugin covers the view-time render and card classification the
// httpapi tests assert. The real implementation lives in its own plugin module.
type fakeVideoPlugin struct{}

func (fakeVideoPlugin) Meta() pluginapi.Meta {
	return pluginapi.Meta{Name: "youtube", APIVersion: pluginapi.APIVersion}
}

func (fakeVideoPlugin) Match(u *url.URL, cap pluginapi.Capability) bool {
	if u == nil {
		return false
	}
	switch cap {
	case pluginapi.CapRender, pluginapi.CapDecorate:
		return fakeVideoID(u) != ""
	default:
		return false
	}
}

func (fakeVideoPlugin) Discover(context.Context, string, pluginapi.Host) ([]pluginapi.Candidate, error) {
	return nil, pluginapi.ErrUnsupportedCapability
}

func (fakeVideoPlugin) Fetch(context.Context, pluginapi.FetchRequest, pluginapi.Host) (pluginapi.Result, error) {
	return pluginapi.Result{}, pluginapi.ErrUnsupportedCapability
}

func (fakeVideoPlugin) Render(_ context.Context, req pluginapi.RenderRequest, _ pluginapi.Host) (pluginapi.Media, error) {
	u, _ := url.Parse(req.Link)
	id := fakeVideoID(u)
	if id == "" {
		return pluginapi.Media{}, nil
	}
	return pluginapi.Media{EmbedSrc: "https://www.youtube.com/embed/" + id}, nil
}

func (fakeVideoPlugin) Decorate(_ context.Context, req pluginapi.DecorateRequest) ([]pluginapi.Decorated, error) {
	out := make([]pluginapi.Decorated, 0, len(req.Items))
	for i, it := range req.Items {
		u, _ := url.Parse(it.Link)
		id := fakeVideoID(u)
		if id == "" {
			out = append(out, pluginapi.Decorated{Index: i})
			continue
		}
		thumb := ""
		if it.ImageURL == "" {
			thumb = "https://i.ytimg.com/vi/" + id + "/hqdefault.jpg"
		}
		out = append(out, pluginapi.Decorated{
			Index:    i,
			Kind:     pluginapi.KindVideo,
			ThumbURL: thumb,
		})
	}
	return out, nil
}

func fakeVideoID(u *url.URL) string {
	if u == nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if host == "youtu.be" {
		return strings.Trim(u.Path, "/")
	}
	if host == "youtube.com" || strings.HasSuffix(host, ".youtube.com") {
		return u.Query().Get("v")
	}
	return ""
}

func useSitePlugin(t *testing.T, s *Server) {
	t.Helper()
	reg := plugin.NewRegistry()
	reg.RegisterExternal(fakeSitePlugin{})
	s.SetPlugins(reg, plugin.NewHosts(s.client, plugin.NewCooldown()))
	s.store.SetURLPolicy(plugin.NewStoreURLPolicy(reg))
	s.store.SetItemDecorator(plugin.NewStoreDecorator(reg))
}

func useVideoPlugin(t *testing.T, s *Server) {
	t.Helper()
	reg := plugin.NewRegistry()
	reg.RegisterExternal(fakeVideoPlugin{})
	s.SetPlugins(reg, plugin.NewHosts(s.client, plugin.NewCooldown()))
	s.store.SetItemDecorator(plugin.NewStoreDecorator(reg))
}

func useSiteAndVideoPlugins(t *testing.T, s *Server) {
	t.Helper()
	reg := plugin.NewRegistry()
	reg.RegisterExternal(fakeSitePlugin{})
	reg.RegisterExternal(fakeVideoPlugin{})
	s.SetPlugins(reg, plugin.NewHosts(s.client, plugin.NewCooldown()))
	s.store.SetURLPolicy(plugin.NewStoreURLPolicy(reg))
	s.store.SetItemDecorator(plugin.NewStoreDecorator(reg))
}

// fakeIconPlugin supplies a built-in site icon for one host.
type fakeIconPlugin struct{ fakeSitePlugin }

func (fakeIconPlugin) Meta() pluginapi.Meta {
	return pluginapi.Meta{Name: "icons", APIVersion: pluginapi.APIVersion}
}
func (fakeIconPlugin) Match(u *url.URL, cap pluginapi.Capability) bool {
	return u != nil && cap == pluginapi.CapIcon && u.Hostname() == "icons.example"
}
func (fakeIconPlugin) Icon(string) (string, []byte) {
	return "image/svg+xml", []byte("<svg>plugin icon</svg>")
}

// TestSourceIconFromPlugin proves the /icons route falls back to a plugin's
// built-in icon before the generic globe.
func TestSourceIconFromPlugin(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	reg := plugin.NewRegistry()
	reg.RegisterExternal(fakeIconPlugin{})
	s.SetPlugins(reg, plugin.NewHosts(s.client, plugin.NewCooldown()))

	got := doGet(h, "/icons/icons.example", cookie)
	if got.Code != http.StatusOK || strings.TrimSpace(got.Body.String()) != "<svg>plugin icon</svg>" {
		t.Fatalf("plugin icon = %d %q", got.Code, got.Body.String())
	}
	fallback := doGet(h, "/icons/plain.example", cookie)
	if fallback.Code != http.StatusOK || !strings.Contains(fallback.Body.String(), "<svg") ||
		strings.Contains(fallback.Body.String(), "plugin icon") {
		t.Fatalf("fallback icon = %d %q", fallback.Code, fallback.Body.String())
	}
}
