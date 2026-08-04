package peer

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anh-chu/termyard/pkg/identity"
	"github.com/anh-chu/termyard/pkg/state"
)

var (
	validCommandID  = state.NewCommandID()
	validSessionID  = state.NewSessionID()
	validOwnerID    = state.NewOwnerID()
	validCommandID2 = state.NewCommandID()
	validCommandID3 = state.NewCommandID()
)

func testEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	_ = os.MkdirAll(filepath.Join(t.TempDir(), ".config", "termyard"), 0o700)
}

func makeTestV2Manager(t *testing.T) *Manager {
	t.Helper()
	testEnv(t)
	id, err := identity.Generate("test-node")
	if err != nil {
		t.Fatal(err)
	}
	ps, err := identity.NewPeerStore()
	if err != nil {
		t.Fatal(err)
	}
	return NewManager(id, ps, state.NewManager())
}

func newV2PeerConnection(hostID string) *PeerConnection {
	pc := NewPeerConnection(hostID, 8)
	pc.Caps = []string{CapV2Catalog, CapV2Command}
	pc.initV2Lazy()
	return pc
}

func TestSendCommand_Success(t *testing.T) {
	mgr := makeTestV2Manager(t)
	peerID := "remotea"
	pc := newV2PeerConnection(peerID)
	mgr.RegisterPeer(peerID, "remotea", "", pc)

	cmd := state.SessionCommand{
		ID:     validCommandID,
		Ref:    state.SessionRef{Owner: validOwnerID, Session: validSessionID},
		Action: "kill",
	}

	done := make(chan state.CommandResult, 1)
	go func() {
		res, err := mgr.SendCommand(context.Background(), peerID, cmd)
		if err != nil {
			t.Errorf("SendCommand: %v", err)
		}
		done <- res
	}()

	// Read the request from the peer's outbound queue.
	var req *V2CommandRequestPayload
	select {
	case req = <-pc.cmdQueue.ch:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for command request")
	}
	if req.ID != string(cmd.ID) {
		t.Fatalf("expected command id %q, got %q", cmd.ID, req.ID)
	}

	// Deliver reply as the remote would.
	data, _ := json.Marshal(state.CommandResult{ID: cmd.ID, Ref: cmd.Ref, Accepted: true})
	mgr.deliverCommandReply(peerID, pc, V2CommandReplyPayload{
		ID:      req.ID,
		Handled: true,
		Result:  data,
	})

	select {
	case res := <-done:
		if !res.Accepted {
			t.Fatalf("expected accepted result, got %+v", res)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for command result")
	}
}

func TestSendCommand_QueueFull(t *testing.T) {
	mgr := makeTestV2Manager(t)
	peerID := "remotea"
	pc := newV2PeerConnection(peerID)
	pc.cmdQueue = newReliableCommandQueue(0)
	mgr.RegisterPeer(peerID, "remotea", "", pc)

	cmd := state.SessionCommand{
		ID:     validCommandID2,
		Ref:    state.SessionRef{Owner: validOwnerID, Session: validSessionID},
		Action: "kill",
	}

	_, err := mgr.SendCommand(context.Background(), peerID, cmd)
	if err != ErrCommandQueueFull {
		t.Fatalf("expected ErrCommandQueueFull, got %v", err)
	}
}

func TestSendCommand_LostReplyRetryReturnsSameReceipt(t *testing.T) {
	// Lost reply idempotency is enforced both by the peer RPC layer
	// (waiter keyed by command ID) and by the command service deduplicating
	// identical command IDs.
	mgr := makeTestV2Manager(t)
	peerID := "remotea"
	pc := newV2PeerConnection(peerID)
	mgr.RegisterPeer(peerID, "remotea", "", pc)

	cmd := state.SessionCommand{
		ID:     validCommandID3,
		Ref:    state.SessionRef{Owner: validOwnerID, Session: validSessionID},
		Action: "kill",
	}

	// First attempt times out because the reply is lost.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := mgr.SendCommand(ctx, peerID, cmd)
	if err != context.DeadlineExceeded {
		t.Fatalf("expected timeout, got %v", err)
	}

	// The waiter is cleaned up; the command service would still have the
	// receipt. A retry with the same ID must not produce a duplicate side
	// effect. At the RPC layer we verify the request is re-issued with the
	// same ID and a fresh waiter is registered.
	// Drain the request left in the queue by the timed-out first attempt.
	select {
	case <-pc.cmdQueue.ch:
	case <-time.After(time.Second):
		t.Fatal("timeout draining stale request")
	}
	go func() {
		req := <-pc.cmdQueue.ch
		if req.ID != string(cmd.ID) {
			t.Errorf("retry command id mismatch: %s", req.ID)
		}
		resultData, _ := json.Marshal(state.CommandResult{ID: cmd.ID, Ref: cmd.Ref, Accepted: true})
		mgr.deliverCommandReply(peerID, pc, V2CommandReplyPayload{
			ID:      string(cmd.ID),
			Handled: true,
			Result:  resultData,
		})
	}()

	res, err := mgr.SendCommand(context.Background(), peerID, cmd)
	if err != nil {
		t.Fatalf("retry failed: %v", err)
	}
	if !res.Accepted {
		t.Fatalf("expected accepted result, got %+v", res)
	}
}

// TestSendCommand_RemoteStateErrorSurvivesRPCRoundTrip is the real-boundary
// proof for Finding 4 (typed state.StateError flattened to a plain string
// over peer RPC, breaking HTTP status mapping downstream in
// pkg/server/routes_state_v2.go's writeV2CommandError/mapV2ErrorCode). It
// exercises the exact wire path a real remote peer failure takes: a
// V2CommandReplyPayload built the way handleV2SessionCommandRequest's fixed
// setReplyError now builds it (structured ErrorCode/ErrorField/ErrorDetail,
// not just a flattened Error string), delivered through the real
// deliverCommandReply, and reconstructed by SendCommand's replyError call.
// Before the fix, SendCommand did `errors.New(res.payload.Error)` --a plain
// error-- so errors.As(err, &state.StateError{}) at the HTTP layer always
// failed for remote-originated errors, and every one of them was reported as
// the generic peer_offline/invalid_input code regardless of its real
// business meaning.
func TestSendCommand_RemoteStateErrorSurvivesRPCRoundTrip(t *testing.T) {
	mgr := makeTestV2Manager(t)
	peerID := "remotea"
	pc := newV2PeerConnection(peerID)
	mgr.RegisterPeer(peerID, "remotea", "", pc)

	cmd := state.SessionCommand{
		ID:     state.NewCommandID(),
		Ref:    state.SessionRef{Owner: validOwnerID, Session: validSessionID},
		Action: "kill",
	}

	errCh := make(chan error, 1)
	go func() {
		_, err := mgr.SendCommand(context.Background(), peerID, cmd)
		errCh <- err
	}()

	var req *V2CommandRequestPayload
	select {
	case req = <-pc.cmdQueue.ch:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for command request")
	}

	// Simulate the remote peer's handleV2SessionCommandRequest failing with a
	// real state.StateError, encoded exactly as the fixed setReplyError does
	// (see pkg/peer/rpc.go).
	remoteErr := state.StateError{Code: state.ErrGenerationMismatch, Field: "generation", Detail: "stale generation"}
	reply := V2CommandReplyPayload{ID: req.ID}
	setReplyError(&reply, remoteErr)
	if reply.ErrorCode == "" {
		t.Fatal("test setup broken: setReplyError did not populate ErrorCode for a state.StateError")
	}
	mgr.deliverCommandReply(peerID, pc, reply)

	var gotErr error
	select {
	case gotErr = <-errCh:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for SendCommand to return")
	}

	if gotErr == nil {
		t.Fatal("expected an error, got nil")
	}
	var se state.StateError
	if !errors.As(gotErr, &se) {
		t.Fatalf("STATE-ERROR LOST OVER RPC: SendCommand returned %v (%T), which does not unwrap to a "+
			"state.StateError -- the HTTP layer's writeV2CommandError/mapV2ErrorCode can only map a real "+
			"state.StateError to its correct status code; anything else falls back to a generic error", gotErr, gotErr)
	}
	if se.Code != state.ErrGenerationMismatch {
		t.Fatalf("reconstructed StateError has Code=%q, want %q", se.Code, state.ErrGenerationMismatch)
	}
	if se.Field != "generation" {
		t.Fatalf("reconstructed StateError has Field=%q, want %q", se.Field, "generation")
	}
}

func TestCatalogSlot_CoalescesSlowQueue(t *testing.T) {
	pc := newV2PeerConnection("peer")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create a test gate to block the emitter before draining slots.
	// This ensures multiple publishes accumulate before any flush occurs,
	// forcing deterministic coalescing.
	gate := make(chan struct{})
	pc.catalogSlot.setTestWaitBeforeDrain(gate)

	go runSnapshotEmitter(ctx, pc, pc.catalogSlot)

	// Push ten snapshots rapidly; the emitter will wake up on the first one
	// but block on the gate, allowing all ten to accumulate in the slot.
	for i := 0; i < 10; i++ {
		payload := V2CatalogSnapshotPayload{Revision: int64(i)}
		msg, _ := NewMessage(MsgV2CatalogSnapshot, payload)
		pc.catalogSlot.swap(msg)
	}

	// Close the gate to unblock the emitter, allowing it to drain all
	// accumulated snapshots. The coalescing logic ensures it emits only the
	// latest revision.
	close(gate)

	select {
	case f := <-pc.LoLane():
		var msg Message
		if err := json.Unmarshal(f.data, &msg); err != nil {
			t.Fatal(err)
		}
		if msg.Type != MsgV2CatalogSnapshot {
			t.Fatalf("expected %s, got %s", MsgV2CatalogSnapshot, msg.Type)
		}
		var p V2CatalogSnapshotPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if p.Revision != 9 {
			t.Fatalf("expected coalesced revision 9, got %d", p.Revision)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for coalesced snapshot")
	}

	// After coalescing, there should be no further pending snapshots.
	select {
	case <-pc.LoLane():
		t.Fatal("unexpected extra snapshot after coalescing")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestCommandWaiter_RegisterBeforeSend(t *testing.T) {
	mgr := makeTestV2Manager(t)
	cmdID := "race-id"
	peerID := "remotea"
	pc := newV2PeerConnection(peerID)

	// Register waiter before the reply exists.
	done := make(chan commandResult, 1)
	mgr.registerCommandWaiter(cmdID, peerID, pc, done)

	// Reply races in before send.
	mgr.deliverCommandReply(peerID, pc, V2CommandReplyPayload{ID: cmdID, Handled: true})

	select {
	case res := <-done:
		if !res.payload.Handled {
			t.Fatal("expected handled reply")
		}
	case <-time.After(time.Second):
		t.Fatal("waiter did not receive racing reply")
	}
}

// TestSendCommand_SameCommandIDDifferentPeersNoCrossTalk is the real-boundary
// proof for Finding (RPC command waiters keyed only by CommandID): two
// concurrent SendCommand calls that happen to carry the SAME CommandID but
// target DIFFERENT live peer connections must each receive their own correct
// reply, with no cross-talk and no spurious timeout. Before the composite
// (peerID, conn, id) waiter key, both registrations shared the single
// map[string]*commandWaiter entry keyed by CommandID alone: the second
// registerCommandWaiter call overwrote the first waiter's map entry, so a
// reply delivered for the first peer's connection could satisfy the SECOND
// call's waiter channel (cross-talk) while the first call's own waiter
// silently lost its registration and would hang until ctx timeout.
func TestSendCommand_SameCommandIDDifferentPeersNoCrossTalk(t *testing.T) {
	mgr := makeTestV2Manager(t)
	const sharedCmdID = state.CommandID("shared-command-id")

	peerAID := "peer-a"
	pcA := newV2PeerConnection(peerAID)
	mgr.RegisterPeer(peerAID, "peer-a", "", pcA)

	peerBID := "peer-b"
	pcB := newV2PeerConnection(peerBID)
	mgr.RegisterPeer(peerBID, "peer-b", "", pcB)

	cmdA := state.SessionCommand{ID: sharedCmdID, Ref: state.SessionRef{Owner: validOwnerID, Session: validSessionID}, Action: "kill"}
	cmdB := state.SessionCommand{ID: sharedCmdID, Ref: state.SessionRef{Owner: validOwnerID, Session: validSessionID}, Action: "label"}

	resA := make(chan state.CommandResult, 1)
	errA := make(chan error, 1)
	go func() {
		res, err := mgr.SendCommand(context.Background(), peerAID, cmdA)
		errA <- err
		resA <- res
	}()

	resB := make(chan state.CommandResult, 1)
	errB := make(chan error, 1)
	go func() {
		res, err := mgr.SendCommand(context.Background(), peerBID, cmdB)
		errB <- err
		resB <- res
	}()

	// Drain both requests -- both carry the identical CommandID, but on
	// separate connections/queues.
	var reqA, reqB *V2CommandRequestPayload
	select {
	case reqA = <-pcA.cmdQueue.ch:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for peer A's command request")
	}
	select {
	case reqB = <-pcB.cmdQueue.ch:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for peer B's command request")
	}
	if reqA.ID != string(sharedCmdID) || reqB.ID != string(sharedCmdID) {
		t.Fatalf("test setup broken: expected both requests to carry id %q, got %q and %q", sharedCmdID, reqA.ID, reqB.ID)
	}

	// Deliver DISTINCT replies on each connection, each carrying the SAME
	// CommandID. Correct routing must deliver reply A only to the call
	// waiting on connection A, and reply B only to the call waiting on
	// connection B.
	dataA, _ := json.Marshal(state.CommandResult{ID: sharedCmdID, Ref: cmdA.Ref, Accepted: true, DisplayName: "from-peer-a"})
	dataB, _ := json.Marshal(state.CommandResult{ID: sharedCmdID, Ref: cmdB.Ref, Accepted: true, DisplayName: "from-peer-b"})
	mgr.deliverCommandReply(peerAID, pcA, V2CommandReplyPayload{ID: reqA.ID, Handled: true, Result: dataA})
	mgr.deliverCommandReply(peerBID, pcB, V2CommandReplyPayload{ID: reqB.ID, Handled: true, Result: dataB})

	select {
	case err := <-errA:
		if err != nil {
			t.Fatalf("SendCommand to peer A: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("peer A's SendCommand timed out -- its waiter was likely clobbered by peer B's registration")
	}
	select {
	case err := <-errB:
		if err != nil {
			t.Fatalf("SendCommand to peer B: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("peer B's SendCommand timed out")
	}

	gotA := <-resA
	gotB := <-resB
	if gotA.DisplayName != "from-peer-a" {
		t.Fatalf("CROSS-TALK: call to peer A got result %+v, want DisplayName=from-peer-a", gotA)
	}
	if gotB.DisplayName != "from-peer-b" {
		t.Fatalf("CROSS-TALK: call to peer B got result %+v, want DisplayName=from-peer-b", gotB)
	}
}

// TestPeerIDForOwner_RoutesSendCommandToLiveOwnerBoundPeer proves the exact
// integration handleV2SessionCommand (pkg/server/routes_state_v2.go) relies
// on: once a peer's catalog snapshot has bound it to an owner (via
// UpdateRemoteCatalog, the same call the real peer-link layer makes for every
// inbound v2 catalog frame), PeerIDForOwner resolves that owner back to the
// live peer connection, and SendCommand using that resolved peerID actually
// reaches the connection's command queue and returns the delivered reply.
// Before Finding 8's remote-command-forwarding fix, no code path ever called
// PeerIDForOwner for a session command at all -- every command silently ran
// against the LOCAL catalog regardless of Ref.Owner. This test pins the two
// primitives (PeerIDForOwner + SendCommand) working together so a regression
// in either one is caught here, independent of the HTTP route wiring (covered
// separately in pkg/server's TestV2SessionCommand_RemoteRefRouting).
func TestPeerIDForOwner_RoutesSendCommandToLiveOwnerBoundPeer(t *testing.T) {
	mgr := makeTestV2Manager(t)
	peerID := "remote-node-b"
	pc := newV2PeerConnection(peerID)
	mgr.RegisterPeer(peerID, "node-b", "", pc)

	owner := state.OwnerIDFromFingerprint(peerID)
	sessionID := state.NewSessionID()
	mgr.UpdateRemoteCatalog(peerID, pc, state.OwnerCatalogSnapshot{
		Owner:    owner,
		Revision: 1,
		Sessions: []state.LocalSessionRecord{
			{ID: sessionID, Owner: owner, Ref: state.SessionRef{Owner: owner, Session: sessionID}, Phase: state.SessionPhaseActive},
		},
	})

	// This is the exact lookup handleV2SessionCommand performs before
	// forwarding: given a ref's owner, resolve which live peer connection
	// owns it.
	resolvedPeerID := mgr.PeerIDForOwner(owner)
	if resolvedPeerID != peerID {
		t.Fatalf("PeerIDForOwner(%q) = %q, want %q", owner, resolvedPeerID, peerID)
	}

	cmd := state.SessionCommand{
		ID:     validCommandID3,
		Ref:    state.SessionRef{Owner: owner, Session: sessionID},
		Action: "label",
	}

	done := make(chan state.CommandResult, 1)
	errCh := make(chan error, 1)
	go func() {
		res, err := mgr.SendCommand(context.Background(), resolvedPeerID, cmd)
		errCh <- err
		done <- res
	}()

	var req *V2CommandRequestPayload
	select {
	case req = <-pc.cmdQueue.ch:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for forwarded command request")
	}
	if req.ID != string(cmd.ID) {
		t.Fatalf("forwarded command id %q, want %q", req.ID, cmd.ID)
	}

	data, _ := json.Marshal(state.CommandResult{ID: cmd.ID, Ref: cmd.Ref, Accepted: true, DisplayName: "node-b-session"})
	mgr.deliverCommandReply(peerID, pc, V2CommandReplyPayload{ID: req.ID, Handled: true, Result: data})

	if err := <-errCh; err != nil {
		t.Fatalf("SendCommand: %v", err)
	}
	res := <-done
	if !res.Accepted || res.DisplayName != "node-b-session" {
		t.Fatalf("expected accepted result with node B's reply, got %+v", res)
	}
}

// TestSendCommand_SamePeerSameConnSameCommandIDSingleFlight is the real-
// boundary proof for round-8 Finding B: two concurrent SendCommand calls
// to the SAME peer, over the SAME live connection, carrying the EXACT SAME
// CommandID. Before the single-flight fix, the composite (peer, conn, id)
// waiter key from commit 7546b83 still let the second registerCommandWaiter
// call replace the first waiter's map entry (registerCommandWaiter is a
// map[key]=w assignment, not an insert-if-absent), so BOTH calls
// independently enqueued an outbound request and only whichever waiter was
// currently registered when the (single, coalesced) reply arrived could
// ever be satisfied -- the other caller hung until ctx.Done(). This test
// proves: (1) exactly ONE request is ever observed on the connection's
// outbound command queue for the shared key, and (2) both concurrent
// callers receive the identical successful result, neither timing out.
func TestSendCommand_SamePeerSameConnSameCommandIDSingleFlight(t *testing.T) {
	mgr := makeTestV2Manager(t)
	const sharedCmdID = state.CommandID("single-flight-shared-id")

	peerID := "peer-single-flight"
	pc := newV2PeerConnection(peerID)
	mgr.RegisterPeer(peerID, "peer-single-flight", "", pc)

	ref := state.SessionRef{Owner: validOwnerID, Session: validSessionID}
	cmd := state.SessionCommand{ID: sharedCmdID, Ref: ref, Action: "kill"}

	// Fire two concurrent SendCommand calls for the exact same
	// (peer, conn, CommandID) key, using a start gate so both calls race
	// registerCommandWaiter genuinely concurrently rather than sequentially.
	start := make(chan struct{})
	res1Ch := make(chan state.CommandResult, 1)
	err1Ch := make(chan error, 1)
	res2Ch := make(chan state.CommandResult, 1)
	err2Ch := make(chan error, 1)

	go func() {
		<-start
		res, err := mgr.SendCommand(context.Background(), peerID, cmd)
		err1Ch <- err
		res1Ch <- res
	}()
	go func() {
		<-start
		res, err := mgr.SendCommand(context.Background(), peerID, cmd)
		err2Ch <- err
		res2Ch <- res
	}()
	close(start)

	// Exactly one request must land on the connection's outbound queue for
	// this shared key: drain the first, then prove no second one ever
	// arrives (a short, bounded wait -- if the bug is present, a second
	// request from the other caller shows up here).
	var req *V2CommandRequestPayload
	select {
	case req = <-pc.cmdQueue.ch:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for the single expected outbound request")
	}
	if req.ID != string(sharedCmdID) {
		t.Fatalf("request id = %q, want %q", req.ID, sharedCmdID)
	}
	select {
	case second := <-pc.cmdQueue.ch:
		t.Fatalf("FINDING B VIOLATION: a SECOND outbound request was enqueued for the exact same "+
			"(peer, conn, CommandID) key -- single-flight is not deduplicating concurrent identical "+
			"requests. second=%+v", second)
	case <-time.After(200 * time.Millisecond):
		// Expected: no second request.
	}

	// Deliver exactly one reply for the one request that was actually sent.
	resultData, err := json.Marshal(state.CommandResult{ID: sharedCmdID, Ref: ref, Accepted: true, DisplayName: "single-flight-result"})
	if err != nil {
		t.Fatal(err)
	}
	delivered := mgr.deliverCommandReply(peerID, pc, V2CommandReplyPayload{ID: req.ID, Handled: true, Result: resultData})
	if !delivered {
		t.Fatal("deliverCommandReply reported no waiter delivered to -- expected at least one (both) subscribers")
	}

	// Both concurrent callers must receive the identical successful result;
	// neither may time out waiting for a reply that was routed only to the
	// other one.
	var err1, err2 error
	var res1, res2 state.CommandResult
	select {
	case err1 = <-err1Ch:
	case <-time.After(time.Second):
		t.Fatal("FINDING B VIOLATION: first concurrent caller timed out waiting for the shared reply")
	}
	select {
	case err2 = <-err2Ch:
	case <-time.After(time.Second):
		t.Fatal("FINDING B VIOLATION: second concurrent caller timed out waiting for the shared reply")
	}
	res1 = <-res1Ch
	res2 = <-res2Ch

	if err1 != nil {
		t.Fatalf("first caller: unexpected error: %v", err1)
	}
	if err2 != nil {
		t.Fatalf("second caller: unexpected error: %v", err2)
	}
	if res1 != res2 {
		t.Fatalf("concurrent callers for the same key received DIFFERENT results: res1=%+v res2=%+v", res1, res2)
	}
	if !res1.Accepted || res1.DisplayName != "single-flight-result" {
		t.Fatalf("unexpected result: %+v", res1)
	}
}
