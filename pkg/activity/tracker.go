package activity

import (
	"sync"
	"time"
)

const (
	// BucketDuration is the size of each sparkline bucket
	BucketDuration = 5 * time.Second
	// BucketCount is how many buckets to keep (60 buckets × 5s = 5 minutes of history)
	BucketCount = 60
)

// SessionActivity tracks output activity for a single session
type SessionActivity struct {
	mu         sync.Mutex
	buckets    [BucketCount]int64 // byte counts per bucket
	headIdx    int                // current bucket index
	headTime   time.Time          // start time of current bucket
	lastActive time.Time          // last time any output was seen
	totalBytes int64
}

// Snapshot is a point-in-time view of a session's activity
type Snapshot struct {
	Host        string  `json:"host,omitempty"` // peer fingerprint (empty = local)
	SessionName string  `json:"session"`
	IdleSeconds float64 `json:"idle_seconds"`
	Sparkline   []int64 `json:"sparkline"` // most recent BucketCount buckets, oldest first
	TotalBytes  int64   `json:"total_bytes"`
}

// Tracker tracks output activity across all sessions
type Tracker struct {
	mu       sync.RWMutex
	sessions map[string]*SessionActivity
}

// NewTracker creates a new activity tracker
func NewTracker() *Tracker {
	return &Tracker{
		sessions: make(map[string]*SessionActivity),
	}
}

// Record records output bytes for a session
func (t *Tracker) Record(session string, n int) {
	t.mu.RLock()
	sa, ok := t.sessions[session]
	t.mu.RUnlock()

	if !ok {
		t.mu.Lock()
		sa, ok = t.sessions[session]
		if !ok {
			sa = &SessionActivity{
				headTime: time.Now().Truncate(BucketDuration),
			}
			t.sessions[session] = sa
		}
		t.mu.Unlock()
	}

	now := time.Now()
	sa.mu.Lock()
	defer sa.mu.Unlock()

	sa.lastActive = now
	sa.totalBytes += int64(n)

	// Advance buckets to current time
	currentBucket := now.Truncate(BucketDuration)
	elapsed := currentBucket.Sub(sa.headTime)
	if elapsed > 0 {
		steps := int(elapsed / BucketDuration)
		if steps > BucketCount {
			steps = BucketCount
		}
		for i := 0; i < steps; i++ {
			sa.headIdx = (sa.headIdx + 1) % BucketCount
			sa.buckets[sa.headIdx] = 0
		}
		sa.headTime = currentBucket
	}

	sa.buckets[sa.headIdx] += int64(n)
}

// Get returns a snapshot of a session's activity
func (t *Tracker) Get(session string) *Snapshot {
	t.mu.RLock()
	sa, ok := t.sessions[session]
	t.mu.RUnlock()

	if !ok {
		return &Snapshot{
			SessionName: session,
			IdleSeconds: -1, // never seen
			Sparkline:   make([]int64, BucketCount),
		}
	}

	sa.mu.Lock()
	defer sa.mu.Unlock()

	// Advance buckets to now before reading
	now := time.Now()
	currentBucket := now.Truncate(BucketDuration)
	elapsed := currentBucket.Sub(sa.headTime)
	if elapsed > 0 {
		steps := int(elapsed / BucketDuration)
		if steps > BucketCount {
			steps = BucketCount
		}
		for i := 0; i < steps; i++ {
			sa.headIdx = (sa.headIdx + 1) % BucketCount
			sa.buckets[sa.headIdx] = 0
		}
		sa.headTime = currentBucket
	}

	// Build sparkline array, oldest first
	sparkline := make([]int64, BucketCount)
	for i := 0; i < BucketCount; i++ {
		idx := (sa.headIdx + 1 + i) % BucketCount
		sparkline[i] = sa.buckets[idx]
	}

	idle := now.Sub(sa.lastActive).Seconds()

	return &Snapshot{
		SessionName: session,
		IdleSeconds: idle,
		Sparkline:   sparkline,
		TotalBytes:  sa.totalBytes,
	}
}

// GetAll returns snapshots for all tracked sessions
func (t *Tracker) GetAll() []*Snapshot {
	t.mu.RLock()
	names := make([]string, 0, len(t.sessions))
	for name := range t.sessions {
		names = append(names, name)
	}
	t.mu.RUnlock()

	snapshots := make([]*Snapshot, 0, len(names))
	for _, name := range names {
		snapshots = append(snapshots, t.Get(name))
	}
	return snapshots
}

// Remove removes tracking for a session
func (t *Tracker) Remove(session string) {
	t.mu.Lock()
	delete(t.sessions, session)
	t.mu.Unlock()
}
