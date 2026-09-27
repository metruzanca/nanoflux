package bluesky

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/metruzanca/nanoflux/pluginapi"
)

// hostFunc adapts a function to pluginapi.Host for tests.
type hostFunc struct {
	do func(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error)
}

func (h hostFunc) Do(ctx context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	return h.do(ctx, req)
}
func (hostFunc) Now() time.Time      { return time.Now().UTC() }
func (hostFunc) Logf(string, ...any) {}

func TestMatch(t *testing.T) {
	p := Plugin{}
	cases := []struct {
		raw  string
		cap  pluginapi.Capability
		want bool
	}{
		{"https://bsky.app/profile/metru.dev", pluginapi.CapDiscover, true},
		{"https://bsky.app/profile/metru.dev/rss", pluginapi.CapDiscover, false},
		{"https://bsky.app/profile/metru.dev", pluginapi.CapFetch, false},
		{"https://bsky.app/profile/metru.dev/rss", pluginapi.CapFetch, true},
		{"https://bsky.app/profile/did:plc:x/rss", pluginapi.CapFetch, true},
		{"https://bsky.app/profile/metru.dev/post/3muzboeg6ek2d", pluginapi.CapFetch, false},
		{"https://bsky.app/about", pluginapi.CapDiscover, false},
		{"https://example.com/profile/x", pluginapi.CapDiscover, false},
	}
	for _, c := range cases {
		u, _ := url.Parse(c.raw)
		if got := p.Match(u, c.cap); got != c.want {
			t.Errorf("Match(%s, cap=%d) = %v, want %v", c.raw, c.cap, got, c.want)
		}
	}
}

// bskyHost serves the mock profile, record list, and blob endpoints the plugin
// reads. profileJSON and recordsJSON are the raw response bodies.
func bskyHost(t *testing.T, profileJSON []byte, recordsJSON []byte) hostFunc {
	t.Helper()
	return hostFunc{do: func(_ context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		switch {
		case strings.Contains(req.URL, "/xrpc/app.bsky.actor.getProfile"):
			return pluginapi.HTTPResponse{Status: 200, Body: profileJSON}, nil
		case strings.Contains(req.URL, "/xrpc/com.atproto.repo.listRecords"):
			return pluginapi.HTTPResponse{Status: 200, Body: recordsJSON}, nil
		}
		return pluginapi.HTTPResponse{Status: 404}, nil
	}}
}

func profileBody(did, handle, name string) []byte {
	b, _ := json.Marshal(map[string]any{
		"did": did, "handle": handle, "displayName": name,
		"avatar": "https://cdn.bsky.app/img/avatar/plain/" + did + "/av", "description": "bio",
	})
	return b
}

// recordsBody wraps post records in a listRecords response.
func recordsBody(t *testing.T, recs ...map[string]any) []byte {
	t.Helper()
	items := make([]map[string]any, 0, len(recs))
	for _, r := range recs {
		items = append(items, map[string]any{
			"uri":   "at://did:plc:test/app.bsky.feed.post/" + r["rkey"].(string),
			"cid":   "cid-" + r["rkey"].(string),
			"value": r,
		})
	}
	b, _ := json.Marshal(map[string]any{"records": items})
	return b
}

