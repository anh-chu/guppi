package peer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/sirupsen/logrus"

	"github.com/anh-chu/termyard/pkg/state"
)

// ErrCommandQueueFull is returned when a reliable command cannot be queued
// because the peer's outbound queue is saturated.
var ErrCommandQueueFull = errors.New("peer command queue full")

// ErrCommandTimeout is returned when a reliable command reply does not arrive
// in time.
var ErrCommandTimeout = errors.New("peer command timeout")

// commandWaiterKey identifies one pending command RPC registration. A
// CommandID alone is not a safe map key: two concurrent callers can target
// different peers/connections with the same CommandID (e.g. a caller retries
// after reconnecting to a different live connection for the same peerID, or
// two independent peers happen to receive requests carrying the same ID), and
// keying by ID alone let one registration silently overwrite another's map
// entry -- misrouting replies and letting one handler's deferred unregister
// delete a different, still-pending waiter.
type commandWaiterKey struct {
	peerID string
	conn   *PeerConnection
	id     string
}

// commandWaiter is a pending command RPC awaiting its reply. It is keyed by
// the composite commandWaiterKey (peer identity + connection + command ID),
// so a reply arriving from a different peer or a superseded connection (e.g.
// one attacker-controlled peer guessing another peer's in-flight CommandID)
// cannot satisfy it, and registrations for genuinely distinct (peer, conn,
// id) tuples can never collide with each other.
type commandWaiter struct {
	id     string
	peerID string
	conn   *PeerConnection
	done   chan commandResult
}

type commandResult struct {
	payload V2CommandReplyPayload
	err     error
}

// reliableCommandQueue carries outbound v2 command requests with bounded
// backpressure. It is separate from the lo lane so command RPCs can be
// rejected explicitly instead of silently dropped.
type reliableCommandQueue struct {
	mu     sync.Mutex
	closed bool
	ch     chan *V2CommandRequestPayload
}

func newReliableCommandQueue(depth int) *reliableCommandQueue {
	if depth < 0 {
		depth = 32
	}
	return &reliableCommandQueue{ch: make(chan *V2CommandRequestPayload, depth)}
}

func (q *reliableCommandQueue) enqueue(req *V2CommandRequestPayload) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return false
	}
	select {
	case q.ch <- req:
		return true
	default:
		return false
	}
}

func (q *reliableCommandQueue) close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.closed = true
	close(q.ch)
}

// V2CommandSender is the subset of PeerConnection needed to send v2 commands.
// mailto:it exists so tests can stub the transport.
type V2CommandSender interface {
	HostID() string
	enqueueCommand(req *V2CommandRequestPayload) bool
}

// PeerConnection v2 extensions.

func (pc *PeerConnection) initV2() {
	pc.catalogSlot = newSnapshotSlot()
	pc.workspaceSlot = newSnapshotSlot()
	pc.cmdQueue = newReliableCommandQueue(32)
}

func (pc *PeerConnection) enqueueCommand(req *V2CommandRequestPayload) bool {
	return pc.cmdQueue.enqueue(req)
}

// EnqueueV2CatalogSnapshot queues the latest catalog snapshot into the
// coalescing snapshot slot. It returns false if the connection is closed.
func (pc *PeerConnection) EnqueueV2CatalogSnapshot(msg *Message) bool {
	if pc.catalogSlot == nil {
		return false
	}
	return pc.catalogSlot.swap(msg)
}

// EnqueueV2WorkspaceSnapshot queues the latest workspace snapshot into the
// coalescing snapshot slot.
func (pc *PeerConnection) EnqueueV2WorkspaceSnapshot(msg *Message) bool {
	if pc.workspaceSlot == nil {
		return false
	}
	return pc.workspaceSlot.swap(msg)
}

// HasV2 reports whether the peer advertised v2 catalog/command capabilities.
func (pc *PeerConnection) HasV2() bool {
	return pc.HasCapability(CapV2Catalog) && pc.HasCapability(CapV2Command)
}

