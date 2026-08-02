package peer

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/anh-chu/termyard/pkg/activity"
	"github.com/anh-chu/termyard/pkg/common"
	"github.com/anh-chu/termyard/pkg/identity"
	"github.com/anh-chu/termyard/pkg/model"
	"github.com/anh-chu/termyard/pkg/state"
	"github.com/anh-chu/termyard/pkg/stats"
	"github.com/anh-chu/termyard/pkg/toolevents"
)

const (
	// OfflineTimeout is how long to keep an offline peer's sessions visible
	OfflineTimeout = 5 * time.Minute
)

// HostState holds all known state for a single peer
type HostState struct {
	ID         string // public key fingerprint
	Name       string
	Version    string
	PublicKey  string
	Address    string // network address (empty for local)
	Sessions   []*model.Session
	Stats      map[string]interface{}
	Activity   []*activity.Snapshot
	ToolEvents []*toolevents.Event
	Connected  bool
	LastSeen   time.Time
	Conn       *PeerConnection // nil for local host
}

// wireFrame is a pre-serialized WebSocket frame ready for the wire. Marshaling
// happens in the producer goroutine (off the single writer) so the writer only
// does the syscall.
type wireFrame struct {
	data []byte
}

// Queue depths. The hi lane carries interactive PTY traffic (keystrokes,
// output, resize) and is deep so a burst of output never starves input. The
// lo lane carries bulky, latency-tolerant control plane (state snapshots,
// stats, activity data, tool events).
const (
	hiQueueDepth = 1024
	loQueueDepth = 128
)

// PeerConnection wraps a control WebSocket to a peer. Sends are gated behind
// Enqueue*/Close so concurrent producers cannot race the close, and split into
// two priority lanes so bulky control-plane messages never block PTY frames
// (head-of-line blocking was the dominant source of typing jitter).
type PeerConnection struct {
	HostID string
	Role   Role
	Caps   []string

	mu     sync.Mutex
	hi     chan wireFrame
	lo     chan wireFrame
	done   chan struct{}
	closed bool
}

// NewPeerConnection constructs a PeerConnection with buffered priority lanes.
// bufSize seeds the low-priority lane; the hi lane is fixed-deep.
func NewPeerConnection(hostID string, bufSize int) *PeerConnection {
	if bufSize < loQueueDepth {
		bufSize = loQueueDepth
	}
	return &PeerConnection{
		HostID: hostID,
		hi:     make(chan wireFrame, hiQueueDepth),
		lo:     make(chan wireFrame, bufSize),
		done:   make(chan struct{}),
	}
}

// Done returns a channel that is closed when the connection is closed. Lets
// consumers (e.g. the browser-input pump) react when the underlying peer link
// dies and tear down dependent state instead of silently dropping messages.
func (pc *PeerConnection) Done() <-chan struct{} {
	return pc.done
}

// HasCapability reports whether the peer advertised a specific capability
// during the hello handshake.
func (pc *PeerConnection) HasCapability(cap string) bool {
	for _, c := range pc.Caps {
		if c == cap {
			return true
		}
	}
	return false
}

// HiLane / LoLane expose the lanes to the writer goroutine for priority drain.
func (pc *PeerConnection) HiLane() <-chan wireFrame { return pc.hi }
func (pc *PeerConnection) LoLane() <-chan wireFrame { return pc.lo }

func (pc *PeerConnection) enqueue(ch chan wireFrame, f wireFrame) bool {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	if pc.closed {
		return false
	}
	select {
	case ch <- f:
		return true
	default:
		return false
	}
}

// Enqueue best-effort queues a control-plane JSON message on the low-priority
// lane. Returns false if the connection was closed or the lane is full.
func (pc *PeerConnection) Enqueue(msg *Message) bool {
	data, err := json.Marshal(msg)
	if err != nil {
		return false
	}
	return pc.enqueue(pc.lo, wireFrame{data: data})
}

// EnqueueHi best-effort queues a small interactive JSON message (e.g. PTY
// resize/control) on the high-priority lane.
func (pc *PeerConnection) EnqueueHi(msg *Message) bool {
	data, err := json.Marshal(msg)
	if err != nil {
		return false
	}
	return pc.enqueue(pc.hi, wireFrame{data: data})
}

