package httpapi

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// loginLimiter throttles failed logins per client IP so a publicly exposed
// instance can't be brute-forced. After maxFailures within a window an IP is
// blocked until the window lapses; a successful login clears the count. The
// state is in-memory (single process) and windows are expired lazily.
type loginLimiter struct {
	mu          sync.Mutex
	byIP        map[string]*attempts
	maxFailures int
	window      time.Duration
}

type attempts struct {
	count int
	start time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{
		byIP:        map[string]*attempts{},
		maxFailures: 5,
		window:      15 * time.Minute,
	}
}

// blocked reports whether ip is currently locked out.
func (l *loginLimiter) blocked(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, ok := l.byIP[ip]
	if !ok {
		return false
	}
	if time.Since(a.start) >= l.window {
		delete(l.byIP, ip)
		return false
	}
	return a.count >= l.maxFailures
}

// fail records a failed attempt for ip.
func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, ok := l.byIP[ip]
	if !ok || time.Since(a.start) >= l.window {
		a = &attempts{start: time.Now()}
		l.byIP[ip] = a
	}
	a.count++
	// Opportunistic cleanup so the map doesn't grow without bound.
	if len(l.byIP) > 4096 {
		for k, v := range l.byIP {
			if time.Since(v.start) >= l.window {
				delete(l.byIP, k)
			}
		}
	}
}

// success clears the failure count for ip after a valid login.
func (l *loginLimiter) success(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.byIP, ip)
}

// clientIP extracts the client's IP from the request. Behind a reverse proxy
// this is the proxy's address unless X-Forwarded-For is forwarded (not trusted
// by default).
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
