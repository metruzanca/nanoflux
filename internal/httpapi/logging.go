package httpapi

import (
	"net/http"
	"time"

	"github.com/charmbracelet/log"
)

// statusWriter records the response status so the logger can see it.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// logRequests logs every request with method, path, status, and duration.
// Successes are Info, client errors Warn, server errors Error.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(sw, r)

		keyvals := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"dur", time.Since(start).Round(time.Microsecond),
		}
		switch {
		case sw.status >= 500:
			log.Error("request", keyvals...)
		case sw.status >= 400:
			log.Warn("request", keyvals...)
		default:
			log.Info("request", keyvals...)
		}
	})
}
