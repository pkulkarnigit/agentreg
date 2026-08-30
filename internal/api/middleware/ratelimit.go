package middleware

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// Limiter is anything that can answer "may this key make one more request
// right now." RateLimiter (below) is the in-memory, single-instance
// implementation; RedisLimiter (redislimiter.go) is the same contract
// backed by Redis, for when apreg-server runs as more than one replica and
// the buckets need to be shared rather than per-process.
type Limiter interface {
	Allow(key string) bool
}

// RateLimiter is an in-memory per-key token bucket. It's single-instance by
// design — each process has its own buckets, invisible to any other
// replica. Fine for a single apreg-server instance; swap in RedisLimiter
// once there's more than one.
type RateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    float64 // tokens added per second
	burst   float64 // max tokens a key can accumulate
}

type bucket struct {
	tokens   float64
	lastFill time.Time
}

// NewRateLimiter allows up to `burst` requests, refilling at a rate of
// `burst` per `per` (e.g. NewRateLimiter(5, time.Hour) is "5 per hour",
// refilling continuously rather than resetting all-at-once on the hour).
func NewRateLimiter(burst int, per time.Duration) *RateLimiter {
	return &RateLimiter{
		buckets: make(map[string]*bucket),
		rate:    float64(burst) / per.Seconds(),
		burst:   float64(burst),
	}
}

// Allow reports whether a request for key may proceed, consuming one token
// if so.
func (rl *RateLimiter) Allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	b, ok := rl.buckets[key]
	if !ok {
		rl.buckets[key] = &bucket{tokens: rl.burst - 1, lastFill: now}
		return true
	}

	elapsed := now.Sub(b.lastFill).Seconds()
	b.tokens += elapsed * rl.rate
	if b.tokens > rl.burst {
		b.tokens = rl.burst
	}
	b.lastFill = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

var _ Limiter = (*RateLimiter)(nil)

// RateLimit wraps next, rejecting requests with 429 once keyFunc(r)'s
// bucket is exhausted.
func RateLimit(rl Limiter, keyFunc func(*http.Request) string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !rl.Allow(keyFunc(r)) {
			w.Header().Set("Retry-After", "60")
			http.Error(w, `{"error":"rate limit exceeded, try again later"}`, http.StatusTooManyRequests)
			return
		}
		next(w, r)
	}
}

// ByIP keys on the request's remote address (no reverse proxy sits in
// front of apreg-server in v1, so RemoteAddr is trustworthy — this
// assumption must be revisited, e.g. switching to X-Forwarded-For with a
// trusted-proxy allowlist, the moment a reverse proxy/ALB is added in
// front at actual deploy time).
func ByIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ByAuthenticatedUser keys on the authenticated user set by RequireAuth,
// falling back to ByIP if none is present. Only meaningful when this
// middleware runs downstream of RequireAuth in the chain.
func ByAuthenticatedUser(r *http.Request) string {
	if u, ok := UserFromContext(r.Context()); ok {
		return u.Username
	}
	return ByIP(r)
}
