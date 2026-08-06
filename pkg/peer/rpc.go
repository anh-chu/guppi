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

// commandWaiter is the single in-flight RPC for one exact (peer, conn, id)
// key. It carries every subscriber's result channel: the first caller for a
// key (the "leader") creates it and actually sends the request; any
// concurrent caller presenting the EXACT SAME key (same peer, same live
// connection, same CommandID) joins the same waiter instead of sending a
// second request, and every subscriber receives an identical copy of the
// eventual result. A reply arriving from a different peer or a superseded
// connection (e.g. one attacker-controlled peer guessing another peer's
// in-flight CommandID) cannot satisfy it, and registrations for genuinely
// distinct (peer, conn, id) tuples can never collide with each other.
type commandWaiter struct {
	id     string
	peerID string
	conn   *PeerConnection
	dones  []chan commandResult
}

type commandResult struct {
	payload CommandReplyPayload
	err     error
}

// reliableCommandQueue carries outbound command requests with bounded
// backpressure. It is separate from the lo lane so command RPCs can be
// rejected explicitly instead of silently dropped.
type reliableCommandQueue struct {
	mu     sync.Mutex
	closed bool
	ch     chan *CommandRequestPayload
}

func newReliableCommandQueue(depth int) *reliableCommandQueue {
	if depth < 0 {
		depth = 32
	}
	return &reliableCommandQueue{ch: make(chan *CommandRequestPayload, depth)}
}

func (q *reliableCommandQueue) enqueue(req *CommandRequestPayload) bool {
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

// CommandSender is the subset of PeerConnection needed to send commands.
// mailto:it exists so tests can stub the transport.
type CommandSender interface {
	HostID() string
	enqueueCommand(req *CommandRequestPayload) bool
}

// PeerConnection command/catalog extensions.

func (pc *PeerConnection) initSlots() {
	pc.catalogSlot = newSnapshotSlot()
	pc.workspaceSlot = newSnapshotSlot()
	pc.cmdQueue = newReliableCommandQueue(32)
}

func (pc *PeerConnection) enqueueCommand(req *CommandRequestPayload) bool {
	return pc.cmdQueue.enqueue(req)
}

// EnqueueCatalogSnapshot queues the latest catalog snapshot into the
// coalescing snapshot slot. It returns false if the connection is closed.
func (pc *PeerConnection) EnqueueCatalogSnapshot(msg *Message) bool {
	if pc.catalogSlot == nil {
		return false
	}
	return pc.catalogSlot.swap(msg)
}

// EnqueueWorkspaceSnapshot queues the latest workspace snapshot into the
// coalescing snapshot slot.
func (pc *PeerConnection) EnqueueWorkspaceSnapshot(msg *Message) bool {
	if pc.workspaceSlot == nil {
		return false
	}
	return pc.workspaceSlot.swap(msg)
}



// runCommandWriters starts the snapshot emitters and command sender for this
// connection. It returns when all writers exit.
func runCommandWriters(ctx context.Context, pc *PeerConnection, log *logrus.Entry) {
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
			msg, err := NewMessage(MsgCommandRequest, req)
			if err != nil {
				log.WithError(err).Debug("failed to marshal command request")
				continue
			}
			data, err := json.Marshal(msg)
			if err != nil {
				continue
			}
			if !pc.enqueue(pc.lo, wireFrame{data: data}) {
				// Queue full or closed; the caller will time out waiting for a
				// reply. Log and drop the request so we don't block forever.
				log.Debug("command request dropped: peer queue full or closed")
			}
		}
	}
}