func TestFetchBuildsItems(t *testing.T) {
	did := "did:plc:test"
	recs := recordsBody(t,
		map[string]any{
			"rkey": "aaa", "$type": "app.bsky.feed.post",
			"text": "Hello world\nsecond line", "createdAt": "2026-09-18T22:41:52.040Z",
			"facets": []any{map[string]any{
				"index":    map[string]any{"byteStart": 0, "byteEnd": 5},
				"features": []any{map[string]any{"$type": "app.bsky.richtext.facet#link", "uri": "https://example.com"}},
			}},
		},
		map[string]any{
			"rkey": "bbb", "$type": "app.bsky.feed.post",
			"text": "a reply", "createdAt": "2026-09-17T22:49:46.615Z",
			"reply": map[string]any{"root": map[string]any{"uri": "at://x/app.bsky.feed.post/1"}},
		},
	)
	p := Plugin{}
	h := bskyHost(t, profileBody(did, "metru.dev", "metru"), recs)
	res, err := p.Fetch(context.Background(), pluginapi.FetchRequest{URL: "https://bsky.app/profile/metru.dev/rss"}, h)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("items = %d, want 1 (reply skipped)", len(res.Items))
	}
	it := res.Items[0]
	if it.GUID != "at://did:plc:test/app.bsky.feed.post/aaa" || it.Identity != it.GUID {
		t.Errorf("guid/identity = %q/%q", it.GUID, it.Identity)
	}
	if it.Link != "https://bsky.app/profile/metru.dev/post/aaa" {
		t.Errorf("link = %q", it.Link)
	}
	if it.Title != "Hello world" {
		t.Errorf("title = %q, want first line", it.Title)
	}
	if it.PublishedAt != "2026-09-18 22:41:52" {
		t.Errorf("published = %q", it.PublishedAt)
	}
	if !strings.Contains(it.Summary, `href="https://example.com"`) {
		t.Errorf("facet link missing: %s", it.Summary)
	}
	if !strings.Contains(it.Summary, "second line") {
		t.Errorf("rest of text missing: %s", it.Summary)
	}
	if res.Feed.Title != "@metru.dev - metru" {
		t.Errorf("feed title = %q", res.Feed.Title)
	}
}

func TestFetchImagesAndVideo(t *testing.T) {
	did := "did:plc:test"
	recs := recordsBody(t,
		map[string]any{
			"rkey": "img", "$type": "app.bsky.feed.post", "text": "two pics",
			"createdAt": "2026-08-11T15:01:41.231Z",
			"embed": map[string]any{
				"$type": "app.bsky.embed.images",
				"images": []any{
					map[string]any{"image": map[string]any{"ref": map[string]any{"$link": "cid1"}, "mimeType": "image/jpeg", "size": 100}},
					map[string]any{"image": map[string]any{"ref": map[string]any{"$link": "cid2"}, "mimeType": "image/png", "size": 200}},
				},
			},
		},
		map[string]any{
			"rkey": "vid", "$type": "app.bsky.feed.post", "text": "",
			"createdAt": "2026-09-08T14:54:16.819Z",
			"embed": map[string]any{
				"$type": "app.bsky.embed.video",
				"video": map[string]any{"ref": map[string]any{"$link": "vcid"}, "mimeType": "video/mp4", "size": 300},
			},
		},
	)
	p := Plugin{}
	h := bskyHost(t, profileBody(did, "metru.dev", "metru"), recs)
	res, err := p.Fetch(context.Background(), pluginapi.FetchRequest{URL: "https://bsky.app/profile/metru.dev/rss"}, h)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(res.Items))
	}
	img := res.Items[0]
	if img.ImageURL != imageURL(did, "cid1") {
		t.Errorf("image url = %q", img.ImageURL)
	}
	if len(img.Enclosures) != 2 || img.Enclosures[1].MIMEType != "image/png" {
		t.Fatalf("image enclosures = %+v", img.Enclosures)
	}
	if img.Title != "two pics" {
		t.Errorf("title = %q", img.Title)
	}
	vid := res.Items[1]
	if len(vid.Enclosures) != 1 {
		t.Fatalf("video enclosures = %+v", vid.Enclosures)
	}
	if vid.Enclosures[0].URL != blobURL(did, "vcid") || vid.Enclosures[0].MIMEType != "video/mp4" {
		t.Errorf("video enclosure = %+v", vid.Enclosures[0])
	}
	if vid.ImageURL != videoThumbURL(did, "vcid") {
		t.Errorf("video thumb = %q", vid.ImageURL)
	}
	if vid.Title != "video" {
		t.Errorf("video title = %q", vid.Title)
	}
}

