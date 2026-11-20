package auth

import (
	"sync"
	"time"
)

// limiter throttles credential guessing per client.
//
// It is a fixed-window counter, not a token bucket: the point is to make guessing
// a 192-bit secret pointless, not to shape traffic. The map is capped so a flood
// of distinct source addresses degrades into forgetting old entries rather than
// growing without bound.
type limiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	now    func() time.Time
	table  map[string]*attempt

	// maxEntries bounds the table. A /64 of IPv6 addresses is trivially cheap to
	// walk, so this cannot be unbounded.
	maxEntries int
}

type attempt struct {
	count int
	until time.Time
}

func newLimiter(limit int, window time.Duration, now func() time.Time) *limiter {
	return &limiter{
		limit:      limit,
		window:     window,
		now:        now,
		table:      make(map[string]*attempt),
		maxEntries: 4096,
	}
}

// blocked reports whether this client has exhausted its window.
func (l *limiter) blocked(client string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	e, ok := l.table[client]
	if !ok {
		return false
	}
	if l.now().After(e.until) {
		delete(l.table, client)
		return false
	}
	return e.count >= l.limit
}

// fail records one bad credential.
func (l *limiter) fail(client string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	e, ok := l.table[client]
	if !ok {
		if len(l.table) >= l.maxEntries {
			l.evictLocked(now)
		}
		l.table[client] = &attempt{count: 1, until: now.Add(l.window)}
		return
	}
	if now.After(e.until) {
		e.count = 1
		e.until = now.Add(l.window)
		return
	}
	e.count++
}

// retryAfter is how long the caller should wait, for the Retry-After header.
func (l *limiter) retryAfter() time.Duration {
	return l.window
}

// evictLocked drops expired entries, and if none are expired drops one at random
// (Go map iteration order) so a full table can never wedge.
func (l *limiter) evictLocked(now time.Time) {
	for key, e := range l.table {
		if now.After(e.until) {
			delete(l.table, key)
		}
	}
	if len(l.table) < l.maxEntries {
		return
	}
	for key := range l.table {
		delete(l.table, key)
		return
	}
}
