package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
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

// SetCookie sets the session cookie for the web UI.
func (a *Authenticator) SetCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(SessionTTL.Seconds()),
		Expires:  time.Now().Add(SessionTTL),
	})
}

// ClearCookie expires the session cookie.
func (a *Authenticator) ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
		Expires:  time.Unix(1, 0),
	})
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
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		next.ServeHTTP(w, WithUser(r, u))
	})
}
