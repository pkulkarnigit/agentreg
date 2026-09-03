package middleware

import (
	"log/slog"
	"net/http"
	"time"
)

// Logging wraps a handler with structured request logging (method, path,
// status, latency, remote addr) via the stdlib log/slog — no new
// dependency, and independent of any particular route's business logic.
// The level scales with the response: a 5xx is a server-side failure
// worth an ERROR (it's the thing an operator actually needs to notice), a
// 4xx is a WARN (a client did something the server rejected — could be
// routine, could be someone probing), and everything else is INFO. A
// health check hitting /healthz is DEBUG instead of INFO — it says
// nothing about real traffic and would otherwise drown out everything
// else in the log at the default level.
func Logging(logger *slog.Logger, next http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)

		level := slog.LevelInfo
		switch {
		case sw.status >= 500:
			level = slog.LevelError
		case sw.status >= 400:
			level = slog.LevelWarn
		case r.URL.Path == "/healthz":
			level = slog.LevelDebug
		}
		logger.Log(r.Context(), level, "request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"remote_addr", r.RemoteAddr,
		)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