// Close marks the connection closed. Idempotent. The lanes are never closed
// (producers gate on the closed flag under mu), so there is no send-on-closed
// race; the writer goroutine exits via Done.
func (pc *PeerConnection) Close() {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	if pc.closed {
		return
	}
	pc.closed = true
	close(pc.done)
}

// Manager aggregates state from local sessions and remote peers
type Manager struct {
	mu    sync.RWMutex
	hosts map[string]*HostState // keyed by peer fingerprint

	localID   string // this node's fingerprint
	localName string
	identity  *identity.Identity
	peerStore *identity.PeerStore
	localMgr  *state.Manager

	// Subscribers for state changes (browser WebSocket hub subscribes here)
	subMu       sync.RWMutex
	subscribers []chan state.StateEvent
}

// NewManager creates a new peer manager
func NewManager(id *identity.Identity, peerStore *identity.PeerStore, localMgr *state.Manager) *Manager {
	m := &Manager{
		hosts:     make(map[string]*HostState),
		localID:   id.Fingerprint(),
		localName: id.Name,
		identity:  id,
		peerStore: peerStore,
		localMgr:  localMgr,
	}

	// Register local host
	m.hosts[m.localID] = &HostState{
		ID:        m.localID,
		Name:      id.Name,
		Version:   common.VERSION,
		PublicKey: id.PublicKey,
		Connected: true,
		LastSeen:  time.Now(),
	}

	return m
}

// updateLocalStats collects system stats and process counts for the local host
func (m *Manager) updateLocalStats() {
	s := stats.SystemStats()
	sessions := m.localMgr.GetSessions()
	s["processes"] = stats.ProcessCountsFromSessions(sessions)
	m.UpdatePeerStats(m.localID, s)
}

// Run starts forwarding local state events to peer manager subscribers
// and pruning offline peers. Blocks until ctx is cancelled.
func (m *Manager) Run(ctx context.Context) {
	// Forward local state events
	localCh := m.localMgr.Subscribe()
	defer m.localMgr.Unsubscribe(localCh)

	pruneTimer := time.NewTicker(30 * time.Second)
	defer pruneTimer.Stop()

	statsTimer := time.NewTicker(30 * time.Second)
	defer statsTimer.Stop()

	// Collect initial stats
	m.updateLocalStats()

	for {
		select {
		case evt, ok := <-localCh:
			if !ok {
				return
			}
			// Stamp with local host info
			evt.Host = m.localID
			evt.HostName = m.localName

			// Update local sessions cache
			m.mu.Lock()
			if h, ok := m.hosts[m.localID]; ok {
				h.Sessions = m.localMgr.GetSessions()
				h.LastSeen = time.Now()
			}
			m.mu.Unlock()

			m.broadcast(evt)

		case <-statsTimer.C:
			m.updateLocalStats()

		case <-pruneTimer.C:
			m.pruneOffline()

		case <-ctx.Done():
			return
		}
	}
}

// Subscribe returns a channel that receives state events from all hosts
func (m *Manager) Subscribe() chan state.StateEvent {
	ch := make(chan state.StateEvent, 64)
	m.subMu.Lock()
	m.subscribers = append(m.subscribers, ch)
	m.subMu.Unlock()
	return ch
}

// Unsubscribe removes a subscriber channel
func (m *Manager) Unsubscribe(ch chan state.StateEvent) {
	m.subMu.Lock()
	defer m.subMu.Unlock()
	for i, sub := range m.subscribers {
		if sub == ch {
			m.subscribers = append(m.subscribers[:i], m.subscribers[i+1:]...)
			close(ch)
			return
		}
	}
}

// broadcast sends an event to all subscribers
func (m *Manager) broadcast(evt state.StateEvent) {
	m.subMu.RLock()
	defer m.subMu.RUnlock()
	for _, ch := range m.subscribers {
		select {
		case ch <- evt:
		default:
		}
	}
}

// GetAllSessions returns sessions from all hosts, with host fields stamped
func (m *Manager) GetAllSessions() []*model.Session {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var all []*model.Session
	for _, h := range m.hosts {
		for _, s := range h.Sessions {
			s.Host = h.ID
			s.HostName = h.Name
			s.HostOnline = h.Connected
			all = append(all, s)
		}
	}
	return all
}

// GetLocalSessions returns only this node's sessions
func (m *Manager) GetLocalSessions() []*model.Session {
	return m.localMgr.GetSessions()
}

