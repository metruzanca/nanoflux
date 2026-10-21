package httpapi

import (
	"context"
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

// fakeRedditPlugin implements the URL policy, discovery, decoration and proxy
// bypass behavior the httpapi tests need. Media rendering is tested in the
// reddit plugin module.
type fakeRedditPlugin struct{}

func (fakeRedditPlugin) Meta() pluginapi.Meta {
	return pluginapi.Meta{Name: "reddit", APIVersion: pluginapi.APIVersion}
}

func (fakeRedditPlugin) Match(u *url.URL, cap pluginapi.Capability) bool {
	if u == nil {
		return false
	}
	switch cap {
	case pluginapi.CapBypassProxy:
		return fakeRedditMediaHost(u.Hostname())
	case pluginapi.CapDiscover, pluginapi.CapURLPolicy, pluginapi.CapDecorate,
		pluginapi.CapSharedKey, pluginapi.CapRender, pluginapi.CapDocs:
		return fakeRedditHost(u.Hostname())
	default:
		return false
	}
}

func (fakeRedditPlugin) Discover(_ context.Context, pageURL string, _ pluginapi.Host) ([]pluginapi.Candidate, error) {
	c, ok := fakeDeriveReddit(pageURL)
	if !ok {
		return nil, pluginapi.ErrUnsupportedCapability
	}
	return []pluginapi.Candidate{c}, nil
}

func (fakeRedditPlugin) Fetch(context.Context, pluginapi.FetchRequest, pluginapi.Host) (pluginapi.Result, error) {
	return pluginapi.Result{}, pluginapi.ErrUnsupportedCapability
}

func (fakeRedditPlugin) CanonicalizeFeedURL(raw string) string {
	if c, ok := fakeDeriveReddit(raw); ok {
		return c.FeedURL
	}
	return raw
}

func (fakeRedditPlugin) FeedToken(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if name, ok := fakeRedditUser(u); ok {
		return "u/" + name
	}
	if sub, ok := fakeRedditSub(u); ok {
		return "r/" + sub
	}
	return ""
}

func (fakeRedditPlugin) SharedKeys(_ context.Context, req pluginapi.SharedKeyRequest, _ pluginapi.Host) ([]pluginapi.ItemSharedKey, error) {
	var out []pluginapi.ItemSharedKey
	for i, it := range req.Items {
		if strings.HasPrefix(it.GUID, "t3_") {
			out = append(out, pluginapi.ItemSharedKey{Index: i, SharedKey: "reddit:" + it.GUID})
		}
	}
	return out, nil
}

var fakeLinkAnchorRe = regexp.MustCompile(`href="([^"]+)"[^>]*>\s*\[link\]\s*</a>`)

func (fakeRedditPlugin) Decorate(_ context.Context, req pluginapi.DecorateRequest) ([]pluginapi.Decorated, error) {
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
		d.Kind, d.ThumbURL = fakeRedditKind(it)
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

func (fakeRedditPlugin) Render(context.Context, pluginapi.RenderRequest, pluginapi.Host) (pluginapi.Media, error) {
	return pluginapi.Media{}, nil
}

// fakeRedditKind classifies an item from its stored thumbnail, matching the
// reddit plugin's view-time classification closely enough for the row tests.
func fakeRedditKind(it pluginapi.Item) (pluginapi.ItemKind, string) {
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

func fakeRedditHost(host string) bool {
	host = strings.ToLower(host)
	return host == "reddit.com" || strings.HasSuffix(host, ".reddit.com")
}

func fakeRedditMediaHost(host string) bool {
	host = strings.ToLower(host)
	for _, h := range []string{"redd.it", "redditmedia.com", "redditstatic.com"} {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}

func fakeRedditUser(u *url.URL) (string, bool) {
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) >= 2 && (parts[0] == "u" || parts[0] == "user") && parts[1] != "" {
		return parts[1], true
	}
	return "", false
}

func fakeRedditSub(u *url.URL) (string, bool) {
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) >= 2 && parts[0] == "r" && parts[1] != "" {
		return strings.TrimSuffix(parts[1], ".rss"), true
	}
	return "", false
}

func fakeDeriveReddit(raw string) (pluginapi.Candidate, bool) {
	u, err := url.Parse(raw)
	if err != nil || !fakeRedditHost(u.Hostname()) {
		return pluginapi.Candidate{}, false
	}
	if name, ok := fakeRedditUser(u); ok {
		home := "https://www.reddit.com/user/" + name
		return pluginapi.Candidate{
			FeedURL:    home + "/submitted.rss",
			Title:      "u/" + name,
			AuthorName: name,
			HomeURL:    home,
			Derived:    true,
		}, true
	}
	if sub, ok := fakeRedditSub(u); ok {
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

// fakeYouTubePlugin covers the view-time render and card classification the
// httpapi tests assert. The real implementation lives in its own plugin module.
type fakeYouTubePlugin struct{}

func (fakeYouTubePlugin) Meta() pluginapi.Meta {
	return pluginapi.Meta{Name: "youtube", APIVersion: pluginapi.APIVersion}
}

func (fakeYouTubePlugin) Match(u *url.URL, cap pluginapi.Capability) bool {
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

func (fakeYouTubePlugin) Discover(context.Context, string, pluginapi.Host) ([]pluginapi.Candidate, error) {
	return nil, pluginapi.ErrUnsupportedCapability
}

func (fakeYouTubePlugin) Fetch(context.Context, pluginapi.FetchRequest, pluginapi.Host) (pluginapi.Result, error) {
	return pluginapi.Result{}, pluginapi.ErrUnsupportedCapability
}

func (fakeYouTubePlugin) Render(_ context.Context, req pluginapi.RenderRequest, _ pluginapi.Host) (pluginapi.Media, error) {
	u, _ := url.Parse(req.Link)
	id := fakeVideoID(u)
	if id == "" {
		return pluginapi.Media{}, nil
	}
	return pluginapi.Media{EmbedSrc: "https://www.youtube.com/embed/" + id}, nil
}

func (fakeYouTubePlugin) Decorate(_ context.Context, req pluginapi.DecorateRequest) ([]pluginapi.Decorated, error) {
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

func useRedditPlugin(t *testing.T, s *Server) {
	t.Helper()
	reg := plugin.NewRegistry()
	reg.RegisterExternal(fakeRedditPlugin{})
	s.SetPlugins(reg, plugin.NewHosts(s.client, plugin.NewCooldown()))
	s.store.SetURLPolicy(plugin.NewStoreURLPolicy(reg))
	s.store.SetItemDecorator(plugin.NewStoreDecorator(reg))
}

func useYouTubePlugin(t *testing.T, s *Server) {
	t.Helper()
	reg := plugin.NewRegistry()
	reg.RegisterExternal(fakeYouTubePlugin{})
	s.SetPlugins(reg, plugin.NewHosts(s.client, plugin.NewCooldown()))
	s.store.SetItemDecorator(plugin.NewStoreDecorator(reg))
}

func useRedditAndYouTube(t *testing.T, s *Server) {
	t.Helper()
	reg := plugin.NewRegistry()
	reg.RegisterExternal(fakeRedditPlugin{})
	reg.RegisterExternal(fakeYouTubePlugin{})
	s.SetPlugins(reg, plugin.NewHosts(s.client, plugin.NewCooldown()))
	s.store.SetURLPolicy(plugin.NewStoreURLPolicy(reg))
	s.store.SetItemDecorator(plugin.NewStoreDecorator(reg))
}
