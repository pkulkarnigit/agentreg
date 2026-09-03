package auth

import (
	"log/slog"
	"sync"
	"time"
)

// Lockout tracks failed login attempts per username, independent of source
// IP — a per-IP rate limiter alone doesn't stop a distributed brute force
// against one account from many addresses. LoginLockout (below) is the
// in-memory, single-instance implementation; RedisLockout (redislockout.go)
// is the same contract backed by Redis, for when krate-server runs as more
// than one replica and failure counts need to be shared rather than
// per-process.
type Lockout interface {
	Locked(username string) bool
	RecordFailure(username string)
	Reset(username string)
}

// LoginLockout is an in-memory, single-instance Lockout. Fine for a single
// krate-server instance; swap in RedisLockout once there's more than one.
type LoginLockout struct {
	mu          sync.Mutex
	failures    map[string][]time.Time
	maxFailures int
	window      time.Duration
}

// NewLoginLockout locks out a username once it has accumulated
// maxFailures failed attempts within the trailing window. The lockout
// clears itself as old failures age out of the window — no separate
// cooldown timer needed — or immediately via Reset on a successful login.
func NewLoginLockout(maxFailures int, window time.Duration) *LoginLockout {
	return &LoginLockout{
		failures:    make(map[string][]time.Time),
		maxFailures: maxFailures,
		window:      window,
	}
}

var _ Lockout = (*LoginLockout)(nil)

// Locked reports whether username currently has >= maxFailures recorded
// failures within the trailing window.
func (l *LoginLockout) Locked(username string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.countRecent(username) >= l.maxFailures
}

// RecordFailure records a failed login attempt for username.
func (l *LoginLockout) RecordFailure(username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	failures := append(l.prune(username), time.Now())
	l.failures[username] = failures
	if len(failures) == l.maxFailures {
		slog.Warn("account locked out after repeated failed logins", "username", username, "failures", len(failures), "window", l.window)
	}
}

// Reset clears a username's failure history, e.g. after a successful login.
func (l *LoginLockout) Reset(username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, username)
}

// countRecent must be called with l.mu held.
func (l *LoginLockout) countRecent(username string) int {
	return len(l.prune(username))
}

// prune must be called with l.mu held. It does not itself write back to
// the map (callers that want the pruned slice persisted do so themselves).
func (l *LoginLockout) prune(username string) []time.Time {
	cutoff := time.Now().Add(-l.window)
	times := l.failures[username]
	kept := times[:0:0]
	for _, t := range times {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	l.failures[username] = kept
	return kept
}
