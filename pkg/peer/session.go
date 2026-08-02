package peer

import (
	"compress/flate"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	ws "github.com/anh-chu/termyard/pkg/ws"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"

	"github.com/anh-chu/termyard/pkg/activity"
	"github.com/anh-chu/termyard/pkg/common"
	"github.com/anh-chu/termyard/pkg/identity"
	"github.com/anh-chu/termyard/pkg/model"
	"github.com/anh-chu/termyard/pkg/pty"
	"github.com/anh-chu/termyard/pkg/recovery"
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

// sendInitialSessionAttrs pushes our full shared session-attribute map once on
// link-up so the remote can reconcile via per-key LWW.
func sendInitialSessionAttrs(pc *PeerConnection, deps SessionDeps) {
	if deps.AttrsSink == nil {
		return
	}
	attrs := deps.AttrsSink.SnapshotAttrs()
	if len(attrs) == 0 {
		return
	}
	msg, err := NewMessage(MsgSessionAttrsSnapshot, SessionAttrsSnapshotPayload{
		Origin: deps.Identity.Fingerprint(),
		Attrs:  attrs,
	})
	if err != nil {
		return
	}
	pc.Enqueue(msg)
}

func sendInitialSessionOrder(pc *PeerConnection, deps SessionDeps) {
	if deps.OrderSink == nil {
		return
	}
	orders := deps.OrderSink.SnapshotOrders()
	if len(orders) == 0 {
		return
	}
	msg, err := NewMessage(MsgSessionOrderSnapshot, SessionOrderSnapshotPayload{
		Origin: deps.Identity.Fingerprint(),
		Orders: orders,
	})
	if err != nil {
		return
	}
	pc.Enqueue(msg)
}

func sendInitialGroups(pc *PeerConnection, deps SessionDeps) {
	if deps.GroupSink == nil {
		return
	}
	groups := deps.GroupSink.SnapshotGroups()
	if len(groups) == 0 {
		return
	}
	msg, err := NewMessage(MsgGroupSnapshot, GroupSnapshotPayload{
		Origin: deps.Identity.Fingerprint(),
		Groups: groups,
	})
	if err != nil {
		return
	}
	pc.Enqueue(msg)
}

// sendInitialPeerState pushes a snapshot containing only the local host
// (transitivity off, see plan §3.5).
func sendInitialPeerState(pc *PeerConnection, deps SessionDeps, remotePeerID string) {
	hosts := deps.Manager.GetHostsForPeer(remotePeerID)
	msg, err := NewMessage(MsgPeerState, PeerStatePayload{Hosts: hosts})
	if err != nil {
		return
	}
	pc.Enqueue(msg)
}

func sendStateUpdate(pc *PeerConnection, deps SessionDeps) {
	sessions := deps.LocalMgr.GetSessions()
	msg, err := NewMessage(MsgStateUpdate, StateUpdatePayload{Sessions: sessions, Version: common.VERSION})
	if err != nil {
		return
	}
	pc.Enqueue(msg)
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

// handleOpenTerminal is the host end of a per-terminal data connection.
func handleOpenTerminal(p OpenTerminalPayload, pc *PeerConnection, deps SessionDeps, log *logrus.Entry) {
	log = log.WithFields(logrus.Fields{"stream": p.StreamID, "session": p.Session})
	dial := pc.Role == RoleDialer
	var conn *websocket.Conn
	if deps.Manager == nil || deps.DaemonReg == nil || deps.Identity == nil {
		return
	}
	if dial {
		addr := deps.Manager.GetPeerAddress(pc.HostID)
		c, err := DialPeerStream(context.Background(), addr, deps.Identity, p.Token)
		if err != nil {
			log.WithError(err).Debug("host data-conn dial failed")
			return
		}
		conn = c
	} else {
		if deps.StreamReg == nil || deps.Manager == nil || deps.DaemonReg == nil {
			return
		}
		ps := NewPendingStream(p.StreamID, p.Session, p.Cols, p.Rows, deps.Manager.LocalID(), p.ViewerHostID, pc.HostID)
		deps.StreamReg.Register(p.Token, ps)
		c, ok := ps.WaitResolved(streamSetupTimeout)
		if !ok {
			return
		}
		conn = c
	}
	defer conn.Close()
	// Enable write compression with fastest level so PTY host→viewer output
	// (the bulk direction) is compressed on the wire.
	conn.EnableWriteCompression(true)
	if err := conn.SetCompressionLevel(flate.BestSpeed); err != nil {
		log.WithError(err).Debug("set compression level ignored")
	}

	socketPath := deps.DaemonReg.SocketPath(p.Session)
	ds, err := pty.NewDaemonSession(socketPath)
	if err != nil {
		log.WithError(err).Debug("failed to connect to daemon socket")
		return
	}
	defer ds.Close()
	_ = ds.Resize(p.Cols, p.Rows)
	ws.BridgeDirectPTY(conn, ds, p.Session, deps.ActTracker, log, false)
}

// handleCapturePane captures the daemon ring buffer for the requested session.
func handleCapturePane(p CapturePanePayload, pc *PeerConnection, deps SessionDeps, log *logrus.Entry) {
	res := CapturePaneResultPayload{Token: p.Token}
	if deps.DaemonReg == nil {
		res.Error = "daemon registry unavailable"
	} else if text, err := deps.DaemonReg.Capture(p.Session); err != nil {
		res.Error = err.Error()
	} else {
		res.Text = model.LastLines(text, p.Lines)
	}
	msg, err := NewMessage(MsgCapturePaneResult, res)
	if err != nil {
		log.WithError(err).Debug("capture-pane result marshal failed")
		return
	}
	pc.Enqueue(msg)
}

// maxFileReadSize caps the file content sent over peer relay (10 MB).
const maxFileReadSize = 10 << 20

// handleFileRead reads a local file and sends its content back over the
// control link. Relative paths resolve against the session's active pane CWD.
func handleFileRead(p FileReadPayload, pc *PeerConnection, deps SessionDeps, log *logrus.Entry) {
	res := FileReadResultPayload{Token: p.Token}

	path := p.Path
	if !filepath.IsAbs(path) {
		base := ""
		if p.Session != "" && deps.DaemonReg != nil {
			for _, s := range deps.DaemonReg.List() {
				if s.ID == p.Session {
					base = s.Cwd
					break
				}
			}
		}
		if base == "" {
			res.Error = "cannot resolve relative path: no active pane cwd"
			if msg, err := NewMessage(MsgFileReadResult, res); err == nil {
				pc.Enqueue(msg)
			}
			return
		}
		path = filepath.Clean(filepath.Join(base, path))
	} else {
		path = filepath.Clean(path)
	}

	info, err := os.Stat(path)
	if err != nil {
		res.Error = "not found"
		if msg, err := NewMessage(MsgFileReadResult, res); err == nil {
			pc.Enqueue(msg)
		}
		return
	}
	if info.IsDir() {
		res.Error = "path is a directory"
		if msg, err := NewMessage(MsgFileReadResult, res); err == nil {
			pc.Enqueue(msg)
		}
		return
	}
	if info.Size() > maxFileReadSize {
		res.Error = "file too large"
		if msg, err := NewMessage(MsgFileReadResult, res); err == nil {
			pc.Enqueue(msg)
		}
		return
	}

	f, err := os.Open(path)
	if err != nil {
		res.Error = err.Error()
		if msg, err := NewMessage(MsgFileReadResult, res); err == nil {
			pc.Enqueue(msg)
		}
		return
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		res.Error = err.Error()
		if msg, err := NewMessage(MsgFileReadResult, res); err == nil {
			pc.Enqueue(msg)
		}
		return
	}

	res.Data = base64.StdEncoding.EncodeToString(data)
	res.FileName = filepath.Base(path)

	// Detect content type.
	ext := filepath.Ext(path)
	ct := mime.TypeByExtension(ext)
	if ct == "" {
		ct = http.DetectContentType(data)
	}
	res.ContentType = ct

	msg, err := NewMessage(MsgFileReadResult, res)
	if err != nil {
		log.WithError(err).Debug("file-read result marshal failed")
		return
	}
	pc.Enqueue(msg)
}

// handleSessionMessage dispatches messages received from the remote peer.
func handleSessionMessage(peerID string, msg *Message, pc *PeerConnection, deps SessionDeps, log *logrus.Entry) {
	switch msg.Type {
	case MsgStateUpdate:
		var p StateUpdatePayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			log.WithError(err).Debug("invalid state-update")
			return
		}
		deps.Manager.UpdatePeerSessions(peerID, p.Sessions)
		if p.Version != "" {
			deps.Manager.UpdatePeerVersion(peerID, p.Version)
		}

	case MsgStateEvent:
		var p StateEventPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return
		}
		if p.EventType == "session-renamed" && deps.BrowserHub != nil {
			newName := ""
			switch data := p.Data.(type) {
			case map[string]any:
				if v, ok := data["new_name"].(string); ok {
					newName = v
				}
			case map[string]string:
				newName = data["new_name"]
			}
			if p.Session != "" && newName != "" {
				deps.BrowserHub.BroadcastJSON(map[string]interface{}{
					"type":    "session-renamed",
					"host":    peerID,
					"session": p.Session,
					"data": map[string]string{
						"new_name": newName,
					},
				})
			}
		}
		deps.Manager.UpdatePeerSessions(peerID, getPeerSessions(deps.Manager, peerID))

	case MsgToolEvent:
		var p ToolEventPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return
		}
		if p.Event != nil {
			p.Event.Host = peerID
			p.Event.HostName = deps.Manager.GetHostName(peerID)
			deps.ToolTracker.Record(p.Event)
		}

	case MsgActivityUpdate:
		var p ActivityUpdatePayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return
		}
		for _, s := range p.Snapshots {
			s.Host = peerID
		}
		deps.Manager.UpdatePeerActivity(peerID, p.Snapshots)

	case MsgStats:
		var p StatsPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return
		}
		deps.Manager.UpdatePeerStats(peerID, p.Stats)

	case MsgPeerState:
		var p PeerStatePayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return
		}
		for _, host := range p.Hosts {
			if deps.Manager.IsLocal(host.ID) {
				continue
			}
			deps.Manager.UpdatePeerSessions(host.ID, host.Sessions)
			if host.Online && !deps.Manager.HasHost(host.ID) {
				deps.Manager.RegisterPeer(host.ID, host.Name, "", nil)
			}
		}

	case MsgPeerConnected:
		var p PeerNotifyPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return
		}
		deps.Manager.RegisterPeer(p.ID, p.Name, "", nil)

	case MsgPeerDisconnected:
		var p PeerNotifyPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return
		}
		deps.Manager.UnregisterPeer(p.ID)

	case MsgOpenTerminal:
		var p OpenTerminalPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			log.WithError(err).Debug("invalid open-terminal")
			return
		}
		go handleOpenTerminal(p, pc, deps, log)

	case MsgSessionAction:
		var p SessionActionPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return
		}
		handleSessionAction(&p, pc, deps, log)

	case MsgRequestState:
		sendStateUpdate(pc, deps)

	case MsgCapturePane:
		var p CapturePanePayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			log.WithError(err).Debug("invalid capture-pane")
			return
		}
		go handleCapturePane(p, pc, deps, log)

	case MsgCapturePaneResult:
		var p CapturePaneResultPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return
		}
		if deps.CaptureReg != nil {
			deps.CaptureReg.Deliver(p.Token, CaptureResult{Text: p.Text, Error: p.Error})
		}

	case MsgFileRead:
		var p FileReadPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			log.WithError(err).Debug("invalid file-read")
			return
		}
		go handleFileRead(p, pc, deps, log)

	case MsgOpenUpload:
		var p OpenUploadPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			log.WithError(err).Debug("invalid open-upload")
			return
		}
		go handleOpenUpload(p, pc, deps, log)

	case MsgFileReadResult:
		var p FileReadResultPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return
		}
		if deps.FileReadReg != nil {
			deps.FileReadReg.Deliver(p.Token, FileReadResult{
				Data: p.Data, ContentType: p.ContentType,
				FileName: p.FileName, Error: p.Error,
			})
		}

	case MsgSessionAttrsSnapshot:
		var p SessionAttrsSnapshotPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			log.WithError(err).Debug("invalid session-attrs-snapshot")
			return
		}
		if p.Origin == deps.Identity.Fingerprint() || deps.AttrsSink == nil {
			return
		}
		changed, err := deps.AttrsSink.ApplyRemoteSnapshot(p.Attrs)
		if err != nil {
			log.WithError(err).Warn("apply remote session-attrs snapshot failed")
			return
		}
		if len(changed) > 0 && deps.BrowserHub != nil {
			deps.BrowserHub.BroadcastJSON(map[string]interface{}{
				"type":   "session-attrs-updated",
				"origin": p.Origin,
			})
		}
		log.WithField("origin", p.Origin).WithField("changed", len(changed)).Debug("applied remote session-attrs snapshot")

	case MsgSessionAttrsDelta:
		var p SessionAttrsDeltaPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			log.WithError(err).Debug("invalid session-attrs-delta")
			return
		}
		if p.Origin == deps.Identity.Fingerprint() || deps.AttrsSink == nil {
			return
		}
		accepted, err := deps.AttrsSink.ApplyRemoteDelta(p.Key, p.Attr.Background, p.Attr.Hidden, p.Attr.ScheduleID, p.Attr.UpdatedAt)
		if err != nil {
			log.WithError(err).Warn("apply remote session-attrs delta failed")
			return
		}
		if !accepted {
			return
		}
		if deps.BrowserHub != nil {
			deps.BrowserHub.BroadcastJSON(map[string]interface{}{
				"type":   "session-attrs-updated",
				"origin": p.Origin,
				"key":    p.Key,
			})
		}
		log.WithField("origin", p.Origin).WithField("key", p.Key).Debug("applied remote session-attrs delta")
	case MsgSessionOrderSnapshot:
		var p SessionOrderSnapshotPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			log.WithError(err).Debug("invalid session-order-snapshot")
			return
		}
		if p.Origin == deps.Identity.Fingerprint() || deps.OrderSink == nil {
			return
		}
		changed, err := deps.OrderSink.ApplyRemoteSnapshot(p.Orders)
		if err != nil {
			log.WithError(err).Warn("apply remote session-order snapshot failed")
			return
		}
		if len(changed) > 0 && deps.BrowserHub != nil {
			deps.BrowserHub.BroadcastJSON(map[string]interface{}{
				"type":   "session-order-updated",
				"origin": p.Origin,
			})
		}
		log.WithField("origin", p.Origin).WithField("changed", len(changed)).Debug("applied remote session-order snapshot")
	case MsgSessionOrderDelta:
		var p SessionOrderDeltaPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			log.WithError(err).Debug("invalid session-order-delta")
			return
		}
		if p.Origin == deps.Identity.Fingerprint() || deps.OrderSink == nil {
			return
		}
		accepted, err := deps.OrderSink.ApplyRemoteDelta(p.Key, p.Order.Rank, p.Order.UpdatedAt)
		if err != nil {
			log.WithError(err).Warn("apply remote session-order delta failed")
			return
		}
		if !accepted {
			return
		}
		if deps.BrowserHub != nil {
			deps.BrowserHub.BroadcastJSON(map[string]interface{}{
				"type":   "session-order-updated",
				"origin": p.Origin,
				"key":    p.Key,
			})
		}
		log.WithField("origin", p.Origin).WithField("key", p.Key).Debug("applied remote session-order delta")
	case MsgGroupSnapshot:
		var p GroupSnapshotPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			log.WithError(err).Debug("invalid group-snapshot")
			return
		}
		if p.Origin == deps.Identity.Fingerprint() || deps.GroupSink == nil {
			return
		}
		changed, err := deps.GroupSink.ApplyRemoteSnapshot(p.Groups)
		if err != nil {
			log.WithError(err).Warn("apply remote group snapshot failed")
			return
		}
		if len(changed) > 0 && deps.BrowserHub != nil {
			deps.BrowserHub.BroadcastJSON(map[string]interface{}{
				"type":   "groups-updated",
				"origin": p.Origin,
			})
		}
		log.WithField("origin", p.Origin).WithField("changed", len(changed)).Debug("applied remote group snapshot")
	case MsgGroupDelta:
		var p GroupDeltaPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			log.WithError(err).Debug("invalid group-delta")
			return
		}
		if p.Origin == deps.Identity.Fingerprint() || deps.GroupSink == nil {
			return
		}
		accepted, err := deps.GroupSink.ApplyRemoteDelta(p.ID, p.Group)
		if err != nil {
			log.WithError(err).Warn("apply remote group delta failed")
			return
		}
		if !accepted {
			return
		}
		if deps.BrowserHub != nil {
			deps.BrowserHub.BroadcastJSON(map[string]interface{}{
				"type":   "groups-updated",
				"origin": p.Origin,
				"id":     p.ID,
			})
		}
		log.WithField("origin", p.Origin).WithField("id", p.ID).Debug("applied remote group delta")

	default:
		log.WithField("type", msg.Type).Debug("unknown session message")
	}
}

