package pluginapi

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"
)

func TestErrorRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		in   error
		want func(error) bool
	}{
		{"unsupported", ErrUnsupportedCapability, func(e error) bool { return errors.Is(e, ErrUnsupportedCapability) }},
		{"ratelimit", &RateLimit{URL: "https://x", Status: 429, RetryAfter: 5 * time.Minute}, func(e error) bool {
			var rl *RateLimit
			return errors.As(e, &rl) && rl.Status == 429 && rl.RetryAfter == 5*time.Minute
		}},
		{"status", &StatusError{Code: 404, URL: "https://x"}, func(e error) bool {
			var se *StatusError
			return errors.As(e, &se) && se.Code == 404
		}},
		{"plain", errors.New("boom"), func(e error) bool { return e != nil && e.Error() == "boom" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pb := toPBError(c.in)
			if pb == nil {
				t.Fatal("toPBError returned nil")
			}
			back := fromPBError(pb)
			if !c.want(back) {
				t.Fatalf("round trip of %v gave %v", c.in, back)
			}
		})
	}
	if toPBError(nil) != nil {
		t.Fatal("toPBError(nil) should be nil")
	}
}

func TestMetaDocsRoundTrip(t *testing.T) {
	// A Fetcher that also documents itself.
	p := &metaDocPlugin{}
	_, hasDocs := interface{}(p).(Docser)
	if !hasDocs {
		t.Fatal("metaDocPlugin should implement Docser")
	}
	if got := p.Docs(); got != "# hi" {
		t.Fatalf("Docs() = %q", got)
	}
	// A Fetcher without Docs is not a Docser.
	if _, ok := interface{}(plainPlugin{}).(Docser); ok {
		t.Fatal("plainPlugin should not implement Docser")
	}
}

type metaDocPlugin struct{}

func (metaDocPlugin) Meta() Meta { return Meta{Name: "d", APIVersion: APIVersion} }
func (metaDocPlugin) Match(*url.URL, Capability) bool {
	return false
}
func (metaDocPlugin) Discover(context.Context, string, Host) ([]Candidate, error) {
	return nil, ErrUnsupportedCapability
}
func (metaDocPlugin) Fetch(context.Context, FetchRequest, Host) (Result, error) {
	return Result{}, nil
}
func (metaDocPlugin) Docs() string { return "# hi" }

type plainPlugin struct{}

func (plainPlugin) Meta() Meta                      { return Meta{Name: "p", APIVersion: APIVersion} }
func (plainPlugin) Match(*url.URL, Capability) bool { return false }
func (plainPlugin) Discover(context.Context, string, Host) ([]Candidate, error) {
	return nil, ErrUnsupportedCapability
}
func (plainPlugin) Fetch(context.Context, FetchRequest, Host) (Result, error) {
	return Result{}, nil
}

func TestItemRoundTrip(t *testing.T) {
	in := []Item{{
		GUID: "g", Identity: "id-1", Title: "t", Link: "l", Summary: "s", ImageURL: "i",
		PublishedAt: "2026-01-01 00:00:00",
		Categories:  []string{"reblog"},
		Enclosures:  []Enclosure{{URL: "u", MIMEType: "audio/mpeg", Length: 42}},
	}}
	out := fromPBItems(toPBItems(in))
	if len(out) != 1 || out[0].GUID != "g" || out[0].Identity != "id-1" ||
		len(out[0].Enclosures) != 1 || out[0].Enclosures[0].Length != 42 {
		t.Fatalf("item round trip = %+v", out)
	}
	if len(out[0].Categories) != 1 || out[0].Categories[0] != "reblog" {
		t.Fatalf("categories round trip = %+v", out[0].Categories)
	}
}