// SendCommand executes a reliable command RPC to peerID. It registers the
// waiter before enqueueing the request, so a reply that races ahead of the
// send is still delivered. If the command queue is full, it returns
// ErrCommandQueueFull immediately.
func (m *Manager) SendCommand(ctx context.Context, peerID string, cmd state.SessionCommand) (state.CommandResult, error) {
	var result state.CommandResult
	reqPayload, err := buildCommandRequest(cmd)
	if err != nil {
		return result, err
	}

	m.mu.RLock()
	pc := m.getPeerConnectionLocked(peerID)
	m.mu.RUnlock()
	if pc == nil {
		return result, fmt.Errorf("peer %s: %w", peerID, errors.New("no live connection"))
	}

	// Register waiter first to avoid lost-reply races. Only the leader for
	// this exact (peer, conn, CommandID) key actually enqueues a request; a
	// concurrent joiner for the exact same key shares the leader's in-flight
	// request and result instead of sending a second one.
	done := make(chan commandResult, 1)
	isLeader := m.registerCommandWaiter(reqPayload.ID, peerID, pc, done)
	defer m.unregisterCommandWaiter(reqPayload.ID, peerID, pc, done)

	if isLeader && !pc.enqueueCommand(reqPayload) {
		m.failCommandWaiters(reqPayload.ID, peerID, pc, ErrCommandQueueFull)
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
func setReplyError(reply *CommandReplyPayload, err error) {
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
func replyError(reply CommandReplyPayload) error {
	if reply.Error == "" {
		return nil
	}
	if reply.ErrorCode != "" {
		return state.StateError{Code: state.ErrorCode(reply.ErrorCode), Field: reply.ErrorField, Detail: reply.ErrorDetail}
	}
	return errors.New(reply.Error)
}

func buildCommandRequest(cmd state.SessionCommand) (*CommandRequestPayload, error) {
	data, err := json.Marshal(cmd)
	if err != nil {
		return nil, err
	}
	return &CommandRequestPayload{
		ID:      string(cmd.ID),
		Kind:    CommandKindSession,
		Command: data,
	}, nil
}

// SendWorkspaceCommand executes a reliable workspace-command RPC to peerID.
// It returns a typed error on rejection or transport failure.
func (m *Manager) SendWorkspaceCommand(ctx context.Context, peerID string, cmd state.WorkspaceCommand) error {
	reqPayload, err := buildWorkspaceCommandRequest(cmd)
	if err != nil {
		return err
	}

	m.mu.RLock()
	pc := m.getPeerConnectionLocked(peerID)
	m.mu.RUnlock()
	if pc == nil {
		return state.StateError{Code: state.ErrWorkspaceOwnerOffline, Field: "peer_id", Detail: fmt.Sprintf("peer %s is offline", peerID)}
	}

	done := make(chan commandResult, 1)
	isLeader := m.registerCommandWaiter(reqPayload.ID, peerID, pc, done)
	defer m.unregisterCommandWaiter(reqPayload.ID, peerID, pc, done)

	if isLeader && !pc.enqueueCommand(reqPayload) {
		m.failCommandWaiters(reqPayload.ID, peerID, pc, ErrCommandQueueFull)
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

func buildWorkspaceCommandRequest(cmd state.WorkspaceCommand) (*CommandRequestPayload, error) {
	data, err := json.Marshal(cmd)
	if err != nil {
		return nil, err
	}
	return &CommandRequestPayload{
		ID:      string(cmd.ID),
		Kind:    CommandKindWorkspace,
		Command: data,
	}, nil
}

// SendRemoteCreate executes a reliable remote-create RPC to peerID.
func (m *Manager) SendRemoteCreate(ctx context.Context, peerID string, req state.RemoteCreateRequest) (state.RemoteCreateResult, error) {
	var result state.RemoteCreateResult
	reqPayload, err := buildRemoteCreateRequest(req)
	if err != nil {
		return result, err
	}

	m.mu.RLock()
	pc := m.getPeerConnectionLocked(peerID)
	m.mu.RUnlock()
	if pc == nil {
		return result, state.StateError{Code: state.ErrWorkspaceOwnerOffline, Field: "peer_id", Detail: fmt.Sprintf("peer %s is offline", peerID)}
	}

	done := make(chan commandResult, 1)
	isLeader := m.registerCommandWaiter(reqPayload.ID, peerID, pc, done)
	defer m.unregisterCommandWaiter(reqPayload.ID, peerID, pc, done)

	if isLeader && !pc.enqueueCommand(reqPayload) {
		m.failCommandWaiters(reqPayload.ID, peerID, pc, ErrCommandQueueFull)
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

func buildRemoteCreateRequest(req state.RemoteCreateRequest) (*CommandRequestPayload, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	return &CommandRequestPayload{
		ID:      string(req.IntentID),
		Kind:    CommandKindRemoteCreate,
		Command: data,
	}, nil
}

// registerCommandWaiter joins or creates the single in-flight waiter for the
// exact (peerID, conn, id) composite key. Two registrations for genuinely
// different keys (different peer, different connection, or different id)
// never collide -- each gets its own waiter and its own outbound request.
// A second registration for the EXACT SAME key (concurrent callers racing
// the same peer/connection/CommandID, e.g. one caller retrying while an
// earlier identical call is still in flight) does NOT send a second
// request: it attaches done as an additional subscriber on the existing
// waiter and returns isLeader=false. The caller that created the waiter
// (isLeader=true) is the only one that may enqueue the outbound request;
// every subscriber -- leader and joiners alike -- receives an identical
// copy of the eventual result via deliverCommandReply/failCommandWaiters.
func (m *Manager) registerCommandWaiter(id, peerID string, conn *PeerConnection, done chan commandResult) (isLeader bool) {
	m.cmdMu.Lock()
	defer m.cmdMu.Unlock()
	if m.commandWaiters == nil {
		m.commandWaiters = make(map[commandWaiterKey]*commandWaiter)
	}
	key := commandWaiterKey{peerID: peerID, conn: conn, id: id}
	if w, ok := m.commandWaiters[key]; ok {
		w.dones = append(w.dones, done)
		return false
	}
	m.commandWaiters[key] = &commandWaiter{id: id, peerID: peerID, conn: conn, dones: []chan commandResult{done}}
	return true
}

// unregisterCommandWaiter removes exactly this caller's done channel from
// the waiter registered under (peerID, conn, id) -- it never wildcard-
// deletes by id alone, so it cannot remove a different peer's or a
// different connection's still-pending waiter that happens to share the
// same CommandID. Other subscribers on the same waiter (joiners sharing the
// exact same key) are left untouched; the map entry itself is only removed
// once every subscriber has unregistered (or once deliverCommandReply /
// failCommandWaiters has already resolved and removed it). This is a
// no-op if the entry was already removed by delivery/failure.
func (m *Manager) unregisterCommandWaiter(id, peerID string, conn *PeerConnection, done chan commandResult) {
	m.cmdMu.Lock()
	defer m.cmdMu.Unlock()
	key := commandWaiterKey{peerID: peerID, conn: conn, id: id}
	w, ok := m.commandWaiters[key]
	if !ok {
		return
	}
	filtered := w.dones[:0]
	for _, d := range w.dones {
		if d != done {
			filtered = append(filtered, d)
		}
	}
	w.dones = filtered
	if len(w.dones) == 0 {
		delete(m.commandWaiters, key)
	}
}

// deliverCommandReply routes a command reply to every subscriber of its
// waiter. peerID and conn identify the authenticated connection the reply
// arrived on and form part of the lookup key (along with reply.ID), so a
// different (or reconnected) peer cannot satisfy another peer's in-flight
// waiter by guessing or replaying a CommandID, and a reply cannot be
// misdelivered to an unrelated peer's registration that happens to share
// the same CommandID. The waiter is removed from the map atomically with
// lookup so a reply is delivered at most once; every current subscriber
// (the leader and any joiners for the exact same key) receives an
// identical copy of the reply. It returns true if a waiter was found and
// the reply was delivered to at least one subscriber.
func (m *Manager) deliverCommandReply(peerID string, conn *PeerConnection, reply CommandReplyPayload) bool {
	w := m.takeCommandWaiter(peerID, conn, reply.ID)
	if w == nil {
		logrus.WithFields(logrus.Fields{
			"command_id": reply.ID,
			"peer":       peerID,
		}).Debug("dropping command reply: no matching waiter for this peer/connection/command id")
		return false
	}
	delivered := false
	for _, d := range w.dones {
		select {
		case d <- commandResult{payload: reply}:
			delivered = true
		default:
		}
	}
	return delivered
}

// failCommandWaiters removes the waiter for (peerID, conn, id), if any, and
// delivers err to every one of its subscribers. It is used when the leader
// fails to even enqueue the outbound request (e.g. ErrCommandQueueFull):
// without this, any joiner attached to the same key would wait forever for
// a reply to a request that was never actually sent.
func (m *Manager) failCommandWaiters(id, peerID string, conn *PeerConnection, err error) {
	w := m.takeCommandWaiter(peerID, conn, id)
	if w == nil {
		return
	}
	for _, d := range w.dones {
		select {
		case d <- commandResult{err: err}:
		default:
		}
	}
}

// takeCommandWaiter atomically looks up and removes the waiter for the exact
// (peerID, conn, id) key, so it can be resolved (delivered or failed)
// exactly once.
func (m *Manager) takeCommandWaiter(peerID string, conn *PeerConnection, id string) *commandWaiter {
	m.cmdMu.Lock()
	defer m.cmdMu.Unlock()
	key := commandWaiterKey{peerID: peerID, conn: conn, id: id}
	w, ok := m.commandWaiters[key]
	if !ok {
		return nil
	}
	delete(m.commandWaiters, key)
	return w
}

// getPeerConnectionLocked returns the live PeerConnection for id, or nil.
// Caller must hold m.mu for reading.
func (m *Manager) getPeerConnectionLocked(id string) *PeerConnection {
	if h, ok := m.hosts[id]; ok && h.Conn != nil {
		return h.Conn
	}
	return nil
}
