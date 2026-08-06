package peer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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
	ID       string // public key fingerprint
	Name     string
	Version  string
	PublicKey  string
	Address    string // network address (empty for local)
	Stats      map[string]interface{}
	Activity   []*activity.Snapshot
	Runtime    []state.SessionRuntimeSnapshot // cached remote runtime snapshots
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

	// Coalescing snapshot slots and reliable command queue.
	catalogSlot   *snapshotSlot
	workspaceSlot *snapshotSlot
	cmdQueue      *reliableCommandQueue
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

// initSlotsLazy lazily initializes the snapshot slots/queue. It is safe to call multiple
// times because a real PeerConnection is created once per connection.
func (pc *PeerConnection) initSlotsLazy() {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	if pc.catalogSlot == nil {
		pc.catalogSlot = newSnapshotSlot()
		pc.workspaceSlot = newSnapshotSlot()
		pc.cmdQueue = newReliableCommandQueue(32)
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
	// catalog is this node's own catalog, wired explicitly via
	// SetCatalog.
	catalog *state.Catalog

	// Subscribers for peer connect/disconnect and rename notifications.
	subMu       sync.RWMutex
	subscribers []chan StateEvent

	// Remote catalog caches, keyed by owner ID. They survive reconnects
	// until explicitly forgotten.
	catalogMu      sync.RWMutex
	remoteCatalogs map[state.OwnerID]remoteCatalogCache

	// remoteRevs tracks the last-accepted snapshot revision per peer for the
	// peer's current connection. A new connection (re-registration) resets the
	// baseline so a restarted peer with a fresh revision counter is accepted;
	// within one connection, non-increasing revisions (stale or delayed
	// snapshots) are rejected to stop cache regression. Each connection is
	// tagged with a unique generation so delayed frames from a superseded
	// connection are rejected. Guarded by catalogMu.
	remoteRevs map[string]*remoteRevisionState
	// connGen is a monotonic counter incremented on each new peer connection.
	// Used to tag remoteRevisionState generations. Guarded by catalogMu.
	connGen int64

	// ownerBinding enforces one owner per peer and one peer per owner so a
	// peer cannot publish state under another peer's established owner.
	// Guarded by catalogMu. Owner is an OwnerID (random base32), which is
	// distinct from the peer fingerprint.
	peerOwner map[string]state.OwnerID
	ownerPeer map[state.OwnerID]string

	// Reliable command waiters, keyed by (peer, connection, CommandID) --
	// see commandWaiterKey. A CommandID alone is not unique across peers or
	// across a reconnect (a superseded connection's in-flight waiter and a
	// new connection's waiter can share the same CommandID during a retry),
	// so keying by ID alone let one registration overwrite an unrelated
	// waiter's map entry and let one handler's deferred unregister delete
	// another handler's still-pending waiter.
	cmdMu          sync.Mutex
	commandWaiters map[commandWaiterKey]*commandWaiter

	// remoteStore persists remote catalog caches across restarts.
	remoteStore *state.Store

	// remoteCreate runs local owner-side remote-create sagas and resumes
	// pending creates after restart.
	remoteCreate *state.RemoteCreateCoordinator

	// remoteCatalogSubs notifies observers (e.g. the browser state stream)
	// whenever a remote-owner catalog cache is updated or forgotten. This is
	// read-only fan-out over data UpdateRemoteCatalog/ForgetRemoteCatalog
	// already validated and stored; it does not affect validation/storage.
	// Guarded by its own mutex so a slow subscriber never blocks catalogMu.
	remoteCatalogSubMu  sync.RWMutex
	remoteCatalogSubs   []remoteCatalogSubscription
	nextRemoteCatalogID int

	// hostSnapSubs notifies observers whenever host connectivity changes
	// (connect, disconnect, name, version, or stats changes). Guarded by its
	// own mutex so a slow subscriber never blocks catalogMu or host mutations.
	hostSnapSubMu  sync.RWMutex
	hostSnapSubs   []hostSnapSubscription
	nextHostSnapID int
}

// StateEvent represents a peer connect/disconnect/rename notification
// broadcast by Manager to its subscribers.
type StateEvent struct {
	Type     string      `json:"type"`
	Session  string      `json:"session,omitempty"`
	Host     string      `json:"host,omitempty"`
	HostName string      `json:"host_name,omitempty"`
	Data     interface{} `json:"data,omitempty"`
}

// remoteCatalogSubscription is one registered observer of remote catalog
// cache changes. removed is true when the owner's cache was forgotten
// (peer removed / catalog explicitly forgotten), in which case snap is the
// zero value and callers must treat it as an explicit removal signal, not
// silence.
type remoteCatalogSubscription struct {
	id int
	fn func(owner state.OwnerID, snap state.OwnerCatalogSnapshot, removed bool)
}

// hostSnapSubscription is one registered observer of host snapshot changes.
type hostSnapSubscription struct {
	id int
	fn func([]state.HostSnapshot)
}

// remoteCatalogCache is an immutable, owner-keyed view received from a peer.
type remoteCatalogCache struct {
	Owner        state.OwnerID
	PeerID       string
	Snapshot     state.OwnerCatalogSnapshot
	Workspace    *state.WorkspaceRecord
	ReceivedAt   time.Time
	CatalogRev   int64
	WorkspaceRev int64
}

// remoteRevisionState is the per-peer, per-connection revision baseline for
// snapshot streams. -1 means "no baseline yet" (fresh connection).
// Generation tags the connection that produced this state; a snapshot with a
// mismatched generation is from a stale/superseded connection and is dropped.
type remoteRevisionState struct {
	generation int64 // monotonic connection-generation token
	catalog    int64
	workspace  int64
}

// NewManager creates a new peer manager
func NewManager(id *identity.Identity, peerStore *identity.PeerStore) *Manager {
	m := &Manager{
		hosts:          make(map[string]*HostState),
		localID:        id.Fingerprint(),
		localName:      id.Name,
		identity:       id,
		peerStore:      peerStore,
		remoteCatalogs: make(map[state.OwnerID]remoteCatalogCache),
		remoteRevs:     make(map[string]*remoteRevisionState),
		peerOwner:      make(map[string]state.OwnerID),
		ownerPeer:      make(map[state.OwnerID]string),
		commandWaiters: make(map[commandWaiterKey]*commandWaiter),
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

// SetCatalog wires this node's own catalog. It is decoupled from
// localMgr, so this is the only way Manager learns what its local catalog is.
func (m *Manager) SetCatalog(cat *state.Catalog) {
	m.catalog = cat
}

// updateLocalStats collects system stats for the local host. There is no
// per-node session source to derive process counts from (the
// catalog carries session data instead), so process counting is skipped.
func (m *Manager) updateLocalStats() {
	s := stats.SystemStats()
	m.UpdatePeerStats(m.localID, s)
}

// Run starts forwarding local state events to peer manager subscribers
// and pruning offline peers. Blocks until ctx is cancelled.
//
// There is no local event channel to subscribe to; the catalog's
// own subscription mechanism (SubscribeCatalog/SubscribeWorkspace) carries
// real state instead.
func (m *Manager) Run(ctx context.Context) {
	pruneTimer := time.NewTicker(30 * time.Second)
	defer pruneTimer.Stop()

	statsTimer := time.NewTicker(30 * time.Second)
	defer statsTimer.Stop()

	// Collect initial stats
	m.updateLocalStats()

	for {
		select {
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
func (m *Manager) Subscribe() chan StateEvent {
	ch := make(chan StateEvent, 64)
	m.subMu.Lock()
	m.subscribers = append(m.subscribers, ch)
	m.subMu.Unlock()
	return ch
}

// Unsubscribe removes a subscriber channel
func (m *Manager) Unsubscribe(ch chan StateEvent) {
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
func (m *Manager) broadcast(evt StateEvent) {
	m.subMu.RLock()
	defer m.subMu.RUnlock()
	for _, ch := range m.subscribers {
		select {
		case ch <- evt:
		default:
		}
	}
}

func copySession(s *model.Session) *model.Session {
	if s == nil {
		return nil
	}
	c := *s
	if len(s.Windows) > 0 {
		c.Windows = make([]*model.Window, 0, len(s.Windows))
		for _, w := range s.Windows {
			if w == nil {
				c.Windows = append(c.Windows, nil)
				continue
			}
			cw := *w
			if len(w.Panes) > 0 {
				cw.Panes = make([]*model.Pane, 0, len(w.Panes))
				for _, p := range w.Panes {
					if p == nil {
						cw.Panes = append(cw.Panes, nil)
						continue
					}
					cp := *p
					cw.Panes = append(cw.Panes, &cp)
				}
			}
			c.Windows = append(c.Windows, &cw)
		}
	}
	return &c
}

// GetHosts returns info about all known hosts. Session slices are copied so
// callers cannot mutate internal state.
func (m *Manager) GetHosts() []HostInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	hosts := make([]HostInfo, 0, len(m.hosts))
	for _, h := range m.hosts {
		owner, _ := m.ownerIDForPeerLocked(h.ID)
		hosts = append(hosts, HostInfo{
			ID:       h.ID,
			OwnerID:  string(owner),
			Name:     h.Name,
			Version:  h.Version,
			Local:    h.ID == m.localID,
			Online:   h.Connected,
			Address:  h.Address,
			Sessions: []*model.Session{},
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
	owner, _ := m.ownerIDForPeerLocked(h.ID)
	return []HostInfo{{
		ID:       h.ID,
		OwnerID:  string(owner),
		Name:     h.Name,
		Version:  h.Version,
		Local:    true,
		Online:   h.Connected,
		Address:  h.Address,
		Sessions: []*model.Session{},
		Activity: h.Activity,
		Stats:    h.Stats,
		LastSeen: h.LastSeen,
	}}
}

// HostSnapshots returns a deterministically ordered slice of all known hosts
// as state.HostSnapshot values (suitable for state streaming and bootstrap).
func (m *Manager) HostSnapshots() []state.HostSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	snapshots := make([]state.HostSnapshot, 0, len(m.hosts))
	for _, h := range m.hosts {
		owner, _ := m.ownerIDForPeerLocked(h.ID)
		snapshots = append(snapshots, state.HostSnapshot{
			PeerID:   h.ID,
			OwnerID:  owner,
			Name:     h.Name,
			Version:  h.Version,
			Local:    h.ID == m.localID,
			Online:   h.Connected,
			LastSeen: h.LastSeen,
			Stats:    h.Stats,
		})
	}
	// Sort by PeerID for deterministic ordering.
	sort.Slice(snapshots, func(i, j int) bool {
		return snapshots[i].PeerID < snapshots[j].PeerID
	})
	return snapshots
}

// SubscribeHostSnapshots registers a callback that receives complete host
// snapshot lists after any connectivity, name, version, or stats changes.
// The returned function unsubscribes.
func (m *Manager) SubscribeHostSnapshots(fn func([]state.HostSnapshot)) func() {
	m.hostSnapSubMu.Lock()
	defer m.hostSnapSubMu.Unlock()
	m.nextHostSnapID++
	sub := hostSnapSubscription{id: m.nextHostSnapID, fn: fn}
	m.hostSnapSubs = append(m.hostSnapSubs, sub)
	return func() {
		m.hostSnapSubMu.Lock()
		defer m.hostSnapSubMu.Unlock()
		filtered := m.hostSnapSubs[:0]
		for _, s := range m.hostSnapSubs {
			if s.id != sub.id {
				filtered = append(filtered, s)
			}
		}
		m.hostSnapSubs = filtered
	}
}

// publishHostSnapshots emits the current host snapshot list to all subscribers.
func (m *Manager) publishHostSnapshots() {
	snapshots := m.HostSnapshots()
	m.hostSnapSubMu.RLock()
	subs := make([]hostSnapSubscription, len(m.hostSnapSubs))
	copy(subs, m.hostSnapSubs)
	m.hostSnapSubMu.RUnlock()
	for _, s := range subs {
		s.fn(snapshots)
	}
}

// ownerIDForPeerLocked returns the catalog OwnerID for a peer fingerprint.
// Caller must hold m.mu for reading (or writing).
//
// For the local peer, the OwnerID is this node's own catalog owner (empty,
// ok=false, if no catalog is wired).
// For a remote peer, the OwnerID is the canonical deterministic conversion of
// its authenticated fingerprint (state.OwnerIDFromFingerprint) -- the same
// forward mapping validateCatalogOwnership already requires every remote
// catalog snapshot's Owner field to equal. This is the forward direction
// only (fingerprint -> OwnerID); the reverse direction (OwnerID -> peer
// connection) must go through PeerIDForOwner's remoteCatalogs-backed lookup,
// never a re-derivation, because a remote catalog binding is only considered
// established once that peer has actually published a snapshot.
func (m *Manager) ownerIDForPeerLocked(peerID string) (state.OwnerID, bool) {
	if peerID == "" {
		return "", false
	}
	if peerID == m.localID {
		if m.catalog == nil {
			return "", false
		}
		return m.catalog.Owner(), true
	}
	return state.OwnerIDFromFingerprint(peerID), true
}

// OwnerIDForPeer returns the catalog OwnerID for a peer's authenticated
// fingerprint (see ownerIDForPeerLocked). Exported for route handlers and
// frontend-facing encoders that must translate a transport peer identity
// into the OwnerID domain (e.g. before threading it into a SessionRef or
// the target_owner wire field), instead of conflating the two identity
// spaces by using the fingerprint directly.
func (m *Manager) OwnerIDForPeer(peerID string) (state.OwnerID, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.ownerIDForPeerLocked(peerID)
}

// IsLocalOwner reports whether owner names this node's own catalog. It is
// the OwnerID-domain equivalent of IsLocal (which compares peer transport
// fingerprints) and must be used wherever a caller's "is this host me"
// value is an OwnerID rather than a fingerprint -- e.g. a
// terminal attach's `host` query parameter, which carries SessionRef.Owner,
// not a peer ID.
func (m *Manager) IsLocalOwner(owner state.OwnerID) bool {
	if owner == "" {
		return false
	}
	m.mu.RLock()
	cat := m.catalog
	m.mu.RUnlock()
	if cat == nil {
		return false
	}
	return owner == cat.Owner()
}

// ResolveHostParam resolves a `host` request parameter that may be either an
// OwnerID (what a session's SessionRef.Owner / the frontend's
// state/session/paneTreeAdapter.ts sessionRefToKey actually carry) or a
// peer fingerprint (what model.Session.Host carries). Before this
// method existed, callers compared the raw `host` value against fingerprints
// via IsLocal/GetPeerConnection unconditionally, which silently misrouted
// every OwnerID-routed remote host (misclassified as local, or resolved
// against no live connection) because an OwnerID is a different string
// encoding than its owner's fingerprint.
//
// It tries the OwnerID interpretation first (the identity domain the
// frontend actually sends); if host does not resolve as a known owner, it
// falls back to treating host as a peer fingerprint so other
// callers/peers keep working unchanged. Returns isLocal=true (peerID="") if
// host names this node itself under either interpretation; otherwise
// returns the fingerprint of the live peer connection to use, or "" if host
// resolves under neither interpretation.
func (m *Manager) ResolveHostParam(host string) (peerID string, isLocal bool) {
	if host == "" {
		return "", true
	}
	if m.IsLocalOwner(state.OwnerID(host)) {
		return "", true
	}
	if pid := m.PeerIDForOwner(state.OwnerID(host)); pid != "" {
		return pid, false
	}
	if m.IsLocal(host) {
		return "", true
	}
	return host, false
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

// SetRemoteStore wires the app-state store used to persist remote catalog
// caches. It is safe to call before any peer connects.
func (m *Manager) SetRemoteStore(store *state.Store) {
	m.catalogMu.Lock()
	defer m.catalogMu.Unlock()
	m.remoteStore = store
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

	// A new connection is a new snapshot-stream generation: reset the
	// revision baseline so a restarted peer's re-emitted snapshots are
	// accepted even though their revisions are lower than the last ones seen
	// on the previous connection.
	m.resetRemoteRevisions(id)

	m.broadcast(StateEvent{
		Type:     "peer-connected",
		Host:     id,
		HostName: name,
	})

	m.publishHostSnapshots()

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

	m.resetRemoteRevisions(id)

	m.broadcast(StateEvent{
		Type:     "peer-connected",
		Host:     id,
		HostName: name,
	})

	m.publishHostSnapshots()

	logrus.WithFields(logrus.Fields{
		"peer": name,
		"id":   id,
	}).Info("peer connected")
	return true
}

// UnregisterPeer marks the peer offline unconditionally. Prefer
// UnregisterPeerConn to prevent a stale connection from unregistering a
// replacement.
func (m *Manager) UnregisterPeer(id string) {
	m.UnregisterPeerConn(id, nil)
}

// UnregisterPeerConn marks the peer offline only if conn is the currently
// registered connection. A nil conn acts as an unconditional unregister.
func (m *Manager) UnregisterPeerConn(id string, conn *PeerConnection) {
	m.mu.Lock()
	h, ok := m.hosts[id]
	if ok {
		if conn != nil && h.Conn != conn {
			m.mu.Unlock()
			logrus.WithFields(logrus.Fields{
				"peer": h.Name,
				"id":   id,
			}).Debug("stale connection ignored during unregister")
			return
		}
		h.Connected = false
		h.Conn = nil
		h.LastSeen = time.Now()
	}
	m.mu.Unlock()

	if ok {
		m.broadcast(StateEvent{
			Type:     "peer-disconnected",
			Host:     id,
			HostName: h.Name,
		})

		m.publishHostSnapshots()

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
		m.forgetRemoteCatalogsForPeer(id)
		m.broadcast(StateEvent{
			Type:     "peer-disconnected",
			Host:     id,
			HostName: h.Name,
		})
		m.publishHostSnapshots()
		logrus.WithFields(logrus.Fields{
			"peer": h.Name,
			"id":   id,
		}).Info("host removed")
	}
}

// validateCatalogOwnership checks that all sessions and layouts in the snapshot
// have their Owner field matching the snapshot's top-level owner, and that each
// session's embedded Ref.Owner also matches. Returns false and logs a warning if
// any embedded owner differs.
func validateCatalogOwnership(peerID string, snap state.OwnerCatalogSnapshot) bool {
	for _, sess := range snap.Sessions {
		if sess.Owner != snap.Owner {
			logrus.WithFields(logrus.Fields{
				"peer":          peerID,
				"owner":         string(snap.Owner),
				"session_id":    string(sess.ID),
				"session_owner": string(sess.Owner),
			}).Warn("dropping snapshot: embedded session owner mismatch")
			return false
		}
		if sess.Ref.Owner != snap.Owner {
			logrus.WithFields(logrus.Fields{
				"peer":       peerID,
				"owner":      string(snap.Owner),
				"session_id": string(sess.ID),
				"ref_owner":  string(sess.Ref.Owner),
			}).Warn("dropping snapshot: embedded session ref owner mismatch")
			return false
		}
	}
	// Workspace is now a singleton and already owned by snap.Owner
	return true
}

// validateCatalogInvariants checks deep structural invariants on the catalog snapshot:
//  1. Each session's Ref.Session must match its own record ID
//  2. Workspace tree structure must be valid (checked via ValidatePaneTree)
//  3. Workspace leaf refs must have owner matching snap.Owner and must
//     correspond to an actual session in the snapshot
//
// Returns false and logs details if any check fails.
func validateCatalogInvariants(peerID string, snap state.OwnerCatalogSnapshot) bool {
	// Build a map of known session IDs for quick lookup
	knownSessions := make(map[state.SessionID]struct{}, len(snap.Sessions))
	for _, sess := range snap.Sessions {
		knownSessions[sess.ID] = struct{}{}
		// Check that Ref.Session matches the session's own ID
		if sess.Ref.Session != sess.ID {
			logrus.WithFields(logrus.Fields{
				"peer":           peerID,
				"session_id":     string(sess.ID),
				"ref_session_id": string(sess.Ref.Session),
			}).Warn("dropping snapshot: session Ref.Session does not match its own ID")
			return false
		}
	}

	// Validate the singleton workspace tree and check that leaf refs correspond to known sessions
	if snap.Workspace != nil && snap.Workspace.Tree != nil {
		// Use canonical ValidatePaneTree to check structure and duplicate leaves
		if err := state.ValidatePaneTree(*snap.Workspace.Tree); err != nil {
			var stateErr state.StateError
			if errors.As(err, &stateErr) {
				logrus.WithFields(logrus.Fields{
					"peer":   peerID,
					"code":   stateErr.Code,
					"field":  stateErr.Field,
					"detail": stateErr.Detail,
				}).Warn("dropping snapshot: invalid pane tree structure")
			} else {
				logrus.WithFields(logrus.Fields{
					"peer":  peerID,
					"error": err.Error(),
				}).Warn("dropping snapshot: invalid pane tree structure")
			}
			return false
		}

		// Check that all leaf refs correspond to known sessions
		leaves, err := collectTreeLeaves(*snap.Workspace.Tree)
		if err != nil {
			logrus.WithFields(logrus.Fields{
				"peer":  peerID,
				"error": err.Error(),
			}).Warn("dropping snapshot: failed to collect leaf refs")
			return false
		}
		for _, ref := range leaves {
			if ref.Owner != snap.Owner {
				logrus.WithFields(logrus.Fields{
					"peer":      peerID,
					"owner":     string(snap.Owner),
					"ref":       ref.String(),
					"ref_owner": string(ref.Owner),
				}).Warn("dropping snapshot: workspace leaf ref owner mismatch")
				return false
			}
			if _, exists := knownSessions[ref.Session]; !exists {
				logrus.WithFields(logrus.Fields{
					"peer": peerID,
					"ref":  ref.String(),
				}).Warn("dropping snapshot: workspace leaf references unknown session")
				return false
			}
		}
	}
	return true
}

// collectTreeLeaves extracts all leaf SessionRefs from a pane tree.
// It reuses the logic from state.ValidatePaneTree's collectLeaves.
func collectTreeLeaves(tree state.PaneNode) ([]state.SessionRef, error) {
	var collect func(state.PaneNode) ([]state.SessionRef, error)
	collect = func(node state.PaneNode) ([]state.SessionRef, error) {
		if node.IsLeaf() {
			if node.Ref == nil {
				return nil, state.StateError{Code: state.ErrMalformedSplit, Field: "leaf", Detail: "leaf pane has nil ref"}
			}
			return []state.SessionRef{*node.Ref}, nil
		}
		if !node.IsSplit() {
			return nil, state.StateError{Code: state.ErrMalformedSplit, Field: "type", Detail: fmt.Sprintf("unknown pane node type %q", node.Type)}
		}
		if node.Direction != state.DirectionHorizontal && node.Direction != state.DirectionVertical {
			return nil, state.StateError{Code: state.ErrMalformedSplit, Field: "direction", Detail: fmt.Sprintf("invalid direction %q", node.Direction)}
		}
		if err := node.Ratio.Validate(); err != nil {
			return nil, state.StateError{Code: state.ErrInvalidRatio, Field: "ratio", Detail: err.Error()}
		}
		if node.First == nil || node.Second == nil {
			return nil, state.StateError{Code: state.ErrMalformedSplit, Field: "split", Detail: "split node missing child"}
		}
		first, err := collect(*node.First)
		if err != nil {
			return nil, err
		}
		second, err := collect(*node.Second)
		if err != nil {
			return nil, err
		}
		return append(first, second...), nil
	}
	return collect(tree)
}

// validateWorkspaceTreeOwnership recursively checks that all leaf SessionRefs in
// the tree have owner matching rec.Owner. Returns false and logs a warning if
// any leaf ref differs.
func validateWorkspaceTreeOwnership(peerID string, rec state.WorkspaceRecord) bool {
	var checkNode func(*state.PaneNode) bool
	checkNode = func(node *state.PaneNode) bool {
		if node == nil {
			return true
		}
		if node.IsLeaf() {
			if node.Ref != nil && node.Ref.Owner != rec.Owner {
				logrus.WithFields(logrus.Fields{
					"peer":      peerID,
					"owner":     string(rec.Owner),
					"ref_owner": string(node.Ref.Owner),
				}).Warn("dropping snapshot: embedded leaf owner mismatch")
				return false
			}
			return true
		}
		if node.IsSplit() {
			if !checkNode(node.First) || !checkNode(node.Second) {
				return false
			}
		}
		return true
	}
	return checkNode(rec.Tree)
}

// validateWorkspaceInvariants checks deep structural invariants on a workspace record:
// 1. Tree structure must be valid (checked via ValidatePaneTree to catch malformed trees and duplicate leaves)
// 2. All leaf SessionRefs must have owner matching rec.Owner (already validated by validateWorkspaceTreeOwnership)
// Returns false and logs details if any check fails.
func validateWorkspaceInvariants(peerID string, rec state.WorkspaceRecord) bool {
	// Use canonical ValidatePaneTree to check structure and duplicate leaves
	if rec.Tree == nil {
		return true // Empty workspace is valid
	}
	if err := state.ValidatePaneTree(*rec.Tree); err != nil {
		var stateErr state.StateError
		if errors.As(err, &stateErr) {
			logrus.WithFields(logrus.Fields{
				"peer":   peerID,
				"code":   stateErr.Code,
				"field":  stateErr.Field,
				"detail": stateErr.Detail,
			}).Warn("dropping snapshot: invalid workspace pane tree structure")
		} else {
			logrus.WithFields(logrus.Fields{
				"peer":  peerID,
				"error": err.Error(),
			}).Warn("dropping snapshot: invalid workspace pane tree structure")
		}
		return false
	}
	return true
}

// isConnectionStill reports whether conn is the currently-active connection for
// peerID. Used to reject snapshots from superseded/stale connections. Caller
// must hold catalogMu.
func (m *Manager) isConnectionStill(peerID string, conn *PeerConnection) bool {
	if conn == nil {
		return false
	}
	m.mu.RLock()
	hostState, ok := m.hosts[peerID]
	m.mu.RUnlock()
	if !ok {
		return false
	}
	return hostState.Conn == conn
}

// bindRemoteOwner enforces the ownership binding: one owner per peer and one
// peer per owner. The first snapshot a peer publishes establishes its sole
// owner; a later snapshot from the same peer under a different owner, or a
// second peer claiming an already-bound owner, is a spoof and is dropped.
// Caller must hold catalogMu. The catalog Owner MUST equal the peer's
// authenticated fingerprint (peerID), so the binding is an authenticated fact,
// not derived from the snapshot.
func (m *Manager) bindRemoteOwner(peerID string, owner state.OwnerID) bool {
	if peerID == "" || owner == "" {
		return false
	}
	if p, ok := m.ownerPeer[owner]; ok && p != peerID {
		logrus.WithFields(logrus.Fields{"peer": peerID, "owner": string(owner), "boundPeer": p}).Warn("dropping snapshot: owner already bound to another peer")
		return false
	}
	if o, ok := m.peerOwner[peerID]; ok && o != owner {
		logrus.WithFields(logrus.Fields{"peer": peerID, "owner": string(owner), "boundOwner": string(o)}).Warn("dropping snapshot: peer switched owners")
		return false
	}
	m.peerOwner[peerID] = owner
	m.ownerPeer[owner] = peerID
	return true
}

// UpdateRemoteCatalog replaces the cached catalog for the snapshot's owner.
// The previous cache (if any) is merged (not overwritten). The owner must equal
// the peer's authenticated identity (enforced via peerID == snap.Owner assumption),
// the peer's bound owner (one owner per peer, one peer per owner), and the
// revision must be increasing within the peer's current connection (stale/delayed
// snapshots from superseded connections are rejected). All embedded session and
// layout owners must match the snapshot's owner. Deep structural invariants are
// checked: each session's Ref.Session must match its ID, layout trees must be
// valid (no duplicate leaves, no malformed structure), and leaf refs must
// correspond to known sessions. If a remote store is wired, the complete set of
// accepted remote catalogs is persisted.
func (m *Manager) UpdateRemoteCatalog(peerID string, conn *PeerConnection, snap state.OwnerCatalogSnapshot) {
	if !validateCatalogOwnership(peerID, snap) {
		return
	}
	if !validateCatalogInvariants(peerID, snap) {
		return
	}
	// Enforce: remote peer's OwnerID must equal its authenticated fingerprint (peerID).
	expectedOwner := state.OwnerIDFromFingerprint(peerID)
	if snap.Owner != expectedOwner {
		logrus.WithFields(logrus.Fields{
			"peer":           peerID,
			"claimed_owner":  string(snap.Owner),
			"expected_owner": string(expectedOwner),
		}).Warn("dropping catalog snapshot: owner does not match authenticated peer identity")
		return
	}
	m.catalogMu.Lock()
	// Check that the snapshot comes from the current active connection (generation match).
	rs := m.remoteRevs[peerID]
	if rs == nil || conn == nil || !m.isConnectionStill(peerID, conn) {
		m.catalogMu.Unlock()
		logrus.WithFields(logrus.Fields{"peer": peerID}).Debug("dropping catalog snapshot: connection generation mismatch or stale")
		return
	}
	if !m.bindRemoteOwner(peerID, snap.Owner) {
		m.catalogMu.Unlock()
		return
	}
	if rs.catalog >= 0 && snap.Revision <= rs.catalog {
		m.catalogMu.Unlock()
		logrus.WithFields(logrus.Fields{"peer": peerID, "rev": snap.Revision, "last": rs.catalog}).Debug("dropping stale catalog snapshot")
		return
	}
	rs.catalog = snap.Revision
	// Merge: preserve existing workspace, update catalog fields.
	cache := m.remoteCatalogs[snap.Owner]
	cache.Owner = snap.Owner
	cache.PeerID = peerID
	cache.Snapshot = snap
	cache.ReceivedAt = time.Now()
	cache.CatalogRev = snap.Revision
	// Do not modify Workspace or WorkspaceRev; let them persist.
	m.remoteCatalogs[snap.Owner] = cache
	m.catalogMu.Unlock()
	m.persistRemoteCatalogs()
	m.notifyRemoteCatalog(snap.Owner, cloneOwnerCatalogSnapshot(snap), false)
}

// UpdateRemoteWorkspace merges the cached workspace for owner. The owner
// must equal the peer's authenticated identity (enforced via peerID == rec.Owner),
// must be the peer's bound owner, and the workspace revision must be
// increasing within the current connection (keyed per layout id). All
// embedded leaf SessionRefs in the tree must have owner matching rec.Owner.
// Deep structural invariants are checked: tree must be valid (no duplicate
// leaves, no malformed structure).
func (m *Manager) UpdateRemoteWorkspace(peerID string, conn *PeerConnection, rec state.WorkspaceRecord) {
	if !validateWorkspaceTreeOwnership(peerID, rec) {
		return
	}
	if !validateWorkspaceInvariants(peerID, rec) {
		return
	}
	// Enforce: remote peer's OwnerID must equal its authenticated fingerprint (peerID).
	expectedOwner := state.OwnerIDFromFingerprint(peerID)
	if rec.Owner != expectedOwner {
		logrus.WithFields(logrus.Fields{
			"peer":           peerID,
			"claimed_owner":  string(rec.Owner),
			"expected_owner": string(expectedOwner),
		}).Warn("dropping workspace snapshot: owner does not match authenticated peer identity")
		return
	}
	m.catalogMu.Lock()
	// Check that the snapshot comes from the current active connection (generation match).
	rs := m.remoteRevs[peerID]
	if rs == nil || conn == nil || !m.isConnectionStill(peerID, conn) {
		m.catalogMu.Unlock()
		logrus.WithFields(logrus.Fields{"peer": peerID}).Debug("dropping workspace snapshot: connection generation mismatch or stale")
		return
	}
	if !m.bindRemoteOwner(peerID, rec.Owner) {
		m.catalogMu.Unlock()
		return
	}
	if rs.workspace > 0 && rec.Revision <= rs.workspace {
		m.catalogMu.Unlock()
		logrus.WithFields(logrus.Fields{"peer": peerID, "rev": rec.Revision, "last": rs.workspace}).Debug("dropping stale workspace snapshot")
		return
	}
	rs.workspace = rec.Revision
	// Merge: preserve existing catalog, update workspace fields.
	cache := m.remoteCatalogs[rec.Owner]
	cache.Owner = rec.Owner
	cache.PeerID = peerID
	cache.Workspace = &rec
	cache.ReceivedAt = time.Now()
	cache.WorkspaceRev = rec.Revision
	// Do not modify Snapshot or CatalogRev; let them persist.
	m.remoteCatalogs[rec.Owner] = cache
	m.catalogMu.Unlock()
	m.persistRemoteCatalogs()
}

// RemoteCatalogSnapshot returns the latest cached catalog for owner, if any.
func (m *Manager) RemoteCatalogSnapshot(owner state.OwnerID) (state.OwnerCatalogSnapshot, bool) {
	m.catalogMu.RLock()
	defer m.catalogMu.RUnlock()
	c, ok := m.remoteCatalogs[owner]
	if !ok {
		return state.OwnerCatalogSnapshot{}, false
	}
	return cloneOwnerCatalogSnapshot(c.Snapshot), true
}

// AllRemoteCatalogSnapshots returns a defensive-copy, owner-sorted list of
// every cached remote-owner catalog. This is the read-only accessor the
// browser-facing aggregation (pkg/state.AggregateCatalog) uses to combine
// this node's local catalog with what its peers have published; it does not
// participate in validation or storage, which remain exclusively in
// UpdateRemoteCatalog.
func (m *Manager) AllRemoteCatalogSnapshots() []state.OwnerCatalogSnapshot {
	m.catalogMu.RLock()
	defer m.catalogMu.RUnlock()
	out := make([]state.OwnerCatalogSnapshot, 0, len(m.remoteCatalogs))
	for _, c := range m.remoteCatalogs {
		out = append(out, cloneOwnerCatalogSnapshot(c.Snapshot))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Owner < out[j].Owner })
	return out
}

// SubscribeRemoteCatalogs registers fn to be called whenever a remote-owner
// catalog cache is updated (removed=false, snap is the new cached snapshot)
// or forgotten (removed=true, snap is the zero value -- an explicit removal
// signal, e.g. the peer disconnected and was forgotten, distinct from mere
// silence). The returned function unsubscribes. fn is invoked synchronously
// and outside catalogMu; a slow subscriber can delay other subscribers but
// never blocks catalog validation/storage.
func (m *Manager) SubscribeRemoteCatalogs(fn func(owner state.OwnerID, snap state.OwnerCatalogSnapshot, removed bool)) func() {
	m.remoteCatalogSubMu.Lock()
	m.nextRemoteCatalogID++
	id := m.nextRemoteCatalogID
	m.remoteCatalogSubs = append(m.remoteCatalogSubs, remoteCatalogSubscription{id: id, fn: fn})
	m.remoteCatalogSubMu.Unlock()
	return func() {
		m.remoteCatalogSubMu.Lock()
		defer m.remoteCatalogSubMu.Unlock()
		filtered := m.remoteCatalogSubs[:0]
		for _, s := range m.remoteCatalogSubs {
			if s.id != id {
				filtered = append(filtered, s)
			}
		}
		m.remoteCatalogSubs = filtered
	}
}

func (m *Manager) notifyRemoteCatalog(owner state.OwnerID, snap state.OwnerCatalogSnapshot, removed bool) {
	m.remoteCatalogSubMu.RLock()
	subs := make([]remoteCatalogSubscription, len(m.remoteCatalogSubs))
	copy(subs, m.remoteCatalogSubs)
	m.remoteCatalogSubMu.RUnlock()
	for _, s := range subs {
		s.fn(owner, snap, removed)
	}
}

// RemoteWorkspaceSnapshot returns the latest cached workspace for owner, if any.
func (m *Manager) RemoteWorkspaceSnapshot(owner state.OwnerID) (*state.WorkspaceRecord, bool) {
	m.catalogMu.RLock()
	defer m.catalogMu.RUnlock()
	c, ok := m.remoteCatalogs[owner]
	if !ok || c.Workspace == nil {
		return nil, false
	}
	w := *c.Workspace
	return &w, true
}

// ForgetRemoteCatalog removes the cached catalog and workspace for owner.
func (m *Manager) ForgetRemoteCatalog(owner state.OwnerID) {
	m.catalogMu.Lock()
	delete(m.remoteCatalogs, owner)
	m.catalogMu.Unlock()
	m.persistRemoteCatalogs()
	m.notifyRemoteCatalog(owner, state.OwnerCatalogSnapshot{}, true)
}

// SetRemoteCreateCoordinator wires the owner-side remote create coordinator.
func (m *Manager) SetRemoteCreateCoordinator(c *state.RemoteCreateCoordinator) {
	m.remoteCreate = c
}



// RequestRemoteCreate routes a remote create to the workspace owner. The local
// owner path runs directly in the coordinator; remote owners are reached over
// the remote-create RPC.
func (m *Manager) RequestRemoteCreate(ctx context.Context, owner state.OwnerID, req state.RemoteCreateRequest) (state.RemoteCreateResult, error) {
	if owner == "" || owner == state.OwnerIDFromFingerprint(m.localID) {
		if m.remoteCreate == nil {
			return state.RemoteCreateResult{}, fmt.Errorf("remote create coordinator not available")
		}
		return m.remoteCreate.ExecuteRemoteCreate(ctx, req)
	}
	peerID := m.peerIDForOwner(owner)
	if peerID == "" {
		return state.RemoteCreateResult{}, state.StateError{Code: state.ErrWorkspaceOwnerOffline, Field: "owner", Detail: fmt.Sprintf("workspace owner %q is offline", owner)}
	}
	pc := m.GetPeerConnection(peerID)
	if pc == nil {
		return state.RemoteCreateResult{}, state.StateError{Code: state.ErrWorkspaceOwnerOffline, Field: "owner", Detail: fmt.Sprintf("workspace owner %q is offline", owner)}
	}
	return m.SendRemoteCreate(ctx, peerID, req)
}

func (m *Manager) peerIDForOwner(owner state.OwnerID) string {
	m.catalogMu.RLock()
	defer m.catalogMu.RUnlock()
	if c, ok := m.remoteCatalogs[owner]; ok {
		return c.PeerID
	}
	return ""
}

// PeerIDForOwner is the exported form of peerIDForOwner, used by route
// handlers outside this package (e.g. routes_sessions.go rename/kill handlers) to resolve
// which live peer connection currently owns a given remote catalog owner,
// so a session command targeting that owner can be forwarded via
// SendCommand instead of executed locally. Returns "" if no peer is
// currently known to own that owner ID (e.g. never seen, or forgotten).
func (m *Manager) PeerIDForOwner(owner state.OwnerID) string {
	return m.peerIDForOwner(owner)
}

// LoadRemoteCatalogCache loads persisted remote catalogs into memory. It is
// idempotent and ignores missing sidecar files. For each loaded catalog,
// restores the peer ownership binding based on the owner value (which is
// expected to equal the peer's authenticated fingerprint).
func (m *Manager) LoadRemoteCatalogCache() error {
	if m.remoteStore == nil {
		return nil
	}
	entries, err := m.remoteStore.LoadRemoteCatalogs()
	if err != nil {
		return err
	}
	m.catalogMu.Lock()
	defer m.catalogMu.Unlock()
	for _, e := range entries {
		c := e.Snapshot
		// Restore the peer fingerprint exactly as persisted alongside the
		// snapshot. It must NOT be re-derived from c.Owner: OwnerID and peer
		// fingerprints live in different, non-invertible identifier spaces
		// (see state.OwnerIDFromFingerprint), so any live connection from
		// this peer after a restart would never resolve if the binding were
		// reconstructed from the owner string instead of the real
		// fingerprint that was authenticated at connect time.
		peerID := e.PeerID
		if peerID == "" {
			// No fingerprint was ever recorded for this cache entry (e.g. an
			// older on-disk cache or manual edit). Skip restoring the
			// ownership binding rather than guessing one, but keep the
			// catalog snapshot available for display.
			m.remoteCatalogs[c.Owner] = remoteCatalogCache{
				Owner:      c.Owner,
				Snapshot:   c,
				ReceivedAt: time.Now(),
				CatalogRev: c.Revision,
			}
			continue
		}
		m.remoteCatalogs[c.Owner] = remoteCatalogCache{
			Owner:      c.Owner,
			PeerID:     peerID,
			Snapshot:   c,
			ReceivedAt: time.Now(),
			CatalogRev: c.Revision,
		}
		// Restore the ownership binding: owner -> peerID.
		m.peerOwner[peerID] = c.Owner
		m.ownerPeer[c.Owner] = peerID
	}
	return nil
}

func (m *Manager) persistRemoteCatalogs() {
	m.catalogMu.RLock()
	store := m.remoteStore
	if store == nil {
		m.catalogMu.RUnlock()
		return
	}
	entries := make([]state.RemoteCatalogCacheEntry, 0, len(m.remoteCatalogs))
	for _, c := range m.remoteCatalogs {
		entries = append(entries, state.RemoteCatalogCacheEntry{
			PeerID:   c.PeerID,
			Snapshot: cloneOwnerCatalogSnapshot(c.Snapshot),
		})
	}
	m.catalogMu.RUnlock()

	sort.Slice(entries, func(i, j int) bool { return entries[i].Snapshot.Owner < entries[j].Snapshot.Owner })
	if err := store.SaveRemoteCatalogs(entries); err != nil {
		logrus.WithError(err).Warn("failed to persist remote catalog caches")
	}
}

func (m *Manager) forgetRemoteCatalogsForPeer(peerID string) {
	m.catalogMu.Lock()
	var removedOwners []state.OwnerID
	for owner, c := range m.remoteCatalogs {
		if c.PeerID == peerID {
			delete(m.remoteCatalogs, owner)
			removedOwners = append(removedOwners, owner)
		}
	}
	// Clear the ownership binding so a subsequently re-added peer starts
	// clean instead of being latched to a stale owner.
	if owner, ok := m.peerOwner[peerID]; ok {
		delete(m.ownerPeer, owner)
		delete(m.peerOwner, peerID)
	}
	m.catalogMu.Unlock()
	// Persist the removal so a forgotten peer does not reappear after a
	// restart (the sidecar is written from the in-memory map).
	m.persistRemoteCatalogs()
	for _, owner := range removedOwners {
		m.notifyRemoteCatalog(owner, state.OwnerCatalogSnapshot{}, true)
	}
}

// resetRemoteRevisions increments the connection generation for a peer and
// initializes a fresh revision baseline. Called whenever a new connection
// registers. Returns the new generation token.
func (m *Manager) resetRemoteRevisions(peerID string) int64 {
	m.catalogMu.Lock()
	m.connGen++
	gen := m.connGen
	m.remoteRevs[peerID] = &remoteRevisionState{
		generation: gen,
		catalog:    -1,
	}
	m.catalogMu.Unlock()
	return gen
}

func cloneOwnerCatalogSnapshot(s state.OwnerCatalogSnapshot) state.OwnerCatalogSnapshot {
	sessions := make([]state.LocalSessionRecord, len(s.Sessions))
	copy(sessions, s.Sessions)
	var workspace *state.WorkspaceRecord
	if s.Workspace != nil {
		cp := *s.Workspace
		if cp.Tree != nil {
			treecp := *cp.Tree
			cp.Tree = &treecp
		}
		workspace = &cp
	}
	return state.OwnerCatalogSnapshot{
		Owner:     s.Owner,
		Revision:  s.Revision,
		Sessions:  sessions,
		Workspace: workspace,
	}
}

// UpdatePeerVersion updates a peer's reported version
func (m *Manager) UpdatePeerVersion(id, version string) {
	m.mu.Lock()
	if h, ok := m.hosts[id]; ok {
		h.Version = version
	}
	m.mu.Unlock()
	m.publishHostSnapshots()
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
	m.publishHostSnapshots()
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

// UpdatePeerRuntime caches a peer's runtime snapshots (volatile session state).
func (m *Manager) UpdatePeerRuntime(id string, owner state.OwnerID, snapshots []state.SessionRuntimeSnapshot) {
	m.mu.Lock()
	if h, ok := m.hosts[id]; ok {
		h.Runtime = snapshots
		h.LastSeen = time.Now()
	}
	m.mu.Unlock()
}

// GetAllRuntimeSnapshots returns runtime snapshots from all remote peers (not local).
func (m *Manager) GetAllRuntimeSnapshots() []state.OwnerRuntimeSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var all []state.OwnerRuntimeSnapshot
	for id, h := range m.hosts {
		if id == m.localID {
			continue
		}
		if len(h.Runtime) == 0 {
			continue
		}
		// Determine owner from peerOwner binding.
		if owner, ok := m.peerOwner[id]; ok {
			all = append(all, state.OwnerRuntimeSnapshot{
				Owner:     owner,
				Snapshots: h.Runtime,
			})
		}
	}
	return all
}

// GetRemoteOwner returns the owner ID bound to a peer connection ID.
func (m *Manager) GetRemoteOwner(peerID string) state.OwnerID {
	m.catalogMu.RLock()
	defer m.catalogMu.RUnlock()
	return m.peerOwner[peerID]
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

// pruneOffline marks long-offline peers as offline. It deliberately does not
// delete remote catalog caches; those are retained through reconnects and
// only dropped on explicit forget.
func (m *Manager) pruneOffline() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	for id, h := range m.hosts {
		if id == m.localID {
			continue
		}
		if !h.Connected && now.Sub(h.LastSeen) > OfflineTimeout {
			// Keep the host entry so its cached catalog remains addressable by
			// peer ID; only mark it as stale.
			h.Connected = false
			logrus.WithFields(logrus.Fields{
				"peer": h.Name,
				"id":   id,
			}).Debug("peer offline timeout reached; catalog retained")
		}
	}
}
