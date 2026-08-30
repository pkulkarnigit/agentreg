package auth

import (
	"testing"
	"time"

	"github.com/pkulkarni/apreg/internal/auth/lockouttest"
)

func TestLoginLockout_Conformance(t *testing.T) {
	lockouttest.RunConformanceSuite(t, func(t *testing.T, maxFailures int, window time.Duration) lockouttest.Lockout {
		return NewLoginLockout(maxFailures, window)
	})
}