// runV2Writers starts the snapshot emitters and command sender for this
// connection. It returns when all writers exit.
func runV2Writers(ctx context.Context, pc *PeerConnection, log *logrus.Entry) {
	var wg sync.WaitGroup

	if pc.catalogSlot != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runSnapshotEmitter(ctx, pc, pc.catalogSlot)
		}()
	}
	if pc.workspaceSlot != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runSnapshotEmitter(ctx, pc, pc.workspaceSlot)
		}()
	}
	if pc.cmdQueue != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runCommandSender(ctx, pc, log)
		}()
	}

	wg.Wait()
}

// runCommandSender pulls command requests from the bounded queue and emits
// them onto the low-priority lane.
func runCommandSender(ctx context.Context, pc *PeerConnection, log *logrus.Entry) {
	if pc.cmdQueue == nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-pc.Done():
			return
		case req, ok := <-pc.cmdQueue.ch:
			if !ok {
				return
			}
			msg, err := NewMessage(MsgV2CommandRequest, req)
			if err != nil {
				log.WithError(err).Debug("failed to marshal v2 command request")
				continue
			}
			data, err := json.Marshal(msg)
			if err != nil {
				continue
			}
			if !pc.enqueue(pc.lo, wireFrame{data: data}) {
				// Queue full or closed; the caller will time out waiting for a
				// reply. Log and drop the request so we don't block forever.
				log.Debug("v2 command request dropped: peer queue full or closed")
			}
		}
	}
}

// SendCommand executes a reliable v2 command RPC to peerID. It registers the
// waiter before enqueueing the request, so a reply that races ahead of the
// send is still delivered. If the command queue is full, it returns
// ErrCommandQueueFull immediately.
func (m *Manager) SendCommand(ctx context.Context, peerID string, cmd state.SessionCommand) (state.CommandResult, error) {
	var result state.CommandResult
	reqPayload, err := buildV2CommandRequest(cmd)
	if err != nil {
		return result, err
	}

	m.mu.RLock()
	pc := m.getPeerConnectionLocked(peerID)
	m.mu.RUnlock()
	if pc == nil {
		return result, fmt.Errorf("peer %s: %w", peerID, errors.New("no live connection"))
	}
	if !pc.HasV2() {
		return result, fmt.Errorf("peer %s: %w", peerID, errors.New("v2 command not supported"))
	}

	// Register waiter first to avoid lost-reply races.
	done := make(chan commandResult, 1)
	m.registerCommandWaiter(reqPayload.ID, peerID, pc, done)
	defer m.unregisterCommandWaiter(reqPayload.ID, peerID, pc)

	if !pc.enqueueCommand(reqPayload) {
		return result, ErrCommandQueueFull
	}

	select {
	case <-ctx.Done():
		return result, ctx.Err()
	case res, ok := <-done:
		if !ok {
			return result, errors.New("command waiter closed")
		}
		if res.err != nil {
			return result, res.err
		}
		if res.payload.Error != "" {
			return result, replyError(res.payload)
		}
		if !res.payload.Handled {
			return result, errors.New("command not handled")
		}
		if err := json.Unmarshal(res.payload.Result, &result); err != nil {
			return result, fmt.Errorf("decode command result: %w", err)
		}
		return result, nil
	}
}

// setReplyError populates reply's error fields from err. When err wraps (or
// is) a state.StateError, its Code/Field/Detail are preserved alongside the
// plain message so the receiving side can reconstruct the real typed error
// (see replyError) instead of losing it to an opaque string over the wire.
func setReplyError(reply *V2CommandReplyPayload, err error) {
	reply.Error = err.Error()
	var se state.StateError
	if errors.As(err, &se) {
		reply.ErrorCode = string(se.Code)
		reply.ErrorField = se.Field
		reply.ErrorDetail = se.Detail
	}
}

