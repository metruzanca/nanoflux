// Package plugin hosts feed integrations. A plugin is an executable that
// implements pluginapi.Fetcher and is loaded over gRPC at startup. Host
// implements pluginapi.Host so every outbound request is mediated and
// rate-limit aware.
package plugin

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/charmbracelet/log"

	"github.com/metruzanca/nanoflux/internal/feedparse"
	"github.com/metruzanca/nanoflux/internal/store"
	"github.com/metruzanca/nanoflux/pluginapi"
)

// Host implements pluginapi.Host for a plugin's brokered calls: HTTP goes
// through the shared client and every response is checked for rate limits,
// which cool the request's registrable host.
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
		log.Debug("plugin request refused: host cooling",
			"plugin", h.name, "host", store.RegistrableDomain(req.URL), "url", req.URL)
		return pluginapi.HTTPResponse{Status: http.StatusTooManyRequests, RateLimited: true, RetryAfter: time.Minute}, nil
	}

	client := h.client
	if req.NoRedirect {
		// Copy so the per-request policy does not mutate the shared client.
		c := *h.client
		c.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
		client = &c
	}
	resp, err := client.Do(httpReq)
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
		host := store.RegistrableDomain(req.URL)
		if h.cool != nil {
			h.cool.Cool(req.URL, time.Now().Add(backoff))
		}
		// Surface the limit at the shared-cooldown chokepoint, so logs show a
		// plugin limit pacing the same host the poller uses.
		log.Warn("plugin rate limited",
			"plugin", h.name, "host", host, "url", req.URL,
			"status", resp.StatusCode, "backoff", backoff.Round(time.Second),
		)
		// The host cools the request host, but still returns the raw response to
		// the plugin (marked RateLimited) so the plugin may recover from its own
		// cache. If it cannot, it returns a RateLimit and the feed is parked.
		return pluginapi.HTTPResponse{
			Status: resp.StatusCode, Headers: headerMap(resp.Header), Body: data,
			RateLimited: true, RetryAfter: backoff, SetCookies: resp.Header.Values("Set-Cookie"),
		}, nil
	}
	return pluginapi.HTTPResponse{
		Status: resp.StatusCode, Headers: headerMap(resp.Header), Body: data,
		SetCookies: resp.Header.Values("Set-Cookie"),
	}, nil
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