func handleSessionAction(payload *SessionActionPayload, pc *PeerConnection, deps SessionDeps, log *logrus.Entry) {
	if deps.Launch == nil {
		log.Warn("no launch service available for session action")
		return
	}
	switch payload.Action {
	case "new":
		var params struct {
			Name           string `json:"name"`
			Path           string `json:"path,omitempty"`
			Command        string `json:"command,omitempty"`
			WorktreeBranch string `json:"worktree_branch,omitempty"`
			ScheduleID     string `json:"schedule_id,omitempty"`
		}
		if err := json.Unmarshal(payload.Params, &params); err != nil || params.Name == "" {
			return
		}
		if deps.Launch == nil {
			log.Warn("no launch service available for session action")
			return
		}
		req := sessionlaunch.Request{
			Name:           params.Name,
			Path:           params.Path,
			Command:        params.Command,
			WorktreeBranch: params.WorktreeBranch,
			ScheduleID:     params.ScheduleID,
		}
		if _, err := deps.Launch.Create(context.Background(), req); err != nil {
			log.WithError(err).Warn("new session via peer failed")
			return
		}
		sendStateUpdate(pc, deps)

	case "rename":
		// Daemon sessions don't support rename — no-op.

	case "select-window":
		// Daemon sessions are single-pane — no-op.

	case "kill":
		var params struct {
			ID   string `json:"id,omitempty"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(payload.Params, &params); err != nil || params.Name == "" {
			return
		}
		if err := deps.DaemonReg.Kill(params.Name); err != nil {
			log.WithError(err).Warn("kill session via peer failed")
		}
		if err := recovery.ForgetSession(params.Name); err != nil {
			log.WithError(err).Warn("failed to remove session from recovery manifest")
		}
		sendStateUpdate(pc, deps)

	case "regenerate-name":
		var params struct {
			Session string `json:"session"`
			Name    string `json:"name"`
		}
		if err := json.Unmarshal(payload.Params, &params); err != nil || params.Session == "" {
			return
		}
		if deps.LocalMgr == nil {
			log.Warn("no state manager available for regenerate-name action")
			return
		}
		if params.Name != "" {
			deps.LocalMgr.ApplyAIName(params.Session, params.Name)
		} else if _, err := deps.LocalMgr.RegenerateName(params.Session); err != nil {
			log.WithError(err).Warn("regenerate name via peer failed")
			return
		}
		sendStateUpdate(pc, deps)

	case "set-display-name":
		var params struct {
			Session     string `json:"session"`
			DisplayName string `json:"display_name"`
			Clear       bool   `json:"clear"`
		}
		if err := json.Unmarshal(payload.Params, &params); err != nil || params.Session == "" {
			return
		}
		if deps.LocalMgr == nil {
			log.Warn("no state manager available for set-display-name action")
			return
		}
		deps.LocalMgr.SetDisplayName(params.Session, params.DisplayName, !params.Clear)
		sendStateUpdate(pc, deps)
	default:
		log.WithField("action", payload.Action).Debug("unknown session action")
	}
}

func getPeerSessions(m *Manager, peerID string) []*model.Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if h, ok := m.hosts[peerID]; ok {
		return h.Sessions
	}
	return nil
}
