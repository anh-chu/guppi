package peer

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"

	"github.com/anh-chu/termyard/pkg/activity"
	"github.com/anh-chu/termyard/pkg/identity"
	"github.com/anh-chu/termyard/pkg/pty"
	"github.com/anh-chu/termyard/pkg/sessionlaunch"
	"github.com/anh-chu/termyard/pkg/state"
	"github.com/anh-chu/termyard/pkg/stats"
	"github.com/anh-chu/termyard/pkg/toolevents"
)

// Role tells the session which side it is. Affects only the initial
// peer-state push.
type Role int

const (
	RoleDialer Role = iota
	RoleListener
)

// SessionAttrsSink is the slice of pkg/sessionattrs the session loop needs in
// order to merge shared session-attribute updates received from paired peers.
// Kept narrow so pkg/peer doesn't pull pkg/sessionattrs directly.
type SessionAttrsSink interface {
	// ApplyRemoteDelta merges a single-key delta via per-key LWW. accepted=false
	// means the local copy was newer-or-equal and the delta was dropped.
	ApplyRemoteDelta(key string, background, hidden bool, scheduleID string, updatedAt time.Time) (accepted bool, err error)
	// ApplyRemoteSnapshot merges a full peer snapshot via per-key LWW, returning
	// the keys that changed locally.
	ApplyRemoteSnapshot(attrs map[string]SessionAttr) (changed []string, err error)
	// SnapshotAttrs returns the full local attribute map to seed a fresh peer.
	SnapshotAttrs() map[string]SessionAttr
	// SetScheduleID records the owning schedule for a session this node spawned
	// locally on behalf of a remote peer's scheduler, keyed by the local session
	// key, so the run groups in this node's own UI.
	SetScheduleID(key, scheduleID string) error
}

// SessionOrderSink is the narrow slice of session-order storage the peer loop
// needs.
type SessionOrderSink interface {
	ApplyRemoteDelta(key, rank string, updatedAt time.Time) (accepted bool, err error)
	ApplyRemoteSnapshot(orders map[string]SessionOrder) (changed []string, err error)
	SnapshotOrders() map[string]SessionOrder
}

// GroupSink is the narrow slice of group storage the peer loop needs.
type GroupSink interface {
	ApplyRemoteDelta(id string, group Group) (accepted bool, err error)
	ApplyRemoteSnapshot(groups map[string]Group) (changed []string, err error)
	SnapshotGroups() map[string]Group
}

// BrowserBroadcaster pushes a JSON message to every connected browser. Used
// to forward session-attrs-updated events to the local UI after we accept a
// remote update from a paired peer.
type BrowserBroadcaster interface {
	BroadcastJSON(v interface{})
}

// DaemonRegistry is the interface for daemon session operations.
type DaemonRegistry interface {
	Create(name, shell, cwd string, cols, rows uint16) error
	Kill(name string) error
	Capture(name string) (string, error)
	SocketPath(name string) string
	List() []pty.SessionInfo
}

// SessionDeps groups the runtime dependencies needed by a peer session.
type SessionDeps struct {
	Manager     *Manager
	LocalMgr    *state.Manager
	Identity    *identity.Identity
	ActTracker  *activity.Tracker
	ToolTracker *toolevents.Tracker
	PeerStore   *identity.PeerStore
	DaemonReg   DaemonRegistry
	StreamReg   *StreamRegistry
	CaptureReg  *CaptureRegistry
	FileReadReg *FileReadRegistry
	AttrsSink   SessionAttrsSink
	Launch      *sessionlaunch.Service
	OrderSink   SessionOrderSink
	GroupSink   GroupSink
	BrowserHub  BrowserBroadcaster
}

// connWriter serializes WebSocket writes from multiple goroutines.
type connWriter struct {
	mu   sync.Mutex
	conn *websocket.Conn
}

// writeFrame writes one pre-serialized frame. Bound the write so a stuck/
// half-open peer socket can't block the writer goroutine indefinitely and
// silently back up the send lanes.
func (w *connWriter) writeFrame(f wireFrame) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	err := w.conn.WriteMessage(websocket.TextMessage, f.data)
	_ = w.conn.SetWriteDeadline(time.Time{})
	return err
}

func (w *connWriter) writeControl(messageType int, data []byte, deadline time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.conn.WriteControl(messageType, data, deadline)
}

