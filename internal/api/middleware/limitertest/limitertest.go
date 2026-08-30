// Package limitertest is a shared behavioral conformance suite for
// middleware.Limiter implementations — the same pattern as
// internal/store/storetest and internal/store/blobtest, applied to rate
// limiting: RateLimiter (in-process) and RedisLimiter (shared across
// replicas) both run the exact same cases, so "swap the backend" is a
// guarantee, not a hope.
//
// This package deliberately does not import middleware — newLimiter's
// return type only needs to satisfy Limiter structurally, which keeps this
// a leaf dependency usable from any package that has its own limiter type.
package limitertest

import (
	"testing"
	"time"
)

// Limiter is the minimal contract under test.
type Limiter interface {
	Allow(key string) bool
}

// RunConformanceSuite runs the full behavioral contract of Limiter against
// a freshly constructed limiter. newLimiter is called once per top-level
// sub-test with a fresh limiter configured for the given burst/refill
// period, matching the (burst int, per time.Duration) shape both
// NewRateLimiter and NewRedisLimiter share.
func RunConformanceSuite(t *testing.T, newLimiter func(t *testing.T, burst int, per time.Duration) Limiter) {
	t.Run("AllowsUpToBurstThenBlocks", func(t *testing.T) {
		testAllowsUpToBurstThenBlocks(t, newLimiter(t, 3, time.Hour))
	})
	t.Run("IndependentPerKey", func(t *testing.T) {
		testIndependentPerKey(t, newLimiter(t, 1, time.Hour))
	})
	t.Run("RefillsOverTime", func(t *testing.T) {
		testRefillsOverTime(t, newLimiter(t, 1, 200*time.Millisecond))
	})
}

func testAllowsUpToBurstThenBlocks(t *testing.T, l Limiter) {
	for i := 0; i < 3; i++ {
		if !l.Allow("alice") {
			t.Fatalf("request %d: expected allowed within burst", i)
		}
	}
	if l.Allow("alice") {
		t.Fatal("expected 4th request to be blocked")
	}
}

func testIndependentPerKey(t *testing.T, l Limiter) {
	if !l.Allow("alice") {
		t.Fatal("expected alice's first request to be allowed")
	}
	if !l.Allow("bob") {
		t.Fatal("expected bob's first request to be allowed independently of alice's bucket")
	}
	if l.Allow("alice") {
		t.Fatal("expected alice's second request to be blocked")
	}
}

// testRefillsOverTime uses a generous margin (5x the refill window) since
// this exercises real wall-clock time and, for RedisLimiter, a real round
// trip — the same tolerance style used elsewhere in this codebase for
// timing-based tests (e.g. internal/auth's lockout window test).
func testRefillsOverTime(t *testing.T, l Limiter) {
	if !l.Allow("alice") {
		t.Fatal("expected first request to be allowed")
	}
	if l.Allow("alice") {
		t.Fatal("expected immediate second request to be blocked")
	}
	time.Sleep(1 * time.Second)
	if !l.Allow("alice") {
		t.Fatal("expected request after refill window to be allowed")
	}
}
