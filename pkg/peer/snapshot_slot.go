package peer

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

// snapshotSlot is a single-writer, single-reader latest-value mailbox.
// Producers swap the current value; a background goroutine drains it and
// emits at most one frame per coalesce window, dropping stale intermediate
// snapshots. This keeps control-plane latency low when state is changing
// faster than the peer link can drain it, without ever losing the latest
// view.
type snapshotSlot struct {
	mu      sync.Mutex
	pending *Message
	cond    *sync.Cond
	closed  bool
}

func newSnapshotSlot() *snapshotSlot {
	s := &snapshotSlot{}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// swap stores msg as the latest pending snapshot, replacing any older one.
func (s *snapshotSlot) swap(msg *Message) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.pending = msg
	s.cond.Signal()
	return true
}

// next waits until there is a pending snapshot or the slot is closed.
// It returns nil when closed.
func (s *snapshotSlot) next() *Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.pending == nil && !s.closed {
		s.cond.Wait()
	}
	msg := s.pending
	s.pending = nil
	return msg
}

// close wakes the reader and prevents further swaps.
func (s *snapshotSlot) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.cond.Signal()
}

// isClosed reports whether the slot is closed.
func (s *snapshotSlot) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// runSnapshotEmitter drains slot and emits coalesced snapshots onto pc.lo.
// It exits when ctx is done or the slot is closed.
func runSnapshotEmitter(ctx context.Context, pc *PeerConnection, slot *snapshotSlot) {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-pc.Done():
			return
		default:
		}

		msg := slot.next()
		if msg == nil {
			return
		}

		// Coalesce: keep consuming pending snapshots until none left, then emit
		// the latest one.
		for {
			next := slot.tryNext()
			if next == nil {
				break
			}
			msg = next
		}

		data, err := json.Marshal(msg)
		if err != nil {
			continue
		}
		if !pc.enqueue(pc.lo, wireFrame{data: data}) {
			// Peer closed or queue full; stop trying.
			return
		}

		// Brief yield so the writer goroutine can drain the lane and so we
		// don't monopolize the CPU when snapshots are produced continuously.
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// tryNext returns a pending snapshot without blocking.
func (s *snapshotSlot) tryNext() *Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	msg := s.pending
	s.pending = nil
	return msg
}
