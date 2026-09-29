package killthenewsletter

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/metruzanca/nanoflux/pluginapi"
)

// hostFunc is a test Host whose Do is the given function.
type hostFunc func(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error)

func (h hostFunc) Do(ctx context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	return h(ctx, req)
}
func (hostFunc) Now() time.Time      { return time.Now().UTC() }
func (hostFunc) Logf(string, ...any) {}

// ktn is a plugin pointed at the public instance, the default.
func ktn() *Plugin { return New() }

func TestConfigureBaseURL(t *testing.T) {
	p := ktn()
	if p.baseURL() != defaultBaseURL {
		t.Fatalf("default base = %q", p.baseURL())
	}
	// An empty value keeps the default.
	p.Configure(map[string]string{"base_url": ""})
	if p.baseURL() != defaultBaseURL {
		t.Fatalf("empty base = %q", p.baseURL())
	}
	// A bare host gets an https scheme; a path is dropped.
	p.Configure(map[string]string{"base_url": "news.example.com/some/path"})
	if p.baseURL() != "https://news.example.com" {
		t.Fatalf("configured base = %q", p.baseURL())
	}
	// A self-hosted base changes what Match recognizes.
	u, _ := url.Parse("https://news.example.com/feeds/abc123.xml")
	if !p.Match(u, pluginapi.CapFeedAdmin) {
		t.Fatal("configured host should match")
	}
	official, _ := url.Parse("https://kill-the-newsletter.com/feeds/abc.xml")
	if p.Match(official, pluginapi.CapFeedAdmin) {
		t.Fatal("official host should no longer match after configuring a self-hosted base")
	}
}

func TestCanonicalFeedURL(t *testing.T) {
	p := ktn()
	cases := []struct{ in, want string }{
		{"https://kill-the-newsletter.com/feeds/abc123", "https://kill-the-newsletter.com/feeds/abc123.xml"},
		{"https://kill-the-newsletter.com/feeds/abc123.xml", "https://kill-the-newsletter.com/feeds/abc123.xml"},
		{"http://www.kill-the-newsletter.com/feeds/abc123", "https://kill-the-newsletter.com/feeds/abc123.xml"},
		{"https://kill-the-newsletter.com/", "https://kill-the-newsletter.com/"},
		{"https://example.com/feeds/abc123", "https://example.com/feeds/abc123"},
	}
	for _, c := range cases {
		if got := p.CanonicalizeFeedURL(c.in); got != c.want {
			t.Errorf("CanonicalizeFeedURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFeedToken(t *testing.T) {
	p := ktn()
	if got := p.FeedToken("https://kill-the-newsletter.com/feeds/abc123.xml"); got != "abc123" {
		t.Errorf("FeedToken = %q, want abc123", got)
	}
	if got := p.FeedToken("https://example.com/feeds/abc123.xml"); got != "" {
		t.Errorf("FeedToken(non-ktn) = %q, want empty", got)
	}
}

func TestDiscover(t *testing.T) {
	p := ktn()
	cs, err := p.Discover(context.Background(), "https://kill-the-newsletter.com/feeds/abc123", nil)
	if err != nil || len(cs) != 1 || !cs[0].Derived || cs[0].FeedURL != "https://kill-the-newsletter.com/feeds/abc123.xml" {
		t.Fatalf("Discover = %+v err=%v", cs, err)
	}
	if _, err := p.Discover(context.Background(), "https://kill-the-newsletter.com/", nil); err != pluginapi.ErrUnsupportedCapability {
		t.Fatalf("Discover(root) err = %v, want unsupported", err)
	}
}

func TestFeedFields(t *testing.T) {
	p := ktn()
	fields := p.FeedFields("https://kill-the-newsletter.com/feeds/xyz789.xml")
	if len(fields) != 1 || fields[0].Value != "xyz789@kill-the-newsletter.com" {
		t.Fatalf("FeedFields = %+v", fields)
	}
	if got := p.FeedFields("https://example.com/x"); got != nil {
		t.Fatalf("FeedFields(non-ktn) = %+v, want nil", got)
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
	got, err := ktn().Provision(context.Background(), pluginapi.ProvisionRequest{Title: "My News"}, h)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if gotReq.URL != "https://kill-the-newsletter.com/feeds" || gotReq.Method != "POST" {
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

func TestProvisionUsesConfiguredBase(t *testing.T) {
	p := ktn()
	p.Configure(map[string]string{"base_url": "https://news.example.com"})
	var gotReq pluginapi.HTTPRequest
	h := hostFunc(func(_ context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		gotReq = req
		return pluginapi.HTTPResponse{Status: 200, Body: []byte(`{"feedId":"zzz"}`)}, nil
	})
	got, err := p.Provision(context.Background(), pluginapi.ProvisionRequest{Title: "T"}, h)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if gotReq.URL != "https://news.example.com/feeds" {
		t.Errorf("request URL = %q", gotReq.URL)
	}
	if got.FeedURL != "https://news.example.com/feeds/zzz.xml" {
		t.Errorf("FeedURL = %q", got.FeedURL)
	}
	if got.Fields[0].Value != "zzz@news.example.com" {
		t.Errorf("email = %q", got.Fields[0].Value)
	}
}

func TestActionSave(t *testing.T) {
	var body string
	h := hostFunc(func(_ context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		body = string(req.Body)
		return pluginapi.HTTPResponse{Status: 200}, nil
	})
	_, err := ktn().Action(context.Background(), pluginapi.FeedActionRequest{
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
	res, err := ktn().Action(context.Background(), pluginapi.FeedActionRequest{
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
	_, err := ktn().Action(context.Background(), pluginapi.FeedActionRequest{
		FeedURL: "https://kill-the-newsletter.com/feeds/abc123.xml", Action: "delete",
	}, h)
	if _, ok := err.(*pluginapi.RateLimit); !ok {
		t.Fatalf("err = %v, want RateLimit", err)
	}
}

func TestMatch(t *testing.T) {
	p := ktn()
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
