// Package plugin hosts feed integrations. A plugin implements
// pluginapi.Fetcher; it may be "native" (compiled into nanoflux and called
// in-process) or "external" (an executable loaded over gRPC at startup). The
// Registry holds both behind one interface, and Host implements pluginapi.Host
// so every outbound request is mediated and rate-limit aware.
package plugin

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/charmbracelet/log"

	"github.com/metruzanca/nanoflux/internal/feedparse"
	"github.com/metruzanca/nanoflux/pluginapi"
)

// Host implements pluginapi.Host for native plugins: HTTP goes through the
// shared client and every response is checked for rate limits, which cool the
// request's registrable host.
type Host struct {
	client *http.Client
	cool   *Cooldown
	name   string
	ua     string // User-Agent for mediated requests; "" uses the app default
}

// NewHost builds a Host. name prefixes log lines (the plugin name); ua overrides
// the User-Agent when non-empty.
func NewHost(client *http.Client, cool *Cooldown, name, ua string) *Host {
	return &Host{client: client, cool: cool, name: name, ua: ua}
}

func (h *Host) Do(ctx context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	method := req.Method
	if method == "" {
		method = http.MethodGet
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, req.URL, strings.NewReader(string(req.Body)))
	if err != nil {
		return pluginapi.HTTPResponse{}, err
	}
	// The host owns the User-Agent: the plugin's declared override, else the app
	// default. A plugin cannot set it per-request.
	ua := h.ua
	if ua == "" {
		ua = feedparse.UserAgent()
	}
	httpReq.Header.Set("User-Agent", ua)
	for k, v := range req.Headers {
		if strings.EqualFold(k, "User-Agent") {
			continue
		}
		httpReq.Header.Set(k, v)
	}

	// Refuse to hit a host that is currently cooling from a rate limit. Return it
	// inline (not as an error) so the signal survives the gRPC boundary.
	if h.cool != nil && h.cool.Cooling(req.URL) {
		return pluginapi.HTTPResponse{Status: http.StatusTooManyRequests, RateLimited: true, RetryAfter: time.Minute}, nil
	}

	resp, err := h.client.Do(httpReq)
	if err != nil {
		return pluginapi.HTTPResponse{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return pluginapi.HTTPResponse{}, err
	}
	if feedparse.IsRateLimited(resp) {
		backoff := feedparse.RateLimitBackoff(resp)
		if h.cool != nil {
			h.cool.Cool(req.URL, time.Now().Add(backoff))
		}
		// The host cools the request host, but still returns the raw response to
		// the plugin (marked RateLimited) so the plugin may recover from its own
		// cache. If it cannot, it returns a RateLimit and the feed is parked.
		return pluginapi.HTTPResponse{
			Status: resp.StatusCode, Headers: headerMap(resp.Header), Body: data,
			RateLimited: true, RetryAfter: backoff,
		}, nil
	}
	return pluginapi.HTTPResponse{Status: resp.StatusCode, Headers: headerMap(resp.Header), Body: data}, nil
}

func (h *Host) Now() time.Time { return time.Now().UTC() }

func (h *Host) Logf(format string, args ...any) {
	log.Info("plugin "+h.name+": "+format, args...)
}

func headerMap(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		if len(v) > 0 {
			out[k] = v[0]
		}
	}
	return out
}
