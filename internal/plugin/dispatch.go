package plugin

import (
	"context"
	"net/url"

	"github.com/metruzanca/nanoflux/internal/feedparse"
	"github.com/metruzanca/nanoflux/pluginapi"
)

// mustParse parses raw as a URL, returning nil when it cannot (a nil URL never
// matches any plugin).
func mustParse(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	return u
}

// Dispatcher adapts the Registry to feedparse's plugin hook, converting between
// the pluginapi and feedparse result types. It also implements
// feedparse.Plugin so installing it routes every Fetch through matching
// plugins.
type Dispatcher struct {
	reg   *Registry
	hosts func(pluginapi.Fetcher) pluginapi.Host
}

// NewDispatcher builds a Dispatcher over reg. hosts supplies the Host a plugin
// uses (in-process for native, brokered for external).
func NewDispatcher(reg *Registry, hosts func(pluginapi.Fetcher) pluginapi.Host) *Dispatcher {
	return &Dispatcher{reg: reg, hosts: hosts}
}

// Install registers the dispatcher as feedparse's plugin hook.
func (d *Dispatcher) Install() { feedparse.SetPlugin(d) }

// MatchFetch reports whether any plugin handles the request's feed URL.
func (d *Dispatcher) MatchFetch(req feedparse.FetchRequest) bool {
	if d.reg.Empty() {
		return false
	}
	return d.reg.Match(mustParse(req.URL), pluginapi.CapFetch) != nil
}

// FetchPlugin runs the matching plugin and converts its result.
func (d *Dispatcher) FetchPlugin(ctx context.Context, req feedparse.FetchRequest) (feedparse.Result, error) {
	f := d.reg.Match(mustParse(req.URL), pluginapi.CapFetch)
	if f == nil {
		return feedparse.Result{}, &feedparse.StatusError{Code: 404, URL: req.URL}
	}
	res, err := f.Fetch(ctx, pluginapi.FetchRequest{URL: req.URL, ETag: req.ETag, LastModified: req.LastModified}, d.hosts(f))
	if err != nil {
		return feedparse.Result{}, convertError(err)
	}
	return toFeedparseResult(res), nil
}

// toFeedparseResult converts a pluginapi.Result into feedparse's model.
func toFeedparseResult(res pluginapi.Result) feedparse.Result {
	out := feedparse.Result{
		Feed: feedparse.Feed{
			Title:       res.Feed.Title,
			HomeURL:     res.Feed.HomeURL,
			Description: res.Feed.Description,
			ImageURL:    res.Feed.ImageURL,
		},
		ETag:         res.ETag,
		LastModified: res.LastModified,
		NextPageURL:  res.NextPageURL,
	}
	for _, it := range res.Items {
		fi := feedparse.Item{
			GUID:        it.GUID,
			Identity:    it.Identity,
			Title:       it.Title,
			Link:        it.Link,
			Summary:     it.Summary,
			ImageURL:    it.ImageURL,
			PublishedAt: it.PublishedAt,
		}
		for _, e := range it.Enclosures {
			fi.Enclosures = append(fi.Enclosures, feedparse.Enclosure{URL: e.URL, MIMEType: e.MIMEType, Length: e.Length})
		}
		out.Items = append(out.Items, fi)
	}
	return out
}

// convertError maps a plugin error to feedparse's typed errors so the poller's
// rate-limit handling keeps working.
func convertError(err error) error {
	switch e := err.(type) {
	case *pluginapi.RateLimit:
		return &feedparse.RateLimitError{URL: e.URL, Status: e.Status, RetryAfter: e.RetryAfter}
	case *pluginapi.StatusError:
		return &feedparse.StatusError{Code: e.Code, URL: e.URL}
	default:
		return err
	}
}