// replyError reconstructs the error carried by a command reply. When the
// reply carries a structured ErrorCode (i.e. the sender's failure was a
// state.StateError), it is reconstructed as a real state.StateError so
// callers such as the HTTP layer's error-code-to-status mapping see the
// original business error, not a generic/opaque one -- even though the
// error crossed a peer RPC boundary. Falls back to a plain error for
// non-StateError failures or replies without a structured code (e.g. from
// an older peer).
func replyError(reply V2CommandReplyPayload) error {
	if reply.Error == "" {
		return nil
	}
	if reply.ErrorCode != "" {
		return state.StateError{Code: state.ErrorCode(reply.ErrorCode), Field: reply.ErrorField, Detail: reply.ErrorDetail}
	}
	return errors.New(reply.Error)
}

func buildV2CommandRequest(cmd state.SessionCommand) (*V2CommandRequestPayload, error) {
	data, err := json.Marshal(cmd)
	if err != nil {
		return nil, err
	}
	return &V2CommandRequestPayload{
		ID:      string(cmd.ID),
		Kind:    V2CommandKindSession,
		Command: data,
	}, nil
}

// SendWorkspaceCommand executes a reliable workspace-command RPC to peerID.
// It returns a typed error on rejection or transport failure.
func (m *Manager) SendWorkspaceCommand(ctx context.Context, peerID string, cmd state.WorkspaceCommand) error {
	reqPayload, err := buildV2WorkspaceCommandRequest(cmd)
	if err != nil {
		return err
	}

	m.mu.RLock()
	pc := m.getPeerConnectionLocked(peerID)
	m.mu.RUnlock()
	if pc == nil {
		return state.StateError{Code: state.ErrWorkspaceOwnerOffline, Field: "peer_id", Detail: fmt.Sprintf("peer %s is offline", peerID)}
	}
	if !pc.HasV2() {
		return state.StateError{Code: state.ErrLegacyPeerUnsupported, Field: "peer_id", Detail: fmt.Sprintf("peer %s does not support v2 workspace commands", peerID)}
	}

	done := make(chan commandResult, 1)
	m.registerCommandWaiter(reqPayload.ID, peerID, pc, done)
	defer m.unregisterCommandWaiter(reqPayload.ID, peerID, pc)

	if !pc.enqueueCommand(reqPayload) {
		return ErrCommandQueueFull
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case res, ok := <-done:
		if !ok {
			return errors.New("command waiter closed")
		}
		if res.err != nil {
			return res.err
		}
		if res.payload.Error != "" {
			return replyError(res.payload)
		}
		if !res.payload.Handled {
			return errors.New("workspace command not handled")
		}
		return nil
	}
}

func buildV2WorkspaceCommandRequest(cmd state.WorkspaceCommand) (*V2CommandRequestPayload, error) {
	data, err := json.Marshal(cmd)
	if err != nil {
		return nil, err
	}
	return &V2CommandRequestPayload{
		ID:      string(cmd.ID),
		Kind:    V2CommandKindWorkspace,
		Command: data,
	}, nil
}

// SendRemoteCreate executes a reliable remote-create RPC to peerID.
func (m *Manager) SendRemoteCreate(ctx context.Context, peerID string, req state.RemoteCreateRequest) (state.RemoteCreateResult, error) {
	var result state.RemoteCreateResult
	reqPayload, err := buildV2RemoteCreateRequest(req)
	if err != nil {
		return result, err
	}

	m.mu.RLock()
	pc := m.getPeerConnectionLocked(peerID)
	m.mu.RUnlock()
	if pc == nil {
		return result, state.StateError{Code: state.ErrWorkspaceOwnerOffline, Field: "peer_id", Detail: fmt.Sprintf("peer %s is offline", peerID)}
	}
	if !pc.HasV2() {
		return result, state.StateError{Code: state.ErrLegacyPeerUnsupported, Field: "peer_id", Detail: fmt.Sprintf("peer %s does not support v2 remote creates", peerID)}
	}

	done := make(chan commandResult, 1)
	m.registerCommandWaiter(reqPayload.ID, peerID, pc, done)
	defer m.unregisterCommandWaiter(reqPayload.ID, peerID, pc)

	if !pc.enqueueCommand(reqPayload) {
		return result, ErrCommandQueueFull
	}

	select {
	case <-ctx.Done():
		return result, ctx.Err()
	case res, ok := <-done:
		if !ok {
			return result, errors.New("command waiter closed")
		}
		if res.err != nil {
			return result, res.err
		}
		if res.payload.Error != "" {
			return result, replyError(res.payload)
		}
		if !res.payload.Handled {
			return result, errors.New("remote create not handled")
		}
		if err := json.Unmarshal(res.payload.Result, &result); err != nil {
			return result, fmt.Errorf("decode remote create result: %w", err)
		}
		return result, nil
	}
}

