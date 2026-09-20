package filestore

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"
)

// IsLocalDefault reports whether cfg points at the built-in local SeaweedFS
// fallback (i.e. S3_ENDPOINT was not set).
func IsLocalDefault(cfg Config) bool {
	return cfg.Endpoint == "http://127.0.0.1:8333"
}

func portOf(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "8333"
	}
	if _, port, err := net.SplitHostPort(u.Host); err == nil && port != "" {
		return port
	}
	return "8333"
}

var probeClient = &http.Client{Timeout: 2 * time.Second}

// httpUp reports whether the endpoint answers HTTP.
func httpUp(ctx context.Context, endpoint string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false
	}
	resp, err := probeClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

// waitReady polls the endpoint until it answers or ctx expires.
func waitReady(ctx context.Context, endpoint string) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if httpUp(ctx, endpoint) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for SeaweedFS at %s", endpoint)
		case <-ticker.C:
		}
	}
}