// GetHosts returns info about all known hosts
func (m *Manager) GetHosts() []HostInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	hosts := make([]HostInfo, 0, len(m.hosts))
	for _, h := range m.hosts {
		hosts = append(hosts, HostInfo{
			ID:       h.ID,
			Name:     h.Name,
			Version:  h.Version,
			Local:    h.ID == m.localID,
			Online:   h.Connected,
			Address:  h.Address,
			Sessions: h.Sessions,
			Activity: h.Activity,
			Stats:    h.Stats,
			LastSeen: h.LastSeen,
		})
	}
	return hosts
}

// GetHostsForPeer returns only the local host info, used to push to a connected
// peer without leaking other peers (no transitivity in phase 1).
func (m *Manager) GetHostsForPeer(remotePeerID string) []HostInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	h, ok := m.hosts[m.localID]
	if !ok {
		return nil
	}
	return []HostInfo{{
		ID:       h.ID,
		Name:     h.Name,
		Version:  h.Version,
		Local:    true,
		Online:   h.Connected,
		Address:  h.Address,
		Sessions: h.Sessions,
		Activity: h.Activity,
		Stats:    h.Stats,
		LastSeen: h.LastSeen,
	}}
}

// LocalID returns this node's fingerprint
func (m *Manager) LocalID() string {
	return m.localID
}

// LocalName returns this node's display name
func (m *Manager) LocalName() string {
	return m.localName
}

// Identity returns this node's identity (for protocol/auth).
func (m *Manager) Identity() *identity.Identity {
	return m.identity
}

// PeerStore returns this manager's peer store.
func (m *Manager) PeerStore() *identity.PeerStore {
	return m.peerStore
}

// LocalManager returns the local state manager.
func (m *Manager) LocalManager() *state.Manager {
	return m.localMgr
}

// RegisterPeer registers a newly connected peer
func (m *Manager) RegisterPeer(id, name, publicKey string, conn *PeerConnection) {
	m.RegisterPeerWithAddress(id, name, publicKey, "", conn)
}

// RegisterPeerWithAddress registers a newly connected peer with its address.
func (m *Manager) RegisterPeerWithAddress(id, name, publicKey, address string, conn *PeerConnection) {
	m.mu.Lock()
	m.hosts[id] = &HostState{
		ID:        id,
		Name:      name,
		PublicKey: publicKey,
		Address:   address,
		Connected: true,
		LastSeen:  time.Now(),
		Conn:      conn,
	}
	m.mu.Unlock()

	m.broadcast(state.StateEvent{
		Type:     "peer-connected",
		Host:     id,
		HostName: name,
	})

	logrus.WithFields(logrus.Fields{
		"peer": name,
		"id":   id,
	}).Info("peer connected")
}

// TryRegisterPeer atomically registers a peer iff no live connection exists
// for the same fingerprint. Returns true on success. Used by session.runSession
// to close the simultaneous-initiate race window.
func (m *Manager) TryRegisterPeer(id, name, publicKey, address string, conn *PeerConnection) bool {
	m.mu.Lock()
	if h, ok := m.hosts[id]; ok && h.Conn != nil {
		m.mu.Unlock()
		return false
	}
	m.hosts[id] = &HostState{
		ID:        id,
		Name:      name,
		PublicKey: publicKey,
		Address:   address,
		Connected: true,
		LastSeen:  time.Now(),
		Conn:      conn,
	}
	m.mu.Unlock()

	m.broadcast(state.StateEvent{
		Type:     "peer-connected",
		Host:     id,
		HostName: name,
	})

	logrus.WithFields(logrus.Fields{
		"peer": name,
		"id":   id,
	}).Info("peer connected")
	return true
}

func (m *Manager) UnregisterPeer(id string) {
	m.mu.Lock()
	h, ok := m.hosts[id]
	if ok {
		h.Connected = false
		h.Conn = nil
		h.LastSeen = time.Now()
	}
	m.mu.Unlock()

	if ok {
		m.broadcast(state.StateEvent{
			Type:     "peer-disconnected",
			Host:     id,
			HostName: h.Name,
		})

		logrus.WithFields(logrus.Fields{
			"peer": h.Name,
			"id":   id,
		}).Info("peer disconnected")
	}
}

