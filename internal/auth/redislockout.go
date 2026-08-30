package auth

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisLockout is Lockout backed by Redis instead of an in-process map, so
// every apreg-server replica shares the same failure counts — a per-process
// LoginLockout can't stop a distributed brute force spread across
// requests that land on different replicas.
//
// Failures for a username are stored as a Redis sorted set (score = the
// failure's timestamp), pruned to the trailing window on each read; that
// pruning is a plain ZREMRANGEBYSCORE rather than a single atomic script,
// so under concurrent access from multiple replicas the count can be off
// by a failure or two around the boundary. That's an acceptable trade for
// a defense-in-depth control sitting on top of bcrypt password hashing and
// per-scope publish authorization — it doesn't need to be exact, just hard
// to bypass at scale. A Redis outage fails a check open (Locked returns
// false, with a logged warning) rather than locking everyone out.
type RedisLockout struct {
	client      *redis.Client
	prefix      string
	maxFailures int
	window      time.Duration
}

// NewRedisLockout mirrors NewLoginLockout's (maxFailures, window)
// semantics. prefix scopes this lockout's keys in Redis, the same role
// RedisLimiter's prefix plays.
func NewRedisLockout(client *redis.Client, prefix string, maxFailures int, window time.Duration) *RedisLockout {
	return &RedisLockout{client: client, prefix: prefix, maxFailures: maxFailures, window: window}
}

func (l *RedisLockout) key(username string) string {
	return "apreg:lockout:" + l.prefix + ":" + username
}

func (l *RedisLockout) Locked(username string) bool {
	ctx := context.Background()
	key := l.key(username)
	cutoff := time.Now().Add(-l.window).UnixNano()

	if err := l.client.ZRemRangeByScore(ctx, key, "-inf", fmt.Sprintf("(%d", cutoff)).Err(); err != nil {
		slog.Warn("redis lockout unreachable, failing open", "username", username, "error", err)
		return false
	}
	count, err := l.client.ZCard(ctx, key).Result()
	if err != nil {
		slog.Warn("redis lockout unreachable, failing open", "username", username, "error", err)
		return false
	}
	return count >= int64(l.maxFailures)
}

func (l *RedisLockout) RecordFailure(username string) {
	ctx := context.Background()
	key := l.key(username)
	now := time.Now()
	member := fmt.Sprintf("%d-%d", now.UnixNano(), rand.Int63())

	pipe := l.client.Pipeline()
	pipe.ZAdd(ctx, key, redis.Z{Score: float64(now.UnixNano()), Member: member})
	pipe.Expire(ctx, key, l.window+time.Minute)
	if _, err := pipe.Exec(ctx); err != nil {
		slog.Warn("redis lockout unreachable, failed to record failure", "username", username, "error", err)
	}
}

func (l *RedisLockout) Reset(username string) {
	if err := l.client.Del(context.Background(), l.key(username)).Err(); err != nil {
		slog.Warn("redis lockout unreachable, failed to reset", "username", username, "error", err)
	}
}

var _ Lockout = (*RedisLockout)(nil)
