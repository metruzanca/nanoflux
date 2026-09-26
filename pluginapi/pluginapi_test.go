package pluginapi

import (
	"errors"
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

func TestItemRoundTrip(t *testing.T) {
	in := []Item{{
		GUID: "g", Identity: "id-1", Title: "t", Link: "l", Summary: "s", ImageURL: "i",
		PublishedAt: "2026-01-01 00:00:00",
		Enclosures:  []Enclosure{{URL: "u", MIMEType: "audio/mpeg", Length: 42}},
	}}
	out := fromPBItems(toPBItems(in))
	if len(out) != 1 || out[0].GUID != "g" || out[0].Identity != "id-1" ||
		len(out[0].Enclosures) != 1 || out[0].Enclosures[0].Length != 42 {
		t.Fatalf("item round trip = %+v", out)
	}
}
