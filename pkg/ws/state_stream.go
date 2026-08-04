package ws

import (
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"

	"github.com/anh-chu/termyard/pkg/state"
)

var stateStreamUpgrader = websocket.Upgrader{
	CheckOrigin:     CheckSameOrigin,
	ReadBufferSize:  1024,
	WriteBufferSize: 1024 * 16,
}

// StateStreamMetrics accumulates process-wide counters for the v2 durable
// state stream. All fields are updated atomically and safe for concurrent
// reads.
type StateStreamMetrics struct {
	ConnectedClients   int64
	CoalescedSnapshots int64
	WriteFailures      int64
	EncodedBytes       int64
}

type encodedSnapshot struct {
	revision int64
	bytes    []byte
}

// stateStreamClient holds at most one pending catalog snapshot and one
// pending workspace snapshot, coalesced by revision, and drains them from a
// single writer goroutine. A later publish with a lower-or-equal revision is
// dropped, so a slow client never accumulates backlog and can never block the
// publisher or any other client.
type stateStreamClient struct {
	conn *websocket.Conn

	mu        sync.Mutex
	catalog   *encodedSnapshot
	workspace *encodedSnapshot

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
		conn:    conn,
		signal:  make(chan struct{}, 1),
		done:    make(chan struct{}),
		metrics: metrics,
	}
	go c.writeLoop()
	return c
}

func (c *stateStreamClient) publishCatalog(rev int64, encoded []byte) {
	c.enqueue(&c.catalog, rev, encoded)
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
			cat := c.catalog
			c.catalog = nil
			wsSnap := c.workspace
			c.workspace = nil
			c.mu.Unlock()

			if cat != nil && !c.write(cat.bytes) {
				return
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

type catalogSnapshotMessage struct {
	Type     string                     `json:"type"`
	Snapshot state.OwnerCatalogSnapshot `json:"snapshot"`
}

type workspaceSnapshotMessage struct {
	Type      string                `json:"type"`
	Workspace state.WorkspaceRecord `json:"workspace"`
}

// StateStreamHub fans out complete catalog/workspace snapshots to durable v2
// browser state connections at /ws/v2/state. Every revision is encoded once
// regardless of how many clients are connected; each client keeps only the
// latest catalog and latest workspace snapshot pending, so one slow browser
// cannot block the publisher (the PTY path) or any other client.
//
// Ephemeral tool/activity events are intentionally NOT sent on this stream;
// they remain on the existing best-effort /ws/events hub.
type StateStreamHub struct {
	catalog *state.Catalog
	// primaryLayout resolves the layout whose workspace is streamed. If nil
	// or it returns "", the first known layout is used.
	primaryLayout func() state.LayoutID

	mu      sync.RWMutex
	clients map[*stateStreamClient]struct{}

	unsubscribeCatalog   func()
	unsubscribeWorkspace func()

	Metrics StateStreamMetrics
}

// NewStateStreamHub creates a hub bound to one catalog and subscribes to its
// catalog/workspace publication streams immediately.
func NewStateStreamHub(catalog *state.Catalog, primaryLayout func() state.LayoutID) *StateStreamHub {
	h := &StateStreamHub{
		catalog:       catalog,
		primaryLayout: primaryLayout,
		clients:       make(map[*stateStreamClient]struct{}),
	}
	h.unsubscribeCatalog = catalog.SubscribeCatalog(h.onCatalog)
	h.unsubscribeWorkspace = catalog.SubscribeWorkspace(h.onWorkspace)
	return h
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
	encoded, err := json.Marshal(catalogSnapshotMessage{Type: "catalog_snapshot", Snapshot: snap})
	if err != nil {
		logrus.WithError(err).Warn("v2 state stream: failed to encode catalog snapshot")
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		c.publishCatalog(snap.Revision, encoded)
	}
}

func (h *StateStreamHub) onWorkspace(layout state.LayoutID, rec state.WorkspaceRecord) {
	if want := h.currentLayout(); want != "" && want != layout {
		return
	}
	encoded, err := json.Marshal(workspaceSnapshotMessage{Type: "workspace_snapshot", Workspace: rec})
	if err != nil {
		logrus.WithError(err).Warn("v2 state stream: failed to encode workspace snapshot")
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		c.publishWorkspace(rec.Revision, encoded)
	}
}

func (h *StateStreamHub) currentLayout() state.LayoutID {
	if h.primaryLayout != nil {
		if id := h.primaryLayout(); id != "" {
			return id
		}
	}
	layouts := h.catalog.Layouts()
	if len(layouts) == 0 {
		return ""
	}
	return layouts[0].ID
}

// HandleState upgrades the connection and streams durable v2 state. On
// connect it enqueues the complete current catalog and (if any layout
// exists) workspace snapshot before any incremental publication is applied,
// so a client never observes a partial view. Disconnecting a client never
// clears cached projections for other clients or the hub itself; a
// reconnecting client is always caught up from a fresh, complete read of the
// catalog.
func (h *StateStreamHub) HandleState(w http.ResponseWriter, r *http.Request) {
	conn, err := stateStreamUpgrader.Upgrade(w, r, nil)
	if err != nil {
		logrus.WithError(err).Warn("v2 state ws upgrade failed")
		return
	}

	c := newStateStreamClient(conn, &h.Metrics)
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	atomic.AddInt64(&h.Metrics.ConnectedClients, 1)

	catSnap := h.catalog.AggregateCatalogSnapshot()
	if encoded, err := json.Marshal(catalogSnapshotMessage{Type: "catalog_snapshot", Snapshot: catSnap}); err == nil {
		c.publishCatalog(catSnap.Revision, encoded)
	}
	if layoutID := h.currentLayout(); layoutID != "" {
		if wsRes, err := h.catalog.WorkspaceSnapshot(layoutID); err == nil {
			if encoded, err := json.Marshal(workspaceSnapshotMessage{Type: "workspace_snapshot", Workspace: wsRes.Record}); err == nil {
				c.publishWorkspace(wsRes.Record.Revision, encoded)
			}
		}
	}

	logrus.Debug("v2 state stream client connected")

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
	logrus.Debug("v2 state stream client disconnected")
}
