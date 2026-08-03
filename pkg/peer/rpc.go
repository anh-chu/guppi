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

// commandWaiter is a pending command RPC awaiting its reply.
type commandWaiter struct {
	id   string
	done chan commandResult
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
	m.registerCommandWaiter(reqPayload.ID, done)
	defer m.unregisterCommandWaiter(reqPayload.ID)

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
			return result, errors.New(res.payload.Error)
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
	m.registerCommandWaiter(reqPayload.ID, done)
	defer m.unregisterCommandWaiter(reqPayload.ID)

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
			return errors.New(res.payload.Error)
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
	m.registerCommandWaiter(reqPayload.ID, done)
	defer m.unregisterCommandWaiter(reqPayload.ID)

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
			return result, errors.New(res.payload.Error)
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

func (m *Manager) registerCommandWaiter(id string, done chan commandResult) {
	m.cmdMu.Lock()
	defer m.cmdMu.Unlock()
	if m.commandWaiters == nil {
		m.commandWaiters = make(map[string]*commandWaiter)
	}
	m.commandWaiters[id] = &commandWaiter{id: id, done: done}
}

func (m *Manager) unregisterCommandWaiter(id string) {
	m.cmdMu.Lock()
	defer m.cmdMu.Unlock()
	delete(m.commandWaiters, id)
}

// deliverCommandReply routes a v2 command reply to its waiter. It returns
// true if a waiter was found.
func (m *Manager) deliverCommandReply(reply V2CommandReplyPayload) bool {
	m.cmdMu.Lock()
	w, ok := m.commandWaiters[reply.ID]
	m.cmdMu.Unlock()
	if !ok {
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
