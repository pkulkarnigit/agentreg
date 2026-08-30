package auth

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/pkulkarni/apreg/internal/auth/lockouttest"
)

// TestRedisLockout_Conformance runs the exact same behavioral suite
// LoginLockout runs, against a real Redis, proving both Lockout
// implementations behave identically. Requires APREG_TEST_REDIS_ADDR;
// skips cleanly without it.
func TestRedisLockout_Conformance(t *testing.T) {
	addr := os.Getenv("APREG_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("APREG_TEST_REDIS_ADDR not set; skipping Redis lockout conformance suite")
	}

	client := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { client.Close() })
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("ping redis at %s: %v", addr, err)
	}

	n := 0
	lockouttest.RunConformanceSuite(t, func(t *testing.T, maxFailures int, window time.Duration) lockouttest.Lockout {
		n++
		prefix := fmt.Sprintf("test-%d-%d", time.Now().UnixNano(), n)
		t.Cleanup(func() {
			keys, err := client.Keys(context.Background(), "apreg:lockout:"+prefix+":*").Result()
			if err == nil && len(keys) > 0 {
				client.Del(context.Background(), keys...)
			}
		})
		return NewRedisLockout(client, prefix, maxFailures, window)
	})
}
