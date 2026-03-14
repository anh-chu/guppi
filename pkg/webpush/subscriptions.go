package webpush

import (
	"sync"

	wp "github.com/SherClockHolmes/webpush-go"
)

// Store manages push notification subscriptions in memory
type Store struct {
	mu            sync.RWMutex
	subscriptions map[string]*wp.Subscription // keyed by endpoint URL
}

// NewStore creates a new subscription store
func NewStore() *Store {
	return &Store{
		subscriptions: make(map[string]*wp.Subscription),
	}
}

// Add registers a push subscription
func (s *Store) Add(sub *wp.Subscription) {
	s.mu.Lock()
	s.subscriptions[sub.Endpoint] = sub
	s.mu.Unlock()
}

// Remove unregisters a push subscription by endpoint
func (s *Store) Remove(endpoint string) {
	s.mu.Lock()
	delete(s.subscriptions, endpoint)
	s.mu.Unlock()
}

// All returns a snapshot of all current subscriptions
func (s *Store) All() []*wp.Subscription {
	s.mu.RLock()
	defer s.mu.RUnlock()
	subs := make([]*wp.Subscription, 0, len(s.subscriptions))
	for _, sub := range s.subscriptions {
		subs = append(subs, sub)
	}
	return subs
}

// Count returns the number of active subscriptions
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.subscriptions)
}
