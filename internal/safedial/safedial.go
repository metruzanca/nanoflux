// Package safedial builds HTTP clients that refuse to connect to private,
// loopback or link-local addresses. Feeds, discovery pages, avatars and images
// are all user-supplied URLs, so an unguarded client would let a user turn the
// server into a probe for its own network (cloud metadata, internal services).
//
// Set NF_ALLOW_PRIVATE_FETCH=1 to opt out, for a self-hoster whose feeds live
// on the local network.
package safedial

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Client returns an *http.Client whose dialer refuses non-public addresses.
// The policy is read from NF_ALLOW_PRIVATE_FETCH at construction time.
func Client(timeout time.Duration) *http.Client {
	return ClientWithPolicy(timeout, allowPrivateFromEnv())
}

// ClientWithPolicy is Client with the policy passed explicitly, for callers
// that already resolved it (and for tests).
func ClientWithPolicy(timeout time.Duration, allowPrivate bool) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           dialContext(allowPrivate),
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: time.Second,
		},
	}
}

// dialContext resolves the host itself and dials an allowed IP directly, so a
// DNS rebind between the resolve and the connect cannot slip a private address
// past the check (the transport never re-resolves a name we already dialed).
func dialContext(allowPrivate bool) func(context.Context, string, string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if allowPrivate {
			return dialer.DialContext(ctx, network, addr)
		}
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		var lastErr error
		for _, ip := range ips {
			if !Allowed(ip.IP) {
				lastErr = fmt.Errorf("safedial: refusing non-public address %s", ip.IP)
				continue
			}
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("safedial: no address for %s", host)
		}
		return nil, lastErr
	}
}

// blockedNets are ranges net.IP's own predicates do not cover but that must
// never be reachable: "this network", benchmarking, TEST-NET, CGNAT, reserved
// and the limited broadcast address.
var blockedNets = func() []*net.IPNet {
	cidrs := []string{
		"0.0.0.0/8",
		"100.64.0.0/10",
		"192.0.0.0/24",
		"192.0.2.0/24",
		"198.18.0.0/15",
		"198.51.100.0/24",
		"203.0.113.0/24",
		"240.0.0.0/4",
		"255.255.255.255/32",
		"::/128",
		"2001:db8::/32",
	}
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		if _, n, err := net.ParseCIDR(c); err == nil {
			out = append(out, n)
		}
	}
	return out
}()

// Allowed reports whether ip is a public address the server may connect to.
func Allowed(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsPrivate() {
		return false
	}
	for _, n := range blockedNets {
		if n.Contains(ip) {
			return false
		}
	}
	return true
}

func allowPrivateFromEnv() bool {
	v := strings.TrimSpace(os.Getenv("NF_ALLOW_PRIVATE_FETCH"))
	if v == "" {
		return false
	}
	b, err := strconv.ParseBool(v)
	return err == nil && b
}
