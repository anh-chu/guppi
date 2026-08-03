package activity

import (
	"sync"
	"time"
)

// SessionActivity tracks output activity for a single session
type SessionActivity struct {
	mu         sync.Mutex
	lastActive time.Time
	totalBytes int64
}

// Snapshot is a point-in-time view of a session's activity
type Snapshot struct {
	Host        string    `json:"host,omitempty"` // peer fingerprint (empty = local)
	SessionName string    `json:"session"`
	IdleSeconds float64   `json:"idle_seconds"`
	TotalBytes  int64     `json:"total_bytes"`
	LastActive  time.Time `json:"last_active,omitempty"` // actual time of last recorded activity
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
			sa = &SessionActivity{}
			t.sessions[session] = sa
		}
		t.mu.Unlock()
	}

	now := time.Now()
	sa.mu.Lock()
	defer sa.mu.Unlock()

	sa.lastActive = now
	sa.totalBytes += int64(n)
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
		}
	}

	sa.mu.Lock()
	defer sa.mu.Unlock()

	now := time.Now()
	idle := now.Sub(sa.lastActive).Seconds()

	return &Snapshot{
		SessionName: session,
		IdleSeconds: idle,
		TotalBytes:  sa.totalBytes,
		LastActive:  sa.lastActive,
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