// runSession owns the post-auth lifetime of one peer connection. It blocks
// until conn is closed or ctx is canceled. Same code runs on both ends.
func runSession(
	ctx context.Context,
	role Role,
	conn *websocket.Conn,
	peerInfo identity.Peer,
	address string,
	caps []string,
	deps SessionDeps,
) error {
	peerID := peerInfo.Fingerprint()
	log := logrus.WithFields(logrus.Fields{"peer": peerInfo.Name, "id": peerID})

	cw := &connWriter{conn: conn}

	pc := NewPeerConnection(peerID, 64)
	pc.Role = role
	pc.Caps = append([]string(nil), caps...)
	if !deps.Manager.TryRegisterPeer(peerID, peerInfo.Name, peerInfo.PublicKey, address, pc) {
		return fmt.Errorf("peer already connected")
	}

	sessionCtx, cancel := context.WithCancel(ctx)

	// Teardown order is crucial:
	//   1. cancel session ctx — stops background producers
	//   2. close websocket — unblocks read loop on the other goroutine, drains pings
	//   3. unregister from manager — stops new HTTP-side producers from finding pc
	//   4. close pc — ends writer loop
	//   5. wait for writer to drain
	writerDone := make(chan struct{})
	defer func() {
		cancel()
		_ = conn.Close()
		deps.Manager.UnregisterPeer(peerID)
		pc.Close()
		<-writerDone
	}()

	// If parent ctx is canceled while we're in ReadJSON, close conn to unblock.
	go func() {
		select {
		case <-sessionCtx.Done():
			_ = conn.Close()
		}
	}()

	// Liveness: ping/pong with 15s ping, 30s read deadline.
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		return nil
	})
	conn.SetPingHandler(func(data string) error {
		conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		return cw.writeControl(websocket.PongMessage, []byte(data), time.Now().Add(5*time.Second))
	})
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))

	// Writer goroutine drains the two priority lanes to conn, favouring the
	// hi (interactive PTY) lane over the lo (bulky control-plane) lane so a fat
	// state snapshot never delays a keystroke echo. The hi drain is capped per
	// cycle: an unbounded burst would starve lo entirely under continuous PTY
	// output, stranding lo-lane traffic (state sync, etc.). After the burst we
	// fall to a fair select where lo gets an equal chance. Any write failure
	// cancels the session so the read loop unblocks.
	go func() {
		defer close(writerDone)
		hi, lo := pc.HiLane(), pc.LoLane()
		const maxHiBurst = 64
		for {
			// Fast path: drain hi, but at most maxHiBurst frames before yielding.
			burst := 0
			for burst < maxHiBurst {
				select {
				case f := <-hi:
					if err := cw.writeFrame(f); err != nil {
						log.WithError(err).Debug("session write failed")
						cancel()
						return
					}
					burst++
					continue
				default:
				}
				break
			}
			// Block on hi, lo, or teardown. select picks a ready case at random,
			// so lo can't be starved even when hi is continuously ready.
			select {
			case f := <-hi:
				if err := cw.writeFrame(f); err != nil {
					log.WithError(err).Debug("session write failed")
					cancel()
					return
				}
			case f := <-lo:
				if err := cw.writeFrame(f); err != nil {
					log.WithError(err).Debug("session write failed")
					cancel()
					return
				}
			case <-pc.Done():
				return
			}
		}
	}()

	// Initial pushes — both sides advertise themselves.
	sendStateUpdate(pc, deps)
	sendInitialPeerState(pc, deps, peerID)
	sendInitialSessionAttrs(pc, deps)
	sendInitialSessionOrder(pc, deps)
	sendInitialGroups(pc, deps)

	// Background loops.
	go pingLoop(sessionCtx, cw)
	go periodicActivity(sessionCtx, pc, deps)
	go periodicStats(sessionCtx, pc, deps)
	go forwardStateEvents(sessionCtx, pc, deps)
	go forwardToolEvents(sessionCtx, pc, deps, peerID)
	go forwardPeerStateChanges(sessionCtx, pc, deps, peerID)

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.WithError(err).Debug("session read error")
			}
			return err
		}
		var msg Message
		if err := json.Unmarshal(raw, &msg); err != nil {
			log.WithError(err).Debug("session read error")
			continue
		}
		if msg.Type == MsgForget {
			log.Info("peer sent forget — removing")
			if err := deps.PeerStore.RemoveByPublicKey(peerInfo.PublicKey); err != nil {
				log.WithError(err).Debug("forget remove failed")
			}
			deps.Manager.RemoveHost(peerID)
			return fmt.Errorf("peer forgot us")
		}
		handleSessionMessage(peerID, &msg, pc, deps, log)
	}
}

func pingLoop(ctx context.Context, cw *connWriter) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := cw.writeControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
				return
			}
		}
	}
}