func buildV2RemoteCreateRequest(req state.RemoteCreateRequest) (*V2CommandRequestPayload, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	return &V2CommandRequestPayload{
		ID:      string(req.IntentID),
		Kind:    V2CommandKindRemoteCreate,
		Command: data,
	}, nil
}

// registerCommandWaiter registers w under its exact (peerID, conn, id)
// composite key. Two registrations for genuinely different keys (different
// peer, different connection, or different id) never collide. A second
// registration for the EXACT SAME key (concurrent callers racing the same
// peer/connection/CommandID -- possible when a caller retries a timed-out
// RPC on the same still-live connection before the first attempt's waiter is
// unregistered) replaces the map entry, matching the pre-existing single-
// outbound-request-per-ID design: only one request for that exact key is
// ever actually sent/in flight, so only the most recent registration should
// receive the eventual reply. The superseded waiter's caller either already
// timed out (its ctx.Done() case) or is about to, since deliverCommandReply
// can now only deliver to the current registration.
func (m *Manager) registerCommandWaiter(id, peerID string, conn *PeerConnection, done chan commandResult) {
	m.cmdMu.Lock()
	defer m.cmdMu.Unlock()
	if m.commandWaiters == nil {
		m.commandWaiters = make(map[commandWaiterKey]*commandWaiter)
	}
	key := commandWaiterKey{peerID: peerID, conn: conn, id: id}
	m.commandWaiters[key] = &commandWaiter{id: id, peerID: peerID, conn: conn, done: done}
}

// unregisterCommandWaiter removes exactly the waiter registered under
// (peerID, conn, id) -- it never wildcard-deletes by id alone, so it cannot
// remove a different peer's or a different connection's still-pending
// waiter that happens to share the same CommandID.
func (m *Manager) unregisterCommandWaiter(id, peerID string, conn *PeerConnection) {
	m.cmdMu.Lock()
	defer m.cmdMu.Unlock()
	delete(m.commandWaiters, commandWaiterKey{peerID: peerID, conn: conn, id: id})
}

// deliverCommandReply routes a v2 command reply to its waiter. peerID and conn
// identify the authenticated connection the reply arrived on and form part of
// the lookup key (along with reply.ID), so a different (or reconnected) peer
// cannot satisfy another peer's in-flight waiter by guessing or replaying a
// CommandID, and a reply cannot be misdelivered to an unrelated peer's
// registration that happens to share the same CommandID. It returns true if
// a waiter was found and the reply was delivered to it.
func (m *Manager) deliverCommandReply(peerID string, conn *PeerConnection, reply V2CommandReplyPayload) bool {
	m.cmdMu.Lock()
	w, ok := m.commandWaiters[commandWaiterKey{peerID: peerID, conn: conn, id: reply.ID}]
	m.cmdMu.Unlock()
	if !ok {
		logrus.WithFields(logrus.Fields{
			"command_id": reply.ID,
			"peer":       peerID,
		}).Debug("dropping v2 command reply: no matching waiter for this peer/connection/command id")
		return false
	}
	select {
	case w.done <- commandResult{payload: reply}:
		return true
	default:
		return false
	}
}

// getPeerConnectionLocked returns the live PeerConnection for id, or nil.
// Caller must hold m.mu for reading.
func (m *Manager) getPeerConnectionLocked(id string) *PeerConnection {
	if h, ok := m.hosts[id]; ok && h.Conn != nil {
		return h.Conn
	}
	return nil
}
