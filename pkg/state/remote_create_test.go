package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

func newTestRemoteCreateCoordinator(t *testing.T) (*RemoteCreateCoordinator, *Catalog, func()) {
	t.Helper()
	owner := testOwner()
	store, cleanup := newTestStore(t, owner)
	catalog := NewCatalog(owner, store)
	if err := catalog.Load(); err != nil {
		t.Fatal(err)
	}
	backend := newTestBackend()
	coord := NewRemoteCreateCoordinator(catalog, backend, RemoteCreateCoordinatorOptions{
		Tick:         50 * time.Millisecond,
		RetryInitial: 10 * time.Millisecond,
	})
	return coord, catalog, cleanup
}

// TestExecuteRemoteCreateFromPeer_RequesterMismatchRejected proves that a
// remote-create request whose Requester does not match the authenticated
// sender is rejected before any durable state is committed, closing the gap
// where the previous no-op check silently accepted any claimed Requester.
func TestExecuteRemoteCreateFromPeer_RequesterMismatchRejected(t *testing.T) {
	coord, catalog, cleanup := newTestRemoteCreateCoordinator(t)
	defer cleanup()

	before := catalog.Revision()
	req := RemoteCreateRequest{
		IntentID:  NewCommandID(),
		Requester: OwnerID("peer-c"),
		Name:      "spoofed",
		Shell:     "/bin/bash",
		Cwd:       "/tmp",
	}

	// Authenticated sender is peer-b, but the request claims to be from
	// peer-c. This must be rejected before any pending-create record or
	// session is committed.
	_, err := coord.ExecuteRemoteCreateFromPeer(context.Background(), req, "peer-b")

	var se StateError
	if !errors.As(err, &se) || se.Code != ErrOwnershipMismatch {
		t.Fatalf("expected ownership_mismatch error, got %v", err)
	}
	if got := catalog.Revision(); got != before {
		t.Fatalf("revision changed on rejected remote create: before=%d after=%d", before, got)
	}
	if len(catalog.PendingRemoteCreates()) != 0 {
		t.Fatalf("expected no pending remote create to be committed, got %+v", catalog.PendingRemoteCreates())
	}

	// The same request, correctly attributed to the authenticated sender, is
	// accepted.
	req.Requester = OwnerIDFromFingerprint("peer-b")
	res, err := coord.ExecuteRemoteCreateFromPeer(context.Background(), req, "peer-b")
	if err != nil {
		t.Fatalf("expected correctly-attributed remote create to succeed, got: %v", err)
	}
	if !res.Accepted {
		t.Fatalf("expected accepted result, got %+v", res)
	}
}

// TestExecuteRemoteCreateFromPeer_LocalCallerBypassesCheck proves the
// ownership check only applies to the peer-originated entrypoint; local
// (non-peer) callers keep using ExecuteRemoteCreate directly and are
// unaffected.
func TestExecuteRemoteCreateFromPeer_LocalCallerBypassesCheck(t *testing.T) {
	coord, _, cleanup := newTestRemoteCreateCoordinator(t)
	defer cleanup()

	req := RemoteCreateRequest{
		IntentID:  NewCommandID(),
		Requester: OwnerID("some-other-owner"),
		Name:      "local-created",
		Shell:     "/bin/bash",
		Cwd:       "/tmp",
	}
	res, err := coord.ExecuteRemoteCreateFromPeer(context.Background(), req, "")
	if err != nil {
		t.Fatalf("expected local caller (peerID==\"\") to bypass ownership check, got: %v", err)
	}
	if !res.Accepted {
		t.Fatalf("expected accepted result, got %+v", res)
	}
}

