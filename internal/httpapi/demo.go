package httpapi

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/charmbracelet/log"

	"github.com/metruzanca/nanoflux/internal/auth"
	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/demo"
	"github.com/metruzanca/nanoflux/internal/store"
	"github.com/metruzanca/nanoflux/internal/web"
)

// SetDemo attaches the demo manager (nil when demo mode is off).
func (s *Server) SetDemo(m *demo.Manager) { s.demo = m }

// demoEnabled reports whether demo mode is active and a manager is wired.
func (s *Server) demoEnabled() bool { return s.demo != nil }

// landingData is the template input for the public marketing page.
type landingData struct {
	Demo        bool // demo mode: show "try the demo" as the primary CTA
	AllowSignup bool // non-demo installs: offer account creation
	Busy        bool // the visitor's IP already created an active demo
}

// demoStatus is per-request demo state carried to the topbar so it can render
// the countdown badge without every handler threading it through basePage.
type demoStatus struct {
	Active    bool
	ExpiresAt string // RFC3339 UTC, read by the client countdown
}

type demoStatusCtxKey struct{}

func withDemoStatus(r *http.Request, st demoStatus) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), demoStatusCtxKey{}, st))
}

// demoFrom reads the demo status the middleware stored; the zero value (inactive)
// when it was never computed.
func demoFrom(ctx context.Context) demoStatus {
	st, _ := ctx.Value(demoStatusCtxKey{}).(demoStatus)
	return st
}

// demoThrottle limits demo *creation* to one per client IP. It deliberately does
// not limit concurrent use: a visitor testing on desktop and phone should get a
// demo on both. It stops an unauthenticated script from minting unlimited
// throwaway accounts. The map is in-memory (single process) and entries expire
// when the demo they granted does.
type demoThrottle struct {
	mu   sync.Mutex
	byIP map[string]time.Time // IP -> when its granted demo expires
}

func newDemoThrottle() *demoThrottle {
	return &demoThrottle{byIP: map[string]time.Time{}}
}

// taken reports whether ip has already created a demo that is still active.
func (t *demoThrottle) taken(ip string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	exp, ok := t.byIP[ip]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(t.byIP, ip)
		return false
	}
	return true
}

// grant records that ip created a demo that expires at exp.
func (t *demoThrottle) grant(ip string, exp time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.byIP[ip] = exp
	// Opportunistic cleanup so the map cannot grow without bound.
	if len(t.byIP) > 4096 {
		now := time.Now()
		for k, v := range t.byIP {
			if now.After(v) {
				delete(t.byIP, k)
			}
		}
	}
}

// root serves the exact "/" route. A signed-in user gets their normal home; an
// anonymous visitor gets the public landing page. There is no redirect to
// /login, so the app's root doubles as its marketing page.
func (s *Server) root(w http.ResponseWriter, r *http.Request) {
	// The nav-counts middleware runs on GET / and already resolves the session
	// for signed-in users; only fall back to a store lookup for anonymous ones.
	if _, ok := auth.UserFrom(r); ok {
		s.home(w, r)
		return
	}
	if u, err := s.auth.User(r); err == nil {
		s.home(w, auth.WithUser(r, u))
		return
	}
	s.landing(w, r)
}

// landing renders the public marketing page. In demo mode the primary call to
// action provisions an ephemeral account; otherwise it points at login/signup.
func (s *Server) landing(w http.ResponseWriter, r *http.Request) {
	// An already-valid session shouldn't sit on the marketing page.
	if _, ok := auth.UserFrom(r); ok {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if _, err := s.auth.User(r); err == nil {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	busy := r.URL.Query().Get("demo") == "busy"
	web.Render(w, r, landingPage(landingData{
		Demo:        s.demoEnabled(),
		AllowSignup: s.allowSignup(),
		Busy:        busy,
	}))
}

// demoStart creates an ephemeral demo account and signs the visitor in. A
// visitor who already holds a valid demo session is sent straight through rather
// than given a second account. Creation (not use) is limited to one per IP.
func (s *Server) demoStart(w http.ResponseWriter, r *http.Request) {
	if !s.demoEnabled() {
		http.NotFound(w, r)
		return
	}
	// Reuse an existing valid session (demo or otherwise): never mint a second
	// account for the same browser.
	if _, err := s.auth.User(r); err == nil {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	ip := clientIP(r)
	if s.demoThrottle.taken(ip) {
		http.Redirect(w, r, "/?demo=busy", http.StatusFound)
		return
	}

	u, err := s.demo.Provision(r.Context())
	if err != nil {
		log.Error("demo provision", "err", err)
		http.Error(w, "could not start a demo right now", http.StatusInternalServerError)
		return
	}
	ttl := s.demo.TTL()
	token, err := s.auth.CreateSessionTTL(u.ID, ttl)
	if err != nil {
		log.Error("demo session", "err", err)
		http.Error(w, "could not start a demo right now", http.StatusInternalServerError)
		return
	}
	s.demoThrottle.grant(ip, time.Now().Add(ttl))
	s.auth.SetCookieTTL(w, r, token, ttl)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/", http.StatusFound)
}

// demoFeedLimitReached reports whether the user is an ephemeral demo account
// that has hit its add-feed cap. It is false for ordinary users and when demo
// mode is off, so it is always safe to call from the feed-create paths.
func (s *Server) demoFeedLimitReached(userID int64) bool {
	if !s.demoEnabled() {
		return false
	}
	reached, err := s.demo.AddFeedLimitReached(userID)
	if err != nil {
		log.Error("demo feed limit", "user_id", userID, "err", err)
		return false
	}
	return reached
}

// demoAddFeedMessage is the user-facing error shown when a demo user hits the
// add-feed cap.
func demoAddFeedMessage() string {
	return "demo limit reached — you can only add a few feeds in a demo"
}

// withDemoStatusFor resolves the demo countdown for a request, if any. It is a
// no-op outside demo mode.
func (s *Server) withDemoStatusFor(r *http.Request, u store.User) *http.Request {
	if !s.demoEnabled() {
		return r
	}
	ephemeral, expiresAt, err := s.store.Users.EphemeralStatus(u.ID)
	if err != nil || !ephemeral {
		return r
	}
	// Stored timestamps are UTC; convert to RFC3339 so the browser parses it as
	// an absolute instant rather than a local-time guess.
	iso := expiresAt
	if t, perr := db.ParseTime(expiresAt); perr == nil {
		iso = t.UTC().Format(time.RFC3339)
	}
	return withDemoStatus(r, demoStatus{Active: true, ExpiresAt: iso})
}
