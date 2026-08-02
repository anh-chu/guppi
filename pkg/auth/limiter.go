package auth

import (
	"sync"
	"time"
)

// Limiter provides bounded global rate limits for authentication attempts.
// It tracks per-client buckets keyed by attempt category and IP address.
type Limiter struct {
	mu     sync.Mutex
	limits map[limitKey]*bucket
	clock  func() time.Time
}

type limitKey struct {
	category string
	ip       string
}

type bucket struct {
	tokens float64
	last   time.Time
}

// Limit describes a category's rate: Capacity attempts per Window.
type Limit struct {
	Capacity int
	Window   time.Duration
}

// CategoryLimits is exported for tests and documentation.
var CategoryLimits = map[string]Limit{
	"setup":     {Capacity: 5, Window: time.Minute},
	"login":     {Capacity: 10, Window: time.Minute},
	"bootstrap": {Capacity: 5, Window: time.Minute},
}

const idleEviction = 15 * time.Minute

// NewLimiter returns a limiter with a real clock.
func NewLimiter() *Limiter {
	return &Limiter{
		limits: make(map[limitKey]*bucket),
		clock:  time.Now,
	}
}

// SetClock replaces the clock function; useful only for tests.
func (l *Limiter) SetClock(now func() time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.clock = now
}

// Allow reports whether a request in category from ip is allowed, and if not
// how long the caller should wait before retrying. Idle buckets are evicted
// after 15 minutes of inactivity.
func (l *Limiter) Allow(category, ip string) (bool, time.Duration) {
	limit, ok := CategoryLimits[category]
	if !ok {
		return true, 0
	}

	now := l.clock()
	key := limitKey{category: category, ip: ip}

	l.mu.Lock()
	b, exists := l.limits[key]
	if !exists {
		b = &bucket{tokens: float64(limit.Capacity - 1), last: now}
		l.limits[key] = b
		l.mu.Unlock()
		l.maybeCleanup(now)
		return true, 0
	}

	elapsed := now.Sub(b.last)
	if elapsed >= limit.Window {
		b.tokens = float64(limit.Capacity)
	} else {
		b.tokens += elapsed.Seconds() / limit.Window.Seconds() * float64(limit.Capacity)
		if b.tokens > float64(limit.Capacity) {
			b.tokens = float64(limit.Capacity)
		}
	}

	if b.tokens >= 1 {
		b.tokens--
		b.last = now
		l.mu.Unlock()
		l.maybeCleanup(now)
		return true, 0
	}

	// Update last-seen so the bucket isn't evicted while the client is being
	// throttled; it will be reset once idle for 15 minutes.
	b.last = now
	retry := time.Duration((1.0-b.tokens)/float64(limit.Capacity)*limit.Window.Seconds()) * time.Second
	if retry < time.Second {
		retry = time.Second
	}
	l.mu.Unlock()
	return false, retry
}

func (l *Limiter) maybeCleanup(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, b := range l.limits {
		if now.Sub(b.last) > idleEviction {
			delete(l.limits, k)
		}
	}
}
