package killthenewsletter

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/metruzanca/nanoflux/pluginapi"
)

// TestMain pins Host so an NF_KTN_HOST in the developer's environment does not
// change the fixtures.
func TestMain(m *testing.M) {
	Host = "kill-the-newsletter.com"
	os.Exit(m.Run())
}

// hostFunc is a test Host whose Do is the given function.
type hostFunc func(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error)

func (h hostFunc) Do(ctx context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	return h(ctx, req)
}
func (hostFunc) Now() time.Time      { return time.Now().UTC() }
func (hostFunc) Logf(string, ...any) {}

func TestCanonicalFeedURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://kill-the-newsletter.com/feeds/abc123", "https://kill-the-newsletter.com/feeds/abc123.xml"},
		{"https://kill-the-newsletter.com/feeds/abc123.xml", "https://kill-the-newsletter.com/feeds/abc123.xml"},
		{"http://www.kill-the-newsletter.com/feeds/abc123", "https://kill-the-newsletter.com/feeds/abc123.xml"},
		{"https://kill-the-newsletter.com/", "https://kill-the-newsletter.com/"},
		{"https://example.com/feeds/abc123", "https://example.com/feeds/abc123"},
	}
	for _, c := range cases {
		if got := canonicalFeedURL(c.in); got != c.want {
			t.Errorf("canonicalFeedURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFeedToken(t *testing.T) {
	if got := feedToken("https://kill-the-newsletter.com/feeds/abc123.xml"); got != "abc123" {
		t.Errorf("feedToken = %q, want abc123", got)
	}
	if got := feedToken("https://example.com/feeds/abc123.xml"); got != "" {
		t.Errorf("feedToken(non-ktn) = %q, want empty", got)
	}
}

func TestDeriveFeed(t *testing.T) {
	c, ok := deriveFeed("https://kill-the-newsletter.com/feeds/abc123")
	if !ok || !c.Derived || c.FeedURL != "https://kill-the-newsletter.com/feeds/abc123.xml" {
		t.Fatalf("deriveFeed = %+v ok=%v", c, ok)
	}
	if _, ok := deriveFeed("https://kill-the-newsletter.com/"); ok {
		t.Fatal("derived a feed from the root")
	}
}

func TestSettings(t *testing.T) {
	fields := Plugin{}.Settings("https://kill-the-newsletter.com/feeds/xyz789.xml")
	if len(fields) != 1 || fields[0].Value != "xyz789@"+Host {
		t.Fatalf("Settings = %+v", fields)
	}
	if got := (Plugin{}).Settings("https://example.com/x"); got != nil {
		t.Fatalf("Settings(non-ktn) = %+v, want nil", got)
	}
}

func TestProvision(t *testing.T) {
	var gotReq pluginapi.HTTPRequest
	h := hostFunc(func(_ context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		gotReq = req
		return pluginapi.HTTPResponse{
			Status: 200,
			Body:   []byte(`{"feedId":"abc123","email":"abc123@kill-the-newsletter.com","feed":"https://kill-the-newsletter.com/feeds/abc123.xml"}`),
		}, nil
	})
	got, err := Plugin{}.Provision(context.Background(), pluginapi.ProvisionRequest{Title: "My News"}, h)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if !strings.Contains(gotReq.URL, "/feeds") || gotReq.Method != "POST" {
		t.Errorf("request = %s %s", gotReq.Method, gotReq.URL)
	}
	if gotReq.Headers["csrf-protection"] != "true" {
		t.Errorf("missing csrf header: %v", gotReq.Headers)
	}
	if got.FeedURL != "https://kill-the-newsletter.com/feeds/abc123.xml" {
		t.Errorf("FeedURL = %q", got.FeedURL)
	}
	if len(got.Fields) != 1 || got.Fields[0].Value != "abc123@kill-the-newsletter.com" {
		t.Errorf("Fields = %+v", got.Fields)
	}
}

func TestProvisionFillsMissingFields(t *testing.T) {
	h := hostFunc(func(_ context.Context, _ pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		return pluginapi.HTTPResponse{Status: 200, Body: []byte(`{"feedId":"zzz"}`)}, nil
	})
	got, err := Plugin{}.Provision(context.Background(), pluginapi.ProvisionRequest{Title: "T"}, h)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if got.FeedURL != "https://kill-the-newsletter.com/feeds/zzz.xml" {
		t.Errorf("FeedURL = %q", got.FeedURL)
	}
	if got.Fields[0].Value != "zzz@"+Host {
		t.Errorf("email = %q", got.Fields[0].Value)
	}
}

func TestActionSave(t *testing.T) {
	var body string
	h := hostFunc(func(_ context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		body = string(req.Body)
		return pluginapi.HTTPResponse{Status: 200}, nil
	})
	_, err := Plugin{}.Action(context.Background(), pluginapi.FeedActionRequest{
		FeedURL: "https://kill-the-newsletter.com/feeds/abc123.xml",
		Action:  "save",
		Fields:  map[string]string{"title": "New Title", "icon": ""},
	}, h)
	if err != nil {
		t.Fatalf("Action: %v", err)
	}
	if !strings.Contains(body, "title=New+Title") || !strings.Contains(body, "icon=") {
		t.Errorf("body = %q", body)
	}
}

func TestActionDelete(t *testing.T) {
	var method string
	h := hostFunc(func(_ context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		method = req.Method
		return pluginapi.HTTPResponse{Status: 200}, nil
	})
	res, err := Plugin{}.Action(context.Background(), pluginapi.FeedActionRequest{
		FeedURL: "https://kill-the-newsletter.com/feeds/abc123.xml",
		Action:  "delete",
	}, h)
	if err != nil {
		t.Fatalf("Action: %v", err)
	}
	if method != "DELETE" || !res.Deleted {
		t.Errorf("method=%q deleted=%v", method, res.Deleted)
	}
}

func TestActionRateLimit(t *testing.T) {
	h := hostFunc(func(_ context.Context, _ pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		return pluginapi.HTTPResponse{Status: 429, RateLimited: true, RetryAfter: time.Minute}, nil
	})
	_, err := Plugin{}.Action(context.Background(), pluginapi.FeedActionRequest{
		FeedURL: "https://kill-the-newsletter.com/feeds/abc123.xml", Action: "delete",
	}, h)
	if _, ok := err.(*pluginapi.RateLimit); !ok {
		t.Fatalf("err = %v, want RateLimit", err)
	}
}

func TestMatch(t *testing.T) {
	p := Plugin{}
	u, _ := url.Parse("https://kill-the-newsletter.com/feeds/abc123.xml")
	if !p.Match(u, pluginapi.CapFeedAdmin) || !p.Match(u, pluginapi.CapProvision) {
		t.Error("expected provision + feed admin to match")
	}
	if p.Match(u, pluginapi.CapFetch) {
		t.Error("plugin must not claim fetch")
	}
	other, _ := url.Parse("https://example.com/")
	if p.Match(other, pluginapi.CapFeedAdmin) {
		t.Error("non-ktn host matched")
	}
}

func TestParseProvisionResponse(t *testing.T) {
	id, email, feedURL := parseProvisionResponse([]byte(`{"feedId":"a1","email":"a1@h","feed":"https://h/feeds/a1.xml"}`))
	if id != "a1" || email != "a1@h" || feedURL != "https://h/feeds/a1.xml" {
		t.Fatalf("got %q %q %q", id, email, feedURL)
	}
	if id, _, _ := parseProvisionResponse([]byte("not json")); id != "" {
		t.Fatalf("expected empty on bad json, got %q", id)
	}
}
