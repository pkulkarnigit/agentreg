package middleware

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/pkulkarni/apreg/internal/api/middleware/limitertest"
)

// TestRedisLimiter_Conformance runs the exact same behavioral suite
// RateLimiter runs, against a real Redis, proving both Limiter
// implementations behave identically. Requires APREG_TEST_REDIS_ADDR;
// skips cleanly without it.
func TestRedisLimiter_Conformance(t *testing.T) {
	addr := os.Getenv("APREG_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("APREG_TEST_REDIS_ADDR not set; skipping Redis limiter conformance suite")
	}

	client := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { client.Close() })
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("ping redis at %s: %v", addr, err)
	}

	n := 0
	limitertest.RunConformanceSuite(t, func(t *testing.T, burst int, per time.Duration) limitertest.Limiter {
		n++
		prefix := fmt.Sprintf("test-%d-%d", time.Now().UnixNano(), n)
		t.Cleanup(func() {
			keys, err := client.Keys(context.Background(), "apreg:ratelimit:"+prefix+":*").Result()
			if err == nil && len(keys) > 0 {
				client.Del(context.Background(), keys...)
			}
		})
		return NewRedisLimiter(client, prefix, burst, per)
	})
}