// RemoveHost fully removes a host from the aggregated state (used on forget,
// where we must not keep the peer's sessions lingering until prune).
func (m *Manager) RemoveHost(id string) {
	if id == m.localID {
		return
	}
	m.mu.Lock()
	h, ok := m.hosts[id]
	if ok {
		delete(m.hosts, id)
	}
	m.mu.Unlock()

	if ok {
		m.broadcast(state.StateEvent{
			Type:     "peer-disconnected",
			Host:     id,
			HostName: h.Name,
		})
		logrus.WithFields(logrus.Fields{
			"peer": h.Name,
			"id":   id,
		}).Info("host removed")
	}
}

// UpdatePeerSessions updates a peer's session list
func (m *Manager) UpdatePeerSessions(id string, sessions []*model.Session) {
	m.mu.Lock()
	h, ok := m.hosts[id]
	if ok {
		h.Sessions = sessions
		h.LastSeen = time.Now()
	}
	m.mu.Unlock()

	if ok {
		m.broadcast(state.StateEvent{
			Type:     "sessions-changed",
			Host:     id,
			HostName: h.Name,
		})
	}
}

// UpdatePeerVersion updates a peer's reported version
func (m *Manager) UpdatePeerVersion(id, version string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if h, ok := m.hosts[id]; ok {
		h.Version = version
	}
}

// UpdatePeerActivity updates a peer's activity snapshots
func (m *Manager) UpdatePeerActivity(id string, snapshots []*activity.Snapshot) {
	m.mu.Lock()
	if h, ok := m.hosts[id]; ok {
		h.Activity = snapshots
		h.LastSeen = time.Now()
	}
	m.mu.Unlock()
}

// UpdatePeerStats updates a peer's system stats
func (m *Manager) UpdatePeerStats(id string, stats map[string]interface{}) {
	m.mu.Lock()
	if h, ok := m.hosts[id]; ok {
		h.Stats = stats
		h.LastSeen = time.Now()
	}
	m.mu.Unlock()
}

// HasLiveConnection reports whether a connected peer connection exists for id.
func (m *Manager) HasLiveConnection(id string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if h, ok := m.hosts[id]; ok {
		return h.Conn != nil
	}
	return false
}

// GetPeerAddress returns a connected peer's stored listening address.

// GetPeerConnection returns the connection for a specific peer
func (m *Manager) GetPeerConnection(id string) *PeerConnection {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if h, ok := m.hosts[id]; ok && h.Conn != nil {
		return h.Conn
	}
	return nil
}

// GetPeerAddress returns a connected peer's stored listening address.
func (m *Manager) GetPeerAddress(id string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if h, ok := m.hosts[id]; ok {
		return h.Address
	}
	return ""
}

// ConnectedPeers returns every currently-connected remote peer connection.
// The local host is skipped. Used for fan-out broadcasts (e.g. layout sync).
func (m *Manager) ConnectedPeers() []*PeerConnection {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]*PeerConnection, 0, len(m.hosts))
	for id, h := range m.hosts {
		if id == m.localID {
			continue
		}
		if h.Conn != nil {
			out = append(out, h.Conn)
		}
	}
	return out
}

// GetAllActivity returns activity snapshots from all remote peers (not local)
func (m *Manager) GetAllActivity() []*activity.Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var all []*activity.Snapshot
	for id, h := range m.hosts {
		if id == m.localID {
			continue
		}
		all = append(all, h.Activity...)
	}
	return all
}

// GetHostName returns the display name for a host ID
func (m *Manager) GetHostName(id string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if h, ok := m.hosts[id]; ok {
		return h.Name
	}
	return ""
}

// HasHost returns true if a host with the given ID is known
func (m *Manager) HasHost(id string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.hosts[id]
	return ok
}

// IsLocal returns true if the given host ID is this node
func (m *Manager) IsLocal(hostID string) bool {
	return hostID == "" || hostID == m.localID
}

// pruneOffline removes peers that have been offline for too long
func (m *Manager) pruneOffline() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	for id, h := range m.hosts {
		if id == m.localID {
			continue
		}
		if !h.Connected && now.Sub(h.LastSeen) > OfflineTimeout {
			delete(m.hosts, id)
			logrus.WithFields(logrus.Fields{
				"peer": h.Name,
				"id":   id,
			}).Info("pruned offline peer")
		}
	}
}
