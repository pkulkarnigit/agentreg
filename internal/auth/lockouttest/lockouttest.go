// Package lockouttest is a shared behavioral conformance suite for
// auth.Lockout implementations — the same pattern as internal/store's
// storetest/blobtest and internal/api/middleware's limitertest, applied to
// login lockout: LoginLockout (in-process) and RedisLockout (shared across
// replicas) both run the exact same cases.
//
// This package deliberately does not import auth — newLockout's return
// type only needs to satisfy Lockout structurally.
package lockouttest

import (
	"testing"
	"time"
)

// Lockout is the minimal contract under test.
type Lockout interface {
	Locked(username string) bool
	RecordFailure(username string)
	Reset(username string)
}

// RunConformanceSuite runs the full behavioral contract of Lockout against
// a freshly constructed lockout. newLockout is called once per top-level
// sub-test with a fresh lockout configured for the given
// (maxFailures, window), matching NewLoginLockout/NewRedisLockout's shape.
func RunConformanceSuite(t *testing.T, newLockout func(t *testing.T, maxFailures int, window time.Duration) Lockout) {
	t.Run("LocksAfterMaxFailures", func(t *testing.T) {
		testLocksAfterMaxFailures(t, newLockout(t, 3, time.Hour))
	})
	t.Run("IndependentPerUsername", func(t *testing.T) {
		testIndependentPerUsername(t, newLockout(t, 1, time.Hour))
	})
	t.Run("ExpiresOutsideWindow", func(t *testing.T) {
		testExpiresOutsideWindow(t, newLockout(t, 1, 200*time.Millisecond))
	})
	t.Run("ResetClearsHistory", func(t *testing.T) {
		testResetClearsHistory(t, newLockout(t, 1, time.Hour))
	})
}

func testLocksAfterMaxFailures(t *testing.T, l Lockout) {
	for i := 0; i < 2; i++ {
		l.RecordFailure("alice")
		if l.Locked("alice") {
			t.Fatalf("should not be locked after %d failures", i+1)
		}
	}
	l.RecordFailure("alice")
	if !l.Locked("alice") {
		t.Fatal("expected lockout after 3rd failure")
	}
}

func testIndependentPerUsername(t *testing.T, l Lockout) {
	l.RecordFailure("alice")
	if !l.Locked("alice") {
		t.Fatal("expected alice locked")
	}
	if l.Locked("bob") {
		t.Fatal("expected bob unaffected by alice's failures")
	}
}

// testExpiresOutsideWindow uses a generous margin, the same tolerance
// style limitertest uses for its own refill test — this exercises real
// wall-clock time and, for RedisLockout, a real round trip.
func testExpiresOutsideWindow(t *testing.T, l Lockout) {
	l.RecordFailure("alice")
	if !l.Locked("alice") {
		t.Fatal("expected locked immediately after failure")
	}
	time.Sleep(1 * time.Second)
	if l.Locked("alice") {
		t.Fatal("expected lockout to expire once failure ages out of window")
	}
}

func testResetClearsHistory(t *testing.T, l Lockout) {
	l.RecordFailure("alice")
	if !l.Locked("alice") {
		t.Fatal("expected locked after failure")
	}
	l.Reset("alice")
	if l.Locked("alice") {
		t.Fatal("expected Reset to clear lockout")
	}
}
