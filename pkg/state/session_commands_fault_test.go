package state

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
)

// TestSetPresentationBackgroundAtomicOnStoreFailure proves the both-or-nothing
// guarantee documented on executeSetPresentation: when backgrounding a
// session (Background false->true) triggers a store/persistence failure, the
// command must fail closed and leave NEITHER the Background flag flip NOR the
// layout-membership removal visible afterward. It exercises the real
// OpenStore/NewCatalog/NewSessionCommandService wiring (not an isolated
// in-memory helper) with a real StoreOptions.WriteHook fault injected on the
// durable write triggered by the set_presentation command itself, mirroring
// the injection style already used by TestCatalogSyncDirFailureDoesNotAcknowledgeOrPublish.
func TestSetPresentationBackgroundAtomicOnStoreFailure(t *testing.T) {
	dir := t.TempDir()
	owner := testOwner()

	var failWrites bool
	store, err := OpenStore(dir, "node-fault-bg", StoreOptions{
		Owner: owner,
		WriteHook: func(f *os.File, p []byte) error {
			if failWrites {
				return errors.New("injected set_presentation write failure")
			}
			_, err := f.Write(p)
			return err
		},
	})
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}

	catalog := NewCatalog(owner, store)
	if err := catalog.Load(); err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}

	rec := activeRecord(SessionID("faultbg1234567890"), "gen-fb")
	if err := catalog.PutSession(rec); err != nil {
		t.Fatalf("PutSession setup: %v", err)
	}
	layout := LayoutRecord{ID: NewLayoutID(), Owner: owner, Revision: 1, Tree: Leaf(rec.Ref)}
	if err := catalog.PutLayout(layout); err != nil {
		t.Fatalf("PutLayout setup: %v", err)
	}

	backend := newTestBackend()
	svc := NewSessionCommandService(catalog, backend, nil, SessionCommandServiceOptions{})

	revBefore := store.Revision()

	// Arm the fault only now, so it hits exactly the set_presentation
	// command's own durable write, not the setup writes above.
	failWrites = true

	background := true
	params, _ := json.Marshal(PresentationParams{Background: &background})
	result, cmdErr := svc.ExecuteSessionCommand(context.Background(), SessionCommand{
		ID:     NewCommandID(),
		Ref:    rec.Ref,
		Action: ActionSetPresentation,
		Params: params,
	})

	if cmdErr == nil && result.Accepted {
		t.Fatalf("ATOMICITY VIOLATION: set_presentation(background=true) returned Accepted=true with nil error "+
			"despite an injected durable-write failure; result=%+v", result)
	}

	gotSession, ok := catalog.Session(rec.ID)
	if !ok {
		t.Fatal("test setup broken: session vanished after failed set_presentation")
	}
	if gotSession.Background {
		t.Fatal("ATOMICITY VIOLATION: Background flag was flipped to true despite the injected store write failure " +
			"-- the flag change and layout removal must commit in the same transaction or not at all")
	}

	gotLayout, ok := catalog.Layout(layout.ID)
	if !ok {
		t.Fatal("ATOMICITY VIOLATION: layout was dropped entirely despite the injected store write failure")
	}
	if !findLeaf(gotLayout.Tree, rec.Ref) {
		t.Fatal("ATOMICITY VIOLATION: session leaf was removed from the layout despite the injected store write " +
			"failure, while (per the assertions above) the Background flag was not -- or vice versa; both must " +
			"move together")
	}

	if store.Revision() != revBefore {
		t.Fatalf("ATOMICITY VIOLATION: store revision advanced from %d to %d despite the injected write failure",
			revBefore, store.Revision())
	}

	// Now disarm the fault and confirm the exact same command retried
	// (replayed under a fresh CommandID, since the failed attempt's ID was
	// never durably recorded as a receipt) succeeds and moves both changes
	// together on the next attempt.
	failWrites = false
	result2, err := svc.ExecuteSessionCommand(context.Background(), SessionCommand{
		ID:     NewCommandID(),
		Ref:    rec.Ref,
		Action: ActionSetPresentation,
		Params: params,
	})
	if err != nil {
		t.Fatalf("retry after fault cleared: %v", err)
	}
	if !result2.Accepted {
		t.Fatalf("expected retry to be accepted, got %+v", result2)
	}
	gotSession2, _ := catalog.Session(rec.ID)
	if !gotSession2.Background {
		t.Fatal("expected Background = true after successful retry")
	}
	gotLayout2, ok := catalog.Layout(layout.ID)
	if ok && findLeaf(gotLayout2.Tree, rec.Ref) {
		t.Fatal("expected session leaf removed from layout after successful backgrounding retry")
	}
}

// TestSetPresentationBackgroundRemovesLeafInSingleRevision proves that
// backgrounding a session (Background false->true) removes it from the sole
// layout's pane tree in the same catalog.apply mutation as the flag flip --
// not a second, separately-committed mutation -- and that the whole command
// (mutation + its receipt commit, the two-phase pattern every session
// command in this file uses) durably advances the revision by exactly 2 no
// matter how many times the same CommandID is replayed: Background=true and
// no matching leaf left behind afterward.
func TestSetPresentationBackgroundRemovesLeafInSingleRevision(t *testing.T) {
	svc, catalog, _, store, cleanup := newTestCommandService(t)
	defer cleanup()

	rec := activeRecord(SessionID("bgleafremove123456"), "gen-blr")
	if err := catalog.PutSession(rec); err != nil {
		t.Fatal(err)
	}
	layout := LayoutRecord{ID: NewLayoutID(), Owner: testOwner(), Revision: 1, Tree: Leaf(rec.Ref)}
	if err := catalog.PutLayout(layout); err != nil {
		t.Fatal(err)
	}

	revBefore := store.Revision()

	background := true
	params, _ := json.Marshal(PresentationParams{Background: &background})
	cmdID := NewCommandID()
	result, err := svc.ExecuteSessionCommand(context.Background(), SessionCommand{
		ID: cmdID, Ref: rec.Ref, Action: ActionSetPresentation, Params: params,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Accepted {
		t.Fatalf("expected accepted, got %+v", result)
	}

	// One catalog.apply for the flag flip + layout-leaf removal (committed
	// together, atomically), plus one catalog.apply for the receipt --
	// exactly 2 durable revisions for one accepted command, and no more.
	if got := store.Revision(); got != revBefore+2 {
		t.Fatalf("expected exactly 2 revision increments (mutation + receipt) from %d, got %d", revBefore, got)
	}

	gotSession, ok := catalog.Session(rec.ID)
	if !ok {
		t.Fatal("session missing after backgrounding")
	}
	if !gotSession.Background {
		t.Fatal("expected Background = true")
	}

	gotLayout, ok := catalog.Layout(layout.ID)
	if ok && findLeaf(gotLayout.Tree, rec.Ref) {
		t.Fatal("expected no matching leaf left in the layout after backgrounding")
	}

	// Exactly one receipt for this command ID.
	if _, ok, replayErr := svc.peekReceipt(cmdID); !ok || replayErr != nil {
		t.Fatalf("expected exactly one durable receipt for the backgrounding command, ok=%v err=%v", ok, replayErr)
	}
	result2, err := svc.ExecuteSessionCommand(context.Background(), SessionCommand{
		ID: cmdID, Ref: rec.Ref, Action: ActionSetPresentation, Params: params,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result2 != result {
		t.Fatalf("replay of the same command ID returned a different result: %+v vs %+v", result2, result)
	}
	if got := store.Revision(); got != revBefore+2 {
		t.Fatalf("replaying the same command ID must not durably mutate anything again: expected %d, got %d",
			revBefore+2, got)
	}
}
