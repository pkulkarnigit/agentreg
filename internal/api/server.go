// Package api implements the KrateAI REST API (HTTP only: decode request,
// call internal/registry, encode response). It never imports a concrete
// store implementation directly.
package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/pkulkarni/apreg/internal/api/middleware"
	"github.com/pkulkarni/apreg/internal/auth"
	"github.com/pkulkarni/apreg/internal/registry"
)

type Server struct {
	reg     *registry.Registry
	lockout auth.Lockout
}

// limiterFactory builds a named Limiter — name distinguishes the
// signup/login/publish buckets so a Redis-backed factory can prefix their
// keys separately even though they share one Redis instance.
type limiterFactory func(name string, burst int, per time.Duration) middleware.Limiter

// Option configures NewHandler's rate limiting and lockout backends. The
// zero value (no options) is the single-instance, in-memory default;
// WithLockout/WithLimiterFactory swap in Redis-backed implementations for
// a horizontally scaled deployment. See cmd/krate-server/main.go for how
// KRATE_REDIS_ADDR selects between them.
type Option func(*handlerConfig)

type handlerConfig struct {
	lockout    auth.Lockout
	newLimiter limiterFactory
	admins     map[string]bool
}

// WithLockout overrides the default in-memory login lockout.
func WithLockout(l auth.Lockout) Option {
	return func(c *handlerConfig) { c.lockout = l }
}

// WithLimiterFactory overrides how the signup/login/publish rate limiters
// are constructed.
func WithLimiterFactory(f limiterFactory) Option {
	return func(c *handlerConfig) { c.newLimiter = f }
}

// WithAdminUsernames grants admin-only endpoints (currently just backfilling
// a version's published_at — see handleAdminSetPublishedAt) to exactly
// these usernames. The zero value (no option passed) is an empty set:
// admin endpoints exist but nobody can reach them, the safe default for
// an instance that hasn't deliberately configured any admins.
func WithAdminUsernames(usernames []string) Option {
	return func(c *handlerConfig) {
		c.admins = make(map[string]bool, len(usernames))
		for _, u := range usernames {
			c.admins[u] = true
		}
	}
}

// Rate limits, chosen per the project plan's "Abuse & auth hardening"
// section: generous enough not to bother a real user, tight enough to
// blunt scripted abuse against a single-instance server. Exported so
// cmd/krate-server can reuse the exact same tuning when constructing
// Redis-backed limiters/lockout via the Option hooks above.
const (
	SignupBurst  = 5
	SignupPer    = time.Hour
	LoginBurst   = 10
	LoginPer     = time.Minute
	PublishBurst = 30
	PublishPer   = time.Hour

	LoginLockoutMaxFailures = 10
	LoginLockoutWindow      = 15 * time.Minute
)

// NewHandler builds the full /v1 API routing table.
func NewHandler(reg *registry.Registry, opts ...Option) http.Handler {
	cfg := &handlerConfig{
		lockout: auth.NewLoginLockout(LoginLockoutMaxFailures, LoginLockoutWindow),
		newLimiter: func(_ string, burst int, per time.Duration) middleware.Limiter {
			return middleware.NewRateLimiter(burst, per)
		},
	}
	for _, opt := range opts {
		opt(cfg)
	}

	s := &Server{reg: reg, lockout: cfg.lockout}
	mux := http.NewServeMux()

	signupLimiter := cfg.newLimiter("signup", SignupBurst, SignupPer)
	loginLimiter := cfg.newLimiter("login", LoginBurst, LoginPer)
	publishLimiter := cfg.newLimiter("publish", PublishBurst, PublishPer)

	mux.HandleFunc("POST /v1/users", middleware.RateLimit(signupLimiter, middleware.ByIP, s.handleSignup))
	mux.HandleFunc("POST /v1/users/verify", s.handleVerifyEmail)
	mux.HandleFunc("POST /v1/tokens", middleware.RateLimit(loginLimiter, middleware.ByIP, s.handleCreateToken))
	mux.HandleFunc("POST /v1/password-reset/request", middleware.RateLimit(loginLimiter, middleware.ByIP, s.handleRequestPasswordReset))
	mux.HandleFunc("POST /v1/password-reset/confirm", s.handleConfirmPasswordReset)

	mux.HandleFunc("PUT /v1/plugins/{scope}/{name}/{version}",
		middleware.RequireAuth(reg, middleware.RateLimit(publishLimiter, middleware.ByAuthenticatedUser, s.handlePublish)))
	mux.HandleFunc("PATCH /v1/admin/plugins/{scope}/{name}/{version}",
		middleware.RequireAuth(reg, middleware.RequireAdmin(cfg.admins, s.handleAdminSetPublishedAt)))
	mux.HandleFunc("GET /v1/plugins/{scope}/{name}", s.handleGetPlugin)
	mux.HandleFunc("GET /v1/plugins/{scope}/{name}/{version}", s.handleGetVersion)
	mux.HandleFunc("GET /v1/plugins/{scope}/{name}/{version}/download", s.handleDownload)
	mux.HandleFunc("GET /v1/search", s.handleSearch)

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	return mux
}

// maxJSONBodyBytes bounds every JSON-decoding endpoint's request body.
// Without this, an attacker can send a multi-gigabyte body to any of
// these small-payload endpoints (signup, login, verify, password reset)
// and exhaust server memory decoding it — the tarball upload path has its
// own, much larger cap (maxTarballBytes) for legitimate reasons; these
// don't need one.
const maxJSONBodyBytes = 1 << 20 // 1MiB

// decodeJSONBody reads and decodes a JSON request body capped at
// maxJSONBodyBytes, writing a 400 response and returning false on any
// failure (invalid JSON, oversized body, or empty body).
func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid or oversized JSON body")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// writeInternalError logs the real error at ERROR level — every 500 this
// API returns comes through here specifically so that whatever actually
// went wrong (a DB error, a filesystem error, whatever) is never just
// discarded in favor of the generic message the client sees. Without
// this, a 500 in production tells an operator nothing beyond "something
// failed" — which is exactly what every "internal error" response used
// to do before this existed.
func writeInternalError(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

func rawJSON(s string) json.RawMessage {
	if s == "" {
		return json.RawMessage("null")
	}
	return json.RawMessage(s)
}
