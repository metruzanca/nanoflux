package patreon

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/metruzanca/nanoflux/pluginapi"
)

type hostFunc func(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error)

func (h hostFunc) Do(ctx context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	return h(ctx, req)
}
func (hostFunc) Now() time.Time      { return time.Now().UTC() }
func (hostFunc) Logf(string, ...any) {}

func TestVanity(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"https://www.patreon.com/cw/chrisandjack", "chrisandjack"},
		{"https://www.patreon.com/chrisandjack", "chrisandjack"},
		{"https://www.patreon.com/api/campaigns/1/posts", ""},
		{"https://www.patreon.com/user?u=1", ""},
		{"https://example.com/x", ""},
	}
	for _, c := range cases {
		u, _ := url.Parse(c.raw)
		if got := vanityOf(u); got != c.want {
			t.Errorf("vanityOf(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

func TestDocText(t *testing.T) {
	raw := `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"Hello"}]},{"type":"paragraph","content":[{"type":"text","text":"World"}]}]}`
	if got := docText(raw); got != "Hello\nWorld" {
		t.Errorf("docText = %q", got)
	}
	if got := docText("not json"); got != "" {
		t.Errorf("non-doc = %q", got)
	}
}

func TestImageURL(t *testing.T) {
	if got := imageURL([]byte(`"https://cdn/x.jpg"`)); got != "https://cdn/x.jpg" {
		t.Errorf("string shape = %q", got)
	}
	if got := imageURL([]byte(`{"large_url":"https://cdn/large.jpg","url":"https://cdn/small.jpg"}`)); got != "https://cdn/large.jpg" {
		t.Errorf("object shape = %q", got)
	}
}

func TestFetchResolvesCampaign(t *testing.T) {
	h := hostFunc(func(_ context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		switch {
		case hasPrefix(req.URL, "https://www.patreon.com/api/campaigns?filter[vanity]"):
			return pluginapi.HTTPResponse{Status: 200, Body: []byte(`{"data":[{"id":"42","attributes":{"name":"Chris & Jack","url":"https://www.patreon.com/chrisandjack","summary":"s","avatar_photo_url":"https://cdn/a.jpg"}}]}`)}, nil
		case hasPrefix(req.URL, "https://www.patreon.com/api/campaigns/42/posts"):
			return pluginapi.HTTPResponse{Status: 200, Body: []byte(`{"data":[{"id":"9","attributes":{"title":"Post","content":"body","url":"https://www.patreon.com/posts/9","published_at":"2026-01-02T03:04:05Z"}}],"links":{"next":"https://www.patreon.com/api/campaigns/42/posts?cursor=x"}}`)}, nil
		default:
			return pluginapi.HTTPResponse{Status: 404}, nil
		}
	})
	res, err := Plugin{}.Fetch(context.Background(), pluginapi.FetchRequest{URL: "https://www.patreon.com/chrisandjack"}, h)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.Feed.Title != "Chris & Jack" || res.Feed.ImageURL != "https://cdn/a.jpg" {
		t.Errorf("feed = %+v", res.Feed)
	}
	if len(res.Items) != 1 || res.Items[0].GUID != "patreon:9" {
		t.Fatalf("items = %+v", res.Items)
	}
	if res.Items[0].PublishedAt != "2026-01-02 03:04:05" {
		t.Errorf("published = %q", res.Items[0].PublishedAt)
	}
	if res.NextPageURL == "" {
		t.Error("next page cursor should be carried")
	}
}

func hasPrefix(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }
