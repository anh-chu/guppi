package ws

import (
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"

	"github.com/anh-chu/termyard/pkg/state"
)

// localCatalogSlotKey is the fixed slot key for this node's own catalog.
const localCatalogSlotKey = "local"

// remoteCatalogSlotKey returns the per-remote-owner slot key so each peer's
// catalog coalesces independently of every other owner's.
func remoteCatalogSlotKey(owner state.OwnerID) string {
	return "remote:" + string(owner)
}

// RemoteCatalogNotifier is implemented by pkg/peer.Manager. It supplies the
// cached remote-owner catalogs for a client's initial connect burst and
// notifies on every subsequent update or explicit removal (peer forgotten /
// disconnected). StateStreamHub only reads from it; it performs no
// validation or storage of its own.
type RemoteCatalogNotifier interface {
	state.RemoteCatalogSource
	SubscribeRemoteCatalogs(fn func(owner state.OwnerID, snap state.OwnerCatalogSnapshot, removed bool)) func()
}

var stateStreamUpgrader = websocket.Upgrader{
	CheckOrigin:     CheckSameOrigin,
	ReadBufferSize:  1024,
	WriteBufferSize: 1024 * 16,
}

// StateStreamMetrics accumulates process-wide counters for the durable
// state stream. All fields are updated atomically and safe for concurrent
// reads.
type StateStreamMetrics struct {
	ConnectedClients   int64
	CoalescedSnapshots int64
	WriteFailures      int64
	EncodedBytes       int64
}

type encodedSnapshot struct {
	// revision is the owner's own authoritative catalog revision for a
	// catalog slot (never a hub-assigned sequence number -- see
	// publishCatalogKeyed for why that distinction matters). For a
	// removed/tombstoned slot, revision retains the highest revision ever
	// observed for that key, so a later stale snapshot arriving out of order
	// with a lower-or-equal revision is still correctly rejected instead of
	// silently overwriting the tombstone.
	revision int64
	// removed marks this slot as a tombstone: the owner was explicitly
	// removed (e.g. a remote peer disconnected and its cache was forgotten).
	// A tombstone is never dropped in favor of a stale lower-revision
	// snapshot, and is itself superseded only by a genuinely newer snapshot
	// (incoming revision strictly greater than the retained revision).
	removed bool
	bytes   []byte
}

// stateStreamClient holds at most one pending snapshot per catalog slot
// (the local owner plus one slot per remote owner it has seen) and one
// pending workspace snapshot, each coalesced independently, and drains them
// from a single writer goroutine. A slot's coalescing/staleness comparison
// is keyed on the OWNER'S OWN authoritative catalog revision (see
// publishCatalogKeyed), never on publish order or a hub-assigned sequence
// number: an earlier-captured-but-later-enqueued snapshot (e.g. an initial
// connect snapshot read before a concurrent live update, but enqueued after
// it) must not be allowed to overwrite a slot that already holds a newer
// revision. This is what keeps a slow client from accumulating backlog
// without ever discarding genuinely newer state in favor of stale state.
type stateStreamClient struct {
	conn *websocket.Conn

	mu           sync.Mutex
	catalogSlots map[string]*encodedSnapshot
	workspace    *encodedSnapshot

	signal chan struct{}
	// done is closed exactly once by close(). It is only ever closed, never
	// sent on, so closing it concurrently with anything is always safe.
	// writeLoop and wake select on it to observe shutdown; signal is NEVER
	// closed, so no goroutine can send on a channel after it has been closed.
	done    chan struct{}
	closed  atomic.Bool
	closeMu sync.Mutex

	metrics *StateStreamMetrics

	// testWaitBeforeDrain blocks writeLoop before draining slots, allowing
	// tests to accumulate multiple publishes before any flush. Nil in production.
	// Stored as an atomic pointer because it is set (by tests) after writeLoop's
	// goroutine has already started, and read concurrently from writeLoop.
	testWaitBeforeDrain atomic.Pointer[chan struct{}]
}

func newStateStreamClient(conn *websocket.Conn, metrics *StateStreamMetrics) *stateStreamClient {
	c := &stateStreamClient{
		conn:         conn,
		catalogSlots: make(map[string]*encodedSnapshot),
		signal:       make(chan struct{}, 1),
		done:         make(chan struct{}),
		metrics:      metrics,
	}
	go c.writeLoop()
	return c
}

// publishCatalogKeyed enqueues a catalog-related message (a snapshot for key,
// or an owner-removal/tombstone message) into the per-key slot. revision is
// the OWNER'S OWN authoritative catalog revision (state.OwnerCatalogSnapshot.
// Revision) for a snapshot -- NOT a hub-assigned publish sequence number.
// Comparing real per-owner revisions (instead of hub-wide sequence/arrival
// order) is what makes coalescing safe when a snapshot is captured early but
// enqueued late (e.g. an initial-connect read racing a concurrent live
// update): a lower revision can never clobber an already-stored higher one,
// regardless of which call reaches the slot first.
//
// removed=true marks a tombstone (owner explicitly removed). A tombstone is
// always applied -- it is never rejected merely because its carried revision
// looks "low" -- but it retains the highest revision already known for this
// key, so a stale snapshot delivered out of order AFTER the tombstone (with
// revision <= that retained value) is still correctly rejected instead of
// silently overwriting the removal. A tombstone is only superseded by a
// later snapshot whose revision is strictly greater than the retained value
// (e.g. the owner reappeared with genuinely newer state).
func (c *stateStreamClient) publishCatalogKeyed(key string, revision int64, removed bool, encoded []byte) {
	if c.closed.Load() {
		return
	}
	c.mu.Lock()
	cur := c.catalogSlots[key]
	if cur != nil && !removed && revision <= cur.revision {
		c.mu.Unlock()
		if c.metrics != nil {
			atomic.AddInt64(&c.metrics.CoalescedSnapshots, 1)
		}
		return
	}
	if cur != nil && c.metrics != nil {
		atomic.AddInt64(&c.metrics.CoalescedSnapshots, 1)
	}
	retained := revision
	if cur != nil && cur.revision > retained {
		retained = cur.revision
	}
	c.catalogSlots[key] = &encodedSnapshot{revision: retained, removed: removed, bytes: encoded}
	c.mu.Unlock()
	c.wake()
}

func (c *stateStreamClient) publishWorkspace(rev int64, encoded []byte) {
	c.enqueue(&c.workspace, rev, encoded)
}

func (c *stateStreamClient) enqueue(slot **encodedSnapshot, rev int64, encoded []byte) {
	if c.closed.Load() {
		return
	}
	c.mu.Lock()
	cur := *slot
	if cur != nil && rev <= cur.revision {
		c.mu.Unlock()
		if c.metrics != nil {
			atomic.AddInt64(&c.metrics.CoalescedSnapshots, 1)
		}
		return
	}
	if cur != nil && c.metrics != nil {
		atomic.AddInt64(&c.metrics.CoalescedSnapshots, 1)
	}
	*slot = &encodedSnapshot{revision: rev, bytes: encoded}
	c.mu.Unlock()
	c.wake()
}

func (c *stateStreamClient) wake() {
	select {
	case c.signal <- struct{}{}:
	case <-c.done:
	default:
	}
}

func (c *stateStreamClient) writeLoop() {
	for {
		select {
		case <-c.done:
			return
		case <-c.signal:
			if c.closed.Load() {
				return
			}
			// If a test gate is set, wait for it before draining. This allows
			// tests to accumulate multiple publishes before any flush occurs.
			if gate := c.testWaitBeforeDrain.Load(); gate != nil {
				select {
				case <-*gate:
				case <-c.done:
					return
				}
			}
			c.mu.Lock()
			pending := c.catalogSlots
			c.catalogSlots = make(map[string]*encodedSnapshot, len(pending))
			wsSnap := c.workspace
			c.workspace = nil
			c.mu.Unlock()

			// Drain catalog slots in a deterministic (sorted-key) order so the
			// local snapshot -- key "local" sorts before any "remote:..." key --
			// is always written before remote-owner snapshots in the same burst.
			keys := make([]string, 0, len(pending))
			for k := range pending {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if !c.write(pending[k].bytes) {
					return
				}
			}
			if wsSnap != nil && !c.write(wsSnap.bytes) {
				return
			}
		}
	}
}

func (c *stateStreamClient) write(payload []byte) bool {
	err := c.conn.WriteMessage(websocket.TextMessage, payload)
	if err != nil {
		if c.metrics != nil {
			atomic.AddInt64(&c.metrics.WriteFailures, 1)
		}
		c.close()
		return false
	}
	if c.metrics != nil {
		atomic.AddInt64(&c.metrics.EncodedBytes, int64(len(payload)))
	}
	return true
}

// close stops the writer goroutine. Safe to call multiple times; done is
// closed exactly once, guarded by closeMu plus the CAS on closed.
func (c *stateStreamClient) close() {
	c.closeMu.Lock()
	defer c.closeMu.Unlock()
	if c.closed.CompareAndSwap(false, true) {
		close(c.done)
	}
}

// catalogSnapshotMessage carries one owner's complete catalog. IsLocal tells
// the browser whether Snapshot.Owner is this node's own owner (true) or a
// cached remote peer's owner (false) -- carried explicitly on the envelope
// rather than inferred from message order, since a client's own local
// snapshot and any remote snapshots are otherwise indistinguishable on the
// wire.
type catalogSnapshotMessage struct {
	Type     string                     `json:"type"`
	Snapshot state.OwnerCatalogSnapshot `json:"snapshot"`
	IsLocal  bool                       `json:"is_local"`
}

// catalogOwnerRemovedMessage is an explicit removal signal for a remote
// owner's catalog -- e.g. the peer disconnected and its cache was forgotten.
// This is never sent for the local owner (this node's own catalog is never
// "removed"). A missing owner in a later bootstrap read is an equivalent
// signal for a fresh page load; this message exists so a LIVE connection
// does not have to distinguish "peer went offline" from ordinary silence.
type catalogOwnerRemovedMessage struct {
	Type  string        `json:"type"`
	Owner state.OwnerID `json:"owner"`
}

type workspaceSnapshotMessage struct {
	Type      string                `json:"type"`
	Workspace state.WorkspaceRecord `json:"workspace"`
}

// StateStreamHub fans out complete catalog/workspace snapshots to durable
// browser state connections at /ws/state. Every revision is encoded once
// regardless of how many clients are connected; each client keeps only the
// latest catalog and latest workspace snapshot pending, so one slow browser
// cannot block the publisher (the PTY path) or any other client.
//
// Ephemeral tool/activity events are intentionally NOT sent on this stream;
// they remain on the existing best-effort /ws/events hub.
type StateStreamHub struct {
	catalog *state.Catalog

	// remoteSource supplies cached remote-owner catalogs, if this node has a
	// peer manager wired up (see AttachRemoteCatalogSource). Nil on a
	// single-node deployment, in which case only the local catalog streams --
	// unchanged from pre-multi-node behavior.
	remoteSource RemoteCatalogNotifier

	// mu guards clients AND, critically, is held across the entire
	// "register client + capture and enqueue its initial snapshot" sequence
	// in HandleState. Every live-publish fan-out (onCatalog/onRemoteCatalog/
	// onWorkspace) takes mu.RLock while iterating clients. Serializing
	// registration against those RLocks as one atomic write-locked operation
	// is what prevents a live update from being published to a client
	// between "client added to clients" and "initial snapshot enqueued",
	// which would otherwise risk a stale initial snapshot silently
	// overwriting a newer live one already sitting in the client's slot.
	mu      sync.RWMutex
	clients map[*stateStreamClient]struct{}

	unsubscribeCatalog   func()
	unsubscribeWorkspace func()
	unsubscribeRemote    func()

	Metrics StateStreamMetrics
}

// NewStateStreamHub creates a hub bound to one catalog and subscribes to its
// catalog/workspace publication streams immediately. Call
// AttachRemoteCatalogSource separately to also stream cached remote-owner
// catalogs (multi-node); a hub with no remote source behaves exactly as a
// single-node hub always has.
func NewStateStreamHub(catalog *state.Catalog) *StateStreamHub {
	h := &StateStreamHub{
		catalog: catalog,
		clients: make(map[*stateStreamClient]struct{}),
	}
	h.unsubscribeCatalog = catalog.SubscribeCatalog(h.onCatalog)
	h.unsubscribeWorkspace = catalog.SubscribeWorkspace(h.onWorkspace)
	return h
}

// AttachRemoteCatalogSource wires source (typically a *peer.Manager) so
// every connected and future client also receives every cached remote-owner
// catalog, kept live via source's own update/removal notifications. Safe to
// call at most once; a nil source is a no-op.
func (h *StateStreamHub) AttachRemoteCatalogSource(source RemoteCatalogNotifier) {
	if source == nil {
		return
	}
	h.mu.Lock()
	h.remoteSource = source
	h.mu.Unlock()
	h.unsubscribeRemote = source.SubscribeRemoteCatalogs(h.onRemoteCatalog)
}

// Close unsubscribes from the catalog. Connected clients are left to
// disconnect naturally when their underlying connection closes.
func (h *StateStreamHub) Close() {
	if h.unsubscribeCatalog != nil {
		h.unsubscribeCatalog()
	}
	if h.unsubscribeWorkspace != nil {
		h.unsubscribeWorkspace()
	}
	if h.unsubscribeRemote != nil {
		h.unsubscribeRemote()
	}
}

// setTestWaitBeforeDrain sets a gate channel on all connected clients to block
// writeLoop before draining slots. Used by tests to ensure multiple publishes
// accumulate before any flush. Gate must be closed or send len(clients) signals
// to unblock all pending drains.
func (h *StateStreamHub) setTestWaitBeforeDrain(gate chan struct{}) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		c.testWaitBeforeDrain.Store(&gate)
	}
}

func (h *StateStreamHub) onCatalog(snap state.OwnerCatalogSnapshot) {
	encoded, err := json.Marshal(catalogSnapshotMessage{Type: "catalog_snapshot", Snapshot: snap, IsLocal: true})
	if err != nil {
		logrus.WithError(err).Warn("state stream: failed to encode catalog snapshot")
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		c.publishCatalogKeyed(localCatalogSlotKey, snap.Revision, false, encoded)
	}
}

// onRemoteCatalog fans out an update to (removed=false) or removal of
// (removed=true) one remote owner's cached catalog to every connected
// client. It is registered with remoteSource via AttachRemoteCatalogSource
// and never touches the local catalog slot.
func (h *StateStreamHub) onRemoteCatalog(owner state.OwnerID, snap state.OwnerCatalogSnapshot, removed bool) {
	var encoded []byte
	var err error
	if removed {
		encoded, err = json.Marshal(catalogOwnerRemovedMessage{Type: "catalog_owner_removed", Owner: owner})
	} else {
		encoded, err = json.Marshal(catalogSnapshotMessage{Type: "catalog_snapshot", Snapshot: snap, IsLocal: false})
	}
	if err != nil {
		logrus.WithError(err).Warn("state stream: failed to encode remote catalog message")
		return
	}
	key := remoteCatalogSlotKey(owner)
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		c.publishCatalogKeyed(key, snap.Revision, removed, encoded)
	}
}

func (h *StateStreamHub) onWorkspace(rec state.WorkspaceRecord) {
	encoded, err := json.Marshal(workspaceSnapshotMessage{Type: "workspace_snapshot", Workspace: rec})
	if err != nil {
		logrus.WithError(err).Warn("state stream: failed to encode workspace snapshot")
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		c.publishWorkspace(rec.Revision, encoded)
	}
}

// HandleState upgrades the connection and streams durable state. On
// connect it enqueues the complete current catalog and (if any layout
// exists) workspace snapshot before any incremental publication is applied,
// so a client never observes a partial view. Disconnecting a client never
// clears cached projections for other clients or the hub itself; a
// reconnecting client is always caught up from a fresh, complete read of the
// catalog.
func (h *StateStreamHub) HandleState(w http.ResponseWriter, r *http.Request) {
	conn, err := stateStreamUpgrader.Upgrade(w, r, nil)
	if err != nil {
		logrus.WithError(err).Warn("state ws upgrade failed")
		return
	}

	c := newStateStreamClient(conn, &h.Metrics)

	// Register the client AND capture+enqueue its initial snapshots as one
	// atomic operation under h.mu (write-locked). onCatalog/onRemoteCatalog/
	// onWorkspace all take h.mu.RLock while fanning out a live publish to
	// h.clients, so holding the write lock across this entire block
	// guarantees no live update can be published to this client between it
	// being added to h.clients and its initial snapshot being enqueued --
	// closing the race where an earlier-read-but-later-enqueued initial
	// snapshot could otherwise overwrite a newer live one already sitting in
	// the client's coalescing slot.
	h.mu.Lock()
	h.clients[c] = struct{}{}
	catSnap := h.catalog.AggregateCatalogSnapshot()
	if encoded, err := json.Marshal(catalogSnapshotMessage{Type: "catalog_snapshot", Snapshot: catSnap, IsLocal: true}); err == nil {
		c.publishCatalogKeyed(localCatalogSlotKey, catSnap.Revision, false, encoded)
	}
	if h.remoteSource != nil {
		for _, rsnap := range h.remoteSource.AllRemoteCatalogSnapshots() {
			if encoded, err := json.Marshal(catalogSnapshotMessage{Type: "catalog_snapshot", Snapshot: rsnap, IsLocal: false}); err == nil {
				c.publishCatalogKeyed(remoteCatalogSlotKey(rsnap.Owner), rsnap.Revision, false, encoded)
			}
		}
	}
	if wsRes, err := h.catalog.WorkspaceSnapshot(); err == nil {
		if encoded, err := json.Marshal(workspaceSnapshotMessage{Type: "workspace_snapshot", Workspace: wsRes.Record}); err == nil {
			c.publishWorkspace(wsRes.Record.Revision, encoded)
		}
	}
	h.mu.Unlock()
	atomic.AddInt64(&h.Metrics.ConnectedClients, 1)

	logrus.Debug("state stream client connected")

	// Keep the connection alive by reading (and discarding) client frames;
	// this stream is server-to-browser only.
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
	}

	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
	atomic.AddInt64(&h.Metrics.ConnectedClients, -1)
	c.close()
	conn.Close()
	logrus.Debug("state stream client disconnected")
}
