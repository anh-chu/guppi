package peer

import (
	"context"
	"encoding/json"
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