func periodicActivity(ctx context.Context, pc *PeerConnection, deps SessionDeps) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	localID := deps.Manager.LocalID()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			snapshots := deps.ActTracker.GetAll()
			for _, s := range snapshots {
				if s.Host == "" {
					s.Host = localID
				}
			}
			msg, err := NewMessage(MsgActivityUpdate, ActivityUpdatePayload{Snapshots: snapshots})
			if err != nil {
				continue
			}
			pc.Enqueue(msg)
		}
	}
}

func collectStats(deps SessionDeps) map[string]interface{} {
	s := stats.SystemStats()
	sessions := deps.LocalMgr.GetSessions()
	s["processes"] = stats.ProcessCountsFromSessions(sessions)
	return s
}

func periodicStats(ctx context.Context, pc *PeerConnection, deps SessionDeps) {
	// Send immediately.
	if msg, err := NewMessage(MsgStats, StatsPayload{Stats: collectStats(deps)}); err == nil {
		pc.Enqueue(msg)
	}
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if msg, err := NewMessage(MsgStats, StatsPayload{Stats: collectStats(deps)}); err == nil {
				pc.Enqueue(msg)
			}
		}
	}
}

func forwardStateEvents(ctx context.Context, pc *PeerConnection, deps SessionDeps) {
	ch := deps.LocalMgr.Subscribe()
	defer deps.LocalMgr.Unsubscribe(ch)
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			msg, err := NewMessage(MsgStateEvent, StateEventPayload{
				EventType: evt.Type,
				Session:   evt.Session,
				Data:      evt.Data,
			})
			if err != nil {
				continue
			}
			pc.Enqueue(msg)
			sendStateUpdate(pc, deps)
		}
	}
}

func forwardToolEvents(ctx context.Context, pc *PeerConnection, deps SessionDeps, remotePeerID string) {
	ch := deps.ToolTracker.Subscribe()
	defer deps.ToolTracker.Unsubscribe(ch)
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			// Don't echo the peer's own events back — this would create a
			// ping-pong loop: peer A's event arrives, gets stamped Host=A,
			// records locally, broadcasts to subscribers, and would forward
			// straight back to A, which re-stamps and re-forwards forever.
			if evt.Host == remotePeerID {
				continue
			}
			// Only forward our own local-origin events; we don't transitively
			// relay other peers' events.
			if evt.Host != "" && evt.Host != deps.Manager.LocalID() {
				continue
			}
			msg, err := NewMessage(MsgToolEvent, ToolEventPayload{Event: evt})
			if err != nil {
				continue
			}
			pc.Enqueue(msg)
		}
	}
}

// forwardPeerStateChanges pushes a peer-state snapshot whenever local state
// changes, so the remote sees our updated session list / activity / stats.
func forwardPeerStateChanges(ctx context.Context, pc *PeerConnection, deps SessionDeps, remotePeerID string) {
	ch := deps.Manager.Subscribe()
	defer deps.Manager.Unsubscribe(ch)
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			// Don't echo the peer's own events back.
			if evt.Host == remotePeerID {
				continue
			}
			// Only push peer-state when our local host changed; we don't
			// transitively expose other peers.
			if evt.Host != "" && evt.Host != deps.Manager.LocalID() {
				continue
			}
			msg, err := NewMessage(MsgPeerState, PeerStatePayload{
				Hosts: deps.Manager.GetHostsForPeer(remotePeerID),
			})
			if err != nil {
				continue
			}
			pc.Enqueue(msg)
		}
	}
}

// handleSessionMessage dispatches messages received from the remote peer.
func handleSessionMessage(peerID string, msg *Message, pc *PeerConnection, deps SessionDeps, log *logrus.Entry) {
	switch msg.Type {
	case MsgStateUpdate,
		MsgStateEvent,
		MsgActivityUpdate,
		MsgStats,
		MsgPeerState,
		MsgPeerConnected,
		MsgPeerDisconnected,
		MsgRequestState:
		handleStateMessage(peerID, msg, pc, deps, log)

	case MsgOpenTerminal,
		MsgCapturePane,
		MsgCapturePaneResult,
		MsgFileRead,
		MsgOpenUpload,
		MsgFileReadResult:
		handleStreamMessage(peerID, msg, pc, deps, log)

	case MsgSessionAttrsSnapshot,
		MsgSessionAttrsDelta,
		MsgSessionOrderSnapshot,
		MsgSessionOrderDelta,
		MsgGroupSnapshot,
		MsgGroupDelta:
		handleAttrsMessage(peerID, msg, pc, deps, log)

	case MsgSessionAction:
		handleActionMessage(msg, pc, deps, log)

	default:
		log.WithField("type", msg.Type).Debug("unknown session message")
	}
}