// TestReplayRemoteCreateIntentIDReturnsOwnSessionNotAnother mirrors
// TestReplaySameCommandIDReturnsOwnSessionNotAnother (session_commands_test.go)
// for the remote-create path: with several sequential remote creates in
// flight, replaying an early intent ID must return exactly that intent's own
// session, never a different one picked up by scanning current sessions in
// some other order (the historical bug in existingResultFromDocLocked).
func TestReplayRemoteCreateIntentIDReturnsOwnSessionNotAnother(t *testing.T) {
	coord, catalog, cleanup := newTestRemoteCreateCoordinator(t)
	defer cleanup()

	const n = 6
	reqs := make([]RemoteCreateRequest, n)
	results := make([]RemoteCreateResult, n)
	for i := 0; i < n; i++ {
		reqs[i] = RemoteCreateRequest{
			IntentID: NewCommandID(),
			Name:     fmt.Sprintf("remote-multi-%d", i),
			Shell:    "/bin/bash",
			Cwd:      "/tmp",
		}
		res, err := coord.ExecuteRemoteCreate(context.Background(), reqs[i])
		if err != nil {
			t.Fatalf("remote create %d failed: %v", i, err)
		}
		results[i] = res
	}

	// Wait for every remote create to be promoted from pending to an active
	// session record.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = coord.Run(ctx) }()
	for start := time.Now(); time.Since(start) < 5*time.Second; {
		if len(catalog.PendingRemoteCreates()) == 0 && len(catalog.Sessions()) == n {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := len(catalog.Sessions()); got != n {
		t.Fatalf("expected %d active sessions, got %d", n, got)
	}

	for i := n - 1; i >= 0; i-- {
		replay, err := coord.ExecuteRemoteCreate(context.Background(), reqs[i])
		if err != nil {
			t.Fatalf("replay %d failed: %v", i, err)
		}
		if replay.Ref.Session != results[i].Ref.Session {
			t.Fatalf("replay of intent %d returned wrong session: got %q, want %q", i, replay.Ref.Session, results[i].Ref.Session)
		}
	}
}

// TestRemoteCreateRequest_JSONRoundTrip_NoSplitTarget is a regression test
// for a production defect where RemoteCreateRequest.Target was declared as
// a non-pointer SessionRef with `json:"target,omitempty"`. Go's
// encoding/json never treats a non-empty-looking struct as "empty" for
// omitempty purposes (structs are never considered empty regardless of
// their field values), so every non-split remote create request (the
// common case) serialized an invalid zero-value SessionRef (roughly
// `":0.0"` per SessionRef.MarshalJSON's canonical string form), which
// SessionRef.UnmarshalJSON correctly rejects as malformed on the receiving
// side. This caused pkg/peer/session_state.go's handleV2RemoteCreateRequest
// to fail every cross-node remote create with "malformed remote create
// request", even when the request never intended a split. This test
// exercises the exact same json.Marshal/json.Unmarshal round trip that
// handler performs (json.Unmarshal(req.Command, &r) where req.Command was
// produced by marshaling a RemoteCreateRequest), without a full peer/HTTP
// harness.
func TestRemoteCreateRequest_JSONRoundTrip_NoSplitTarget(t *testing.T) {
	req := RemoteCreateRequest{
		IntentID:  NewCommandID(),
		Requester: OwnerID("requester-a"),
		Name:      "no-split-session",
		Shell:     "/bin/bash",
		Cwd:       "/tmp",
		Cols:      120,
		Rows:      40,
		// Target intentionally left unset: this is the common non-split
		// remote create request shape.
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal RemoteCreateRequest: %v", err)
	}

	var decoded RemoteCreateRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal RemoteCreateRequest (a non-split remote create must never be rejected as malformed): %v\nwire payload: %s", err, data)
	}

	if decoded.Target != nil {
		t.Errorf("Target = %+v, want nil for a non-split remote create request", decoded.Target)
	}
	if decoded.Name != req.Name || decoded.Requester != req.Requester {
		t.Errorf("round trip lost fields: got %+v, want name=%q requester=%q", decoded, req.Name, req.Requester)
	}
}

// TestRemoteCreateRequest_JSONRoundTrip_WithSplitTarget proves the pointer
// Target field still round-trips correctly when a split IS requested, so
// the omitempty fix did not regress the split-on-create path.
func TestRemoteCreateRequest_JSONRoundTrip_WithSplitTarget(t *testing.T) {
	target := SessionRef{Owner: testOwner(), Session: "existingsession"}
	req := RemoteCreateRequest{
		IntentID:  NewCommandID(),
		Requester: testOwner(),
		Name:      "split-session",
		LayoutID:  LayoutID("layout-1"),
		Target:    &target,
		Direction: DirectionVertical,
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal RemoteCreateRequest: %v", err)
	}

	var decoded RemoteCreateRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal RemoteCreateRequest with split target: %v\nwire payload: %s", err, data)
	}
	if decoded.Target == nil {
		t.Fatal("Target = nil, want non-nil for a split remote create request")
	}
	if *decoded.Target != target {
		t.Errorf("Target = %+v, want %+v", *decoded.Target, target)
	}
}