func TestFetchExternalAndQuote(t *testing.T) {
	did := "did:plc:test"
	recs := recordsBody(t,
		map[string]any{
			"rkey": "ext", "$type": "app.bsky.feed.post", "text": "check this",
			"createdAt": "2026-08-17T18:57:07.512Z",
			"embed": map[string]any{
				"$type": "app.bsky.embed.external",
				"external": map[string]any{
					"uri": "https://blog.example/ci", "title": "Run your own CI", "description": "It is worth it",
					"thumb": map[string]any{"ref": map[string]any{"$link": "thumbcid"}, "mimeType": "image/jpeg"},
				},
			},
		},
		map[string]any{
			"rkey": "quote", "$type": "app.bsky.feed.post", "text": "agreed",
			"createdAt": "2026-08-16T10:00:00.000Z",
			"embed": map[string]any{
				"$type":  "app.bsky.embed.record",
				"record": map[string]any{"record": map[string]any{"uri": "at://did:plc:other/app.bsky.feed.post/xyz", "cid": "c"}},
			},
		},
	)
	p := Plugin{}
	h := bskyHost(t, profileBody(did, "metru.dev", "metru"), recs)
	res, err := p.Fetch(context.Background(), pluginapi.FetchRequest{URL: "https://bsky.app/profile/metru.dev/rss"}, h)
	if err != nil {
		t.Fatal(err)
	}
	ext := res.Items[0]
	if !strings.Contains(ext.Summary, "Run your own CI") || !strings.Contains(ext.Summary, `href="https://blog.example/ci"`) {
		t.Errorf("external card missing: %s", ext.Summary)
	}
	if ext.ImageURL != imageURL(did, "thumbcid") {
		t.Errorf("external thumb = %q", ext.ImageURL)
	}
	q := res.Items[1]
	if !strings.Contains(q.Summary, `href="https://bsky.app/profile/did:plc:other/post/xyz"`) {
		t.Errorf("quote link missing: %s", q.Summary)
	}
}

func TestFetchRejectsNonProfile(t *testing.T) {
	p := Plugin{}
	h := bskyHost(t, nil, nil)
	if _, err := p.Fetch(context.Background(), pluginapi.FetchRequest{URL: "https://bsky.app/about"}, h); err == nil {
		t.Fatal("expected error for non-profile URL")
	}
}

func TestDiscoverUsesDisplayName(t *testing.T) {
	did := "did:plc:test"
	h := bskyHost(t, profileBody(did, "metru.dev", "metru"), nil)
	p := Plugin{}
	cs, err := p.Discover(context.Background(), "https://bsky.app/profile/metru.dev", h)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 {
		t.Fatalf("candidates = %d", len(cs))
	}
	c := cs[0]
	if c.FeedURL != "https://bsky.app/profile/"+did+"/rss" || c.Title != "@metru.dev - metru" {
		t.Errorf("candidate = %+v", c)
	}
	if c.IconURL == "" || c.HomeURL != "https://bsky.app/profile/metru.dev" {
		t.Errorf("candidate meta = %+v", c)
	}
}

func TestTitleFromText(t *testing.T) {
	if got := titleFromText("\n\n  hello  \nworld"); got != "hello" {
		t.Errorf("title = %q", got)
	}
	if got := titleFromText("   "); got != "" {
		t.Errorf("blank title = %q", got)
	}
	long := strings.Repeat("x", 150)
	got := titleFromText(long)
	if len([]rune(got)) != 101 || !strings.HasSuffix(got, "…") {
		t.Errorf("clamped = %q (%d runes)", got, len([]rune(got)))
	}
}

func TestFeatureURL(t *testing.T) {
	cases := []struct {
		f    facetFeature
		want string
	}{
		{facetFeature{Type: "app.bsky.richtext.facet#link", URI: "https://x"}, "https://x"},
		{facetFeature{Type: "app.bsky.richtext.facet#mention", DID: "did:plc:a"}, "https://bsky.app/profile/did:plc:a"},
		{facetFeature{Type: "app.bsky.richtext.facet#tag", Tag: "golang"}, "https://bsky.app/hashtag/golang"},
		{facetFeature{Type: "app.bsky.richtext.facet#other"}, ""},
	}
	for _, c := range cases {
		if got := featureURL(c.f); got != c.want {
			t.Errorf("featureURL(%+v) = %q, want %q", c.f, got, c.want)
		}
	}
}
