package middleware

import (
	"context"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// tokenBucketScript is the same token-bucket algorithm RateLimiter runs
// in-process, just executed atomically inside Redis instead of behind a
// Go mutex — that atomicity is the whole point: several apreg-server
// replicas hitting the same key must still see one consistent bucket, not
// one each. KEYS[1] is the bucket's Redis key; ARGV is rate (tokens/sec),
// burst, and the current unix time in fractional seconds. Returns 1 if the
// request may proceed, 0 if the bucket is empty.
const tokenBucketScript = `
local key = KEYS[1]
local rate = tonumber(ARGV[1])
local burst = tonumber(ARGV[2])
local now = tonumber(ARGV[3])

local data = redis.call('HMGET', key, 'tokens', 'ts')
local tokens = tonumber(data[1])
local ts = tonumber(data[2])
if tokens == nil then
  tokens = burst
  ts = now
end

local elapsed = math.max(0, now - ts)
tokens = math.min(burst, tokens + elapsed * rate)

local allowed = 0
if tokens >= 1 then
  tokens = tokens - 1
  allowed = 1
end

redis.call('HMSET', key, 'tokens', tostring(tokens), 'ts', tostring(now))
redis.call('EXPIRE', key, math.ceil(burst / rate) + 1)
return allowed
`

// RedisLimiter is Limiter backed by Redis instead of an in-process map, so
// every apreg-server replica shares the same buckets. It's the horizontal-
// scaling counterpart to RateLimiter: same NewRateLimiter(burst, per)
// semantics, same Allow(key) contract, different storage.
//
// A Redis outage fails the bucket open (Allow returns true, with a logged
// warning) rather than blocking all traffic — rate limiting here is
// defense-in-depth on top of password hashing and per-scope publish
// authorization, not the only thing standing between the registry and
// abuse, so availability wins on that trade-off. This mirrors how a failed
// notification send elsewhere in this codebase logs and continues rather
// than blocking the action it's securing.
type RedisLimiter struct {
	client *redis.Client
	prefix string
	rate   float64
	burst  float64
}

// NewRedisLimiter mirrors NewRateLimiter's (burst, per) semantics: up to
// burst requests, refilling continuously at burst-per-`per`. prefix scopes
// this limiter's keys in Redis so, e.g., the signup and login limiters
// (which share a Redis instance but must not share buckets) don't collide.
func NewRedisLimiter(client *redis.Client, prefix string, burst int, per time.Duration) *RedisLimiter {
	return &RedisLimiter{
		client: client,
		prefix: prefix,
		rate:   float64(burst) / per.Seconds(),
		burst:  float64(burst),
	}
}

func (rl *RedisLimiter) Allow(key string) bool {
	now := float64(time.Now().UnixNano()) / 1e9
	res, err := rl.client.Eval(context.Background(), tokenBucketScript,
		[]string{"apreg:ratelimit:" + rl.prefix + ":" + key},
		rl.rate, rl.burst, now,
	).Int64()
	if err != nil {
		slog.Warn("redis rate limiter unreachable, failing open", "prefix", rl.prefix, "error", err)
		return true
	}
	return res == 1
}

var _ Limiter = (*RedisLimiter)(nil)
