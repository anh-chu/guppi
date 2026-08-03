package peer

import (
	"context"
	"encoding/json"
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

	// v2 coalescing snapshot slots and reliable command queue.
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

// initV2Lazy lazily initializes v2 slots/queue. It is safe to call multiple
// times because a real PeerConnection is created once per connection.
func (pc *PeerConnection) initV2Lazy() {
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
	localMgr  *state.Manager

	// Subscribers for state changes (browser WebSocket hub subscribes here)
	subMu       sync.RWMutex
	subscribers []chan state.StateEvent

	// v2 remote catalog caches, keyed by owner ID. They survive reconnects
	// until explicitly forgotten.
	catalogMu      sync.RWMutex
	remoteCatalogs map[state.OwnerID]remoteCatalogCache

	// remoteRevs tracks the last-accepted snapshot revision per peer for the
	// peer's current connection. A new connection (re-registration) resets the
	// baseline so a restarted peer with a fresh revision counter is accepted;
	// within one connection, non-increasing revisions (stale or delayed
	// snapshots) are rejected to stop cache regression. Guarded by catalogMu.
	remoteRevs map[string]*remoteRevisionState

	// ownerBinding enforces one owner per peer and one peer per owner so a
	// peer cannot publish state under another peer's established owner.
	// Guarded by catalogMu. Owner is a v2 OwnerID (random base32), which is
	// distinct from the peer fingerprint.
	peerOwner map[string]state.OwnerID
	ownerPeer map[state.OwnerID]string

	// v2 reliable command waiters.
	cmdMu          sync.Mutex
	commandWaiters map[string]*commandWaiter

	// remoteStore persists remote catalog caches across restarts.
	remoteStore *state.Store

	// v2RemoteCreate runs local owner-side remote-create sagas and resumes
	// pending creates after restart.
	v2RemoteCreate *state.RemoteCreateCoordinator
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
// v2 snapshot streams. -1 means "no baseline yet" (fresh connection).
type remoteRevisionState struct {
	catalog   int64
	workspace map[state.LayoutID]int64
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

	m.remoteCatalogs = make(map[state.OwnerID]remoteCatalogCache)
	m.remoteRevs = make(map[string]*remoteRevisionState)
	m.peerOwner = make(map[string]state.OwnerID)
	m.ownerPeer = make(map[state.OwnerID]string)
	m.commandWaiters = make(map[string]*commandWaiter)

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

// GetAllSessions returns sessions from all hosts, with host fields stamped.
// The returned slice and session values are copies so callers cannot mutate
// internal manager state.
func (m *Manager) GetAllSessions() []*model.Session {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var all []*model.Session
	for _, h := range m.hosts {
		for _, s := range h.Sessions {
			copyS := copySession(s)
			copyS.Host = h.ID
			copyS.HostName = h.Name
			copyS.HostOnline = h.Connected
			all = append(all, copyS)
		}
	}
	return all
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

// GetLocalSessions returns only this node's sessions
func (m *Manager) GetLocalSessions() []*model.Session {
	return m.localMgr.GetSessions()
}

// GetHosts returns info about all known hosts. Session slices are copied so
// callers cannot mutate internal state.
func (m *Manager) GetHosts() []HostInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	hosts := make([]HostInfo, 0, len(m.hosts))
	for _, h := range m.hosts {
		sessions := make([]*model.Session, len(h.Sessions))
		for i, s := range h.Sessions {
			sessions[i] = copySession(s)
		}
		hosts = append(hosts, HostInfo{
			ID:       h.ID,
			Name:     h.Name,
			Version:  h.Version,
			Local:    h.ID == m.localID,
			Online:   h.Connected,
			Address:  h.Address,
			Sessions: sessions,
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

	m.resetRemoteRevisions(id)

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
		m.forgetRemoteCatalogsForPeer(id)
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

// validateCatalogOwnership checks that all sessions and layouts in the snapshot
// have their Owner field matching the snapshot's top-level owner. Returns false
// and logs a warning if any embedded owner differs.
func validateCatalogOwnership(peerID string, snap state.OwnerCatalogSnapshot) bool {
	for _, sess := range snap.Sessions {
		if sess.Owner != snap.Owner {
			logrus.WithFields(logrus.Fields{
				"peer": peerID,
				"owner": string(snap.Owner),
				"session_id": string(sess.ID),
				"session_owner": string(sess.Owner),
			}).Warn("dropping v2 snapshot: embedded session owner mismatch")
			return false
		}
	}
	for _, layout := range snap.Layouts {
		if layout.Owner != snap.Owner {
			logrus.WithFields(logrus.Fields{
				"peer": peerID,
				"owner": string(snap.Owner),
				"layout_id": string(layout.ID),
				"layout_owner": string(layout.Owner),
			}).Warn("dropping v2 snapshot: embedded layout owner mismatch")
			return false
		}
	}
	return true
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
					"peer": peerID,
					"owner": string(rec.Owner),
					"layout_id": string(rec.ID),
					"ref_owner": string(node.Ref.Owner),
				}).Warn("dropping v2 snapshot: embedded leaf owner mismatch")
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
	return checkNode(&rec.Tree)
}

// bindRemoteOwner enforces the ownership binding: one owner per peer and one
// peer per owner. The first snapshot a peer publishes establishes its sole
// owner; a later snapshot from the same peer under a different owner, or a
// second peer claiming an already-bound owner, is a spoof and is dropped.
// Caller must hold catalogMu. The v2 catalog Owner is a random base32 id, NOT
// the peer fingerprint, so an explicit cross-peer binding map is the sound
// integrity mechanism here.
func (m *Manager) bindRemoteOwner(peerID string, owner state.OwnerID) bool {
	if peerID == "" || owner == "" {
		return false
	}
	if p, ok := m.ownerPeer[owner]; ok && p != peerID {
		logrus.WithFields(logrus.Fields{"peer": peerID, "owner": string(owner), "boundPeer": p}).Warn("dropping v2 snapshot: owner already bound to another peer")
		return false
	}
	if o, ok := m.peerOwner[peerID]; ok && o != owner {
		logrus.WithFields(logrus.Fields{"peer": peerID, "owner": string(owner), "boundOwner": string(o)}).Warn("dropping v2 snapshot: peer switched owners")
		return false
	}
	m.peerOwner[peerID] = owner
	m.ownerPeer[owner] = peerID
	return true
}

// UpdateRemoteCatalog replaces the cached catalog for the snapshot's owner.
// The previous cache (if any) is overwritten, not merged. The owner must be
// the peer's bound owner and the revision must be increasing within the
// peer's current connection (stale/delayed snapshots are rejected). All
// embedded session and layout owners must match the snapshot's owner. If a
// remote store is wired, the complete set of accepted remote catalogs is
// persisted.
func (m *Manager) UpdateRemoteCatalog(peerID string, snap state.OwnerCatalogSnapshot) {
	if !validateCatalogOwnership(peerID, snap) {
		return
	}
	m.catalogMu.Lock()
	if !m.bindRemoteOwner(peerID, snap.Owner) {
		m.catalogMu.Unlock()
		return
	}
	rs := m.remoteRevs[peerID]
	if rs == nil {
		rs = &remoteRevisionState{catalog: -1}
		m.remoteRevs[peerID] = rs
	}
	if rs.catalog >= 0 && snap.Revision <= rs.catalog {
		m.catalogMu.Unlock()
		logrus.WithFields(logrus.Fields{"peer": peerID, "rev": snap.Revision, "last": rs.catalog}).Debug("dropping stale v2 catalog snapshot")
		return
	}
	rs.catalog = snap.Revision
	m.remoteCatalogs[snap.Owner] = remoteCatalogCache{
		Owner:      snap.Owner,
		PeerID:     peerID,
		Snapshot:   snap,
		ReceivedAt: time.Now(),
		CatalogRev: snap.Revision,
	}
	m.catalogMu.Unlock()
	m.persistRemoteCatalogs()
}

// UpdateRemoteWorkspace replaces the cached workspace for owner. The owner
// must be the peer's bound owner, and the workspace revision must be
// increasing within the current connection (keyed per layout id). All
// embedded leaf SessionRefs in the tree must have owner matching rec.Owner.
func (m *Manager) UpdateRemoteWorkspace(peerID string, rec state.WorkspaceRecord) {
	if !validateWorkspaceTreeOwnership(peerID, rec) {
		return
	}
	m.catalogMu.Lock()
	if !m.bindRemoteOwner(peerID, rec.Owner) {
		m.catalogMu.Unlock()
		return
	}
	rs := m.remoteRevs[peerID]
	if rs == nil {
		rs = &remoteRevisionState{catalog: -1}
		m.remoteRevs[peerID] = rs
	}
	if rs.workspace == nil {
		rs.workspace = make(map[state.LayoutID]int64)
	}
	if last, seen := rs.workspace[rec.ID]; seen && rec.Revision <= last {
		m.catalogMu.Unlock()
		logrus.WithFields(logrus.Fields{"peer": peerID, "layout": string(rec.ID), "rev": rec.Revision, "last": last}).Debug("dropping stale v2 workspace snapshot")
		return
	}
	rs.workspace[rec.ID] = rec.Revision
	cache := m.remoteCatalogs[rec.Owner]
	cache.Owner = rec.Owner
	cache.PeerID = peerID
	cache.Workspace = &rec
	cache.ReceivedAt = time.Now()
	cache.WorkspaceRev = rec.Revision
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
}

// SetRemoteCreateCoordinator wires the v2 owner-side remote create coordinator.
func (m *Manager) SetRemoteCreateCoordinator(c *state.RemoteCreateCoordinator) {
	m.v2RemoteCreate = c
}

// WorkspaceAuthority returns the owning owner for layout and, when the owner is
// remote, the peer ID that last advertised it. The empty peerID means this node
// is the authority.
func (m *Manager) WorkspaceAuthority(layout state.LayoutID) (state.OwnerID, string, error) {
	if cat := m.localMgr.V2Catalog(); cat != nil {
		if rec, ok := cat.Layout(layout); ok {
			return rec.Owner, "", nil
		}
	}
	m.catalogMu.RLock()
	defer m.catalogMu.RUnlock()
	for owner, cache := range m.remoteCatalogs {
		for _, l := range cache.Snapshot.Layouts {
			if l.ID == layout {
				return owner, cache.PeerID, nil
			}
		}
		if cache.Workspace != nil && cache.Workspace.ID == layout {
			return owner, cache.PeerID, nil
		}
	}
	return "", "", state.StateError{Code: state.ErrUnknownLayout, Field: "layout", Detail: fmt.Sprintf("workspace authority not found for layout %q", layout)}
}

// IsWorkspaceOwnerOnline reports whether owner is reachable for v2 commands.
func (m *Manager) IsWorkspaceOwnerOnline(owner state.OwnerID) bool {
	if owner == state.OwnerID(m.localID) {
		return true
	}
	peerID := m.peerIDForOwner(owner)
	if peerID == "" {
		return false
	}
	pc := m.GetPeerConnection(peerID)
	return pc != nil && pc.HasV2()
}

// ProxyWorkspaceCommand routes a workspace command to the layout's owner.
// Local authority applies directly; remote authority is sent over the v2
// command RPC. Legacy or offline owners return typed errors.
func (m *Manager) ProxyWorkspaceCommand(ctx context.Context, cmd state.WorkspaceCommand) error {
	owner, peerID, err := m.WorkspaceAuthority(cmd.Layout)
	if err != nil {
		return err
	}
	if owner == state.OwnerID(m.localID) {
		cat := m.localMgr.V2Catalog()
		if cat == nil {
			return fmt.Errorf("v2 catalog not enabled")
		}
		return cat.ApplyWorkspaceCommand(cmd)
	}
	pc := m.GetPeerConnection(peerID)
	if pc == nil {
		return state.StateError{Code: state.ErrWorkspaceOwnerOffline, Field: "owner", Detail: fmt.Sprintf("workspace owner %q is offline", owner)}
	}
	if !pc.HasV2() {
		return state.StateError{Code: state.ErrLegacyPeerUnsupported, Field: "peer_id", Detail: fmt.Sprintf("peer %q does not support v2 workspace commands", peerID)}
	}
	return m.SendWorkspaceCommand(ctx, peerID, cmd)
}

// RequestRemoteCreate routes a remote create to the workspace owner. The local
// owner path runs directly in the coordinator; remote owners are reached over
// the v2 remote-create RPC.
func (m *Manager) RequestRemoteCreate(ctx context.Context, owner state.OwnerID, req state.RemoteCreateRequest) (state.RemoteCreateResult, error) {
	if owner == "" || owner == state.OwnerID(m.localID) {
		if m.v2RemoteCreate == nil {
			return state.RemoteCreateResult{}, fmt.Errorf("remote create coordinator not available")
		}
		return m.v2RemoteCreate.ExecuteRemoteCreate(ctx, req)
	}
	peerID := m.peerIDForOwner(owner)
	if peerID == "" {
		return state.RemoteCreateResult{}, state.StateError{Code: state.ErrWorkspaceOwnerOffline, Field: "owner", Detail: fmt.Sprintf("workspace owner %q is offline", owner)}
	}
	pc := m.GetPeerConnection(peerID)
	if pc == nil {
		return state.RemoteCreateResult{}, state.StateError{Code: state.ErrWorkspaceOwnerOffline, Field: "owner", Detail: fmt.Sprintf("workspace owner %q is offline", owner)}
	}
	if !pc.HasV2() {
		return state.RemoteCreateResult{}, state.StateError{Code: state.ErrLegacyPeerUnsupported, Field: "peer_id", Detail: fmt.Sprintf("peer %q does not support v2 remote creates", peerID)}
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

// LoadRemoteCatalogCache loads persisted remote catalogs into memory. It is
// idempotent and ignores missing sidecar files.
func (m *Manager) LoadRemoteCatalogCache() error {
	if m.remoteStore == nil {
		return nil
	}
	catalogs, err := m.remoteStore.LoadRemoteCatalogs()
	if err != nil {
		return err
	}
	m.catalogMu.Lock()
	defer m.catalogMu.Unlock()
	for _, c := range catalogs {
		m.remoteCatalogs[c.Owner] = remoteCatalogCache{
			Owner:      c.Owner,
			Snapshot:   c,
			ReceivedAt: time.Now(),
			CatalogRev: c.Revision,
		}
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
	catalogs := make([]state.OwnerCatalogSnapshot, 0, len(m.remoteCatalogs))
	for _, c := range m.remoteCatalogs {
		catalogs = append(catalogs, cloneOwnerCatalogSnapshot(c.Snapshot))
	}
	m.catalogMu.RUnlock()

	sort.Slice(catalogs, func(i, j int) bool { return catalogs[i].Owner < catalogs[j].Owner })
	if err := store.SaveRemoteCatalogs(catalogs); err != nil {
		logrus.WithError(err).Warn("failed to persist remote catalog caches")
	}
}

func (m *Manager) forgetRemoteCatalogsForPeer(peerID string) {
	m.catalogMu.Lock()
	for owner, c := range m.remoteCatalogs {
		if c.PeerID == peerID {
			delete(m.remoteCatalogs, owner)
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
}

// resetRemoteRevisions clears the per-connection revision baseline for a
// peer. Called whenever a new connection registers.
func (m *Manager) resetRemoteRevisions(peerID string) {
	m.catalogMu.Lock()
	delete(m.remoteRevs, peerID)
	m.catalogMu.Unlock()
}

func cloneOwnerCatalogSnapshot(s state.OwnerCatalogSnapshot) state.OwnerCatalogSnapshot {
	sessions := make([]state.LocalSessionRecord, len(s.Sessions))
	copy(sessions, s.Sessions)
	layouts := make([]state.LayoutRecord, len(s.Layouts))
	copy(layouts, s.Layouts)
	return state.OwnerCatalogSnapshot{
		Owner:    s.Owner,
		Revision: s.Revision,
		Sessions: sessions,
		Layouts:  layouts,
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

// pruneOffline marks long-offline peers as offline. It deliberately does not
// delete v2 remote catalog caches; those are retained through reconnects and
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
