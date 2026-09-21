package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/store"
)

const (
	// SessionCookieName is the httpOnly session cookie for the web UI.
	SessionCookieName = "rss_session"
	// SessionTTL is the lifetime of a session, extended on activity.
	SessionTTL = 30 * 24 * time.Hour
)

// Authenticator creates and resolves sessions. Sessions are stored in the
// database; the web UI sends them as an httpOnly cookie and the browser
// extension sends the same token as `Authorization: Bearer <token>`.
type Authenticator struct {
	store *store.Store
}

func New(st *store.Store) *Authenticator {
	return &Authenticator{store: st}
}

// CreateSession mints a new token for the user and stores it.
func (a *Authenticator) CreateSession(userID int64) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := hex.EncodeToString(buf)
	expires := db.FormatTime(time.Now().Add(SessionTTL))
	if err := a.store.Sessions.Create(userID, token, expires); err != nil {
		return "", err
	}
	return token, nil
}

// SetCookie sets the session cookie for the web UI. The Secure flag is derived
// from the request so it is only set when the client connection was HTTPS —
// either TLS terminated at the app or forwarded by a reverse proxy
// (X-Forwarded-Proto/X-Forwarded-Ssl). Plain-HTTP LAN and dev installs keep a
// non-Secure cookie.
func (a *Authenticator) SetCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   SecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(SessionTTL.Seconds()),
		Expires:  time.Now().Add(SessionTTL),
	})
}

// ClearCookie expires the session cookie.
func (a *Authenticator) ClearCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   SecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
		Expires:  time.Unix(1, 0),
	})
}

// SecureRequest reports whether the client connection is (or was, via a
// reverse proxy) HTTPS.
func SecureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Ssl"), "on")
}

// Token returns the session token from the cookie or Authorization header.
func Token(r *http.Request) string {
	if c, err := r.Cookie(SessionCookieName); err == nil {
		return c.Value
	}
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return ""
}

// User resolves the session from the cookie or Authorization header.
func (a *Authenticator) User(r *http.Request) (store.User, error) {
	token := Token(r)
	if token == "" {
		return store.User{}, store.ErrNotFound
	}
	u, err := a.store.Sessions.UserByToken(token)
	if err != nil {
		return store.User{}, err
	}
	// Sliding session: extend expiry, ignore errors.
	a.store.Sessions.Touch(token, db.FormatTime(time.Now().Add(SessionTTL)))
	return u, nil
}

type ctxKey int

const userCtxKey ctxKey = 0

// WithUser stores the authenticated user in the request context.
func WithUser(r *http.Request, u store.User) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), userCtxKey, u))
}

// UserFrom returns the authenticated user stored by the middleware.
func UserFrom(r *http.Request) (store.User, bool) {
	u, ok := r.Context().Value(userCtxKey).(store.User)
	return u, ok
}

// Require wraps a handler, rejecting unauthenticated requests. Web requests
// redirect to /login; JSON API requests get a 401.
func (a *Authenticator) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, err := a.User(r)
		if err != nil {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			// Send GETs back to where they were headed after login.
			target := "/login"
			if r.Method == http.MethodGet {
				target += "?next=" + url.QueryEscape(r.URL.RequestURI())
			}
			http.Redirect(w, r, target, http.StatusFound)
			return
		}
		next.ServeHTTP(w, WithUser(r, u))
	})
}
