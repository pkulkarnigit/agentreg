package auth

import (
	"sync"
	"time"
)

// LoginLockout tracks failed login attempts per username, independent of
// source IP — a per-IP rate limiter alone doesn't stop a distributed
// brute force against one account from many addresses. In-memory and
// single-instance, like RateLimiter; same future swap story if the server
// ever scales horizontally.
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
	l.failures[username] = append(l.prune(username), time.Now())
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
