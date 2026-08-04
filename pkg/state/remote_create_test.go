package state

import (
	"context"
	"errors"
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
	req.Requester = OwnerID("peer-b")
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
