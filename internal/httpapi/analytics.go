package httpapi

import (
	"context"
	"net/http"

	"github.com/metruzanca/nanoflux/internal/config"
)

type analyticsCtxKey struct{}

// withAnalytics stores the analytics config in a request context.
func withAnalytics(ctx context.Context, a config.AnalyticsConfig) context.Context {
	return context.WithValue(ctx, analyticsCtxKey{}, a)
}

// analyticsFrom reads the analytics config the middleware stored; the zero
// value (disabled) when it was never set.
func analyticsFrom(ctx context.Context) config.AnalyticsConfig {
	a, _ := ctx.Value(analyticsCtxKey{}).(config.AnalyticsConfig)
	return a
}

// analyticsMiddleware attaches the analytics config to every request that may
// render a full document, so the shared head can emit the tracker without
// changing its signature. It runs for public share pages and the landing page
// too, unlike navCountsMiddleware, because those are still site pages.
func (s *Server) analyticsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.cfg.Analytics.Enabled() {
			next.ServeHTTP(w, r)
			return
		}
		ctx := withAnalytics(r.Context(), s.cfg.Analytics)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
