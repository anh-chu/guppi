package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestCatalogSyncDirFailureDoesNotAcknowledgeOrPublish is a real regression
// proof that a post-rename directory-fsync failure must NOT be reported as
// an accepted, successful command.
//
// Real production path exercised: SessionCommandService.ExecuteSessionCommand
// (action=create) -> executeCreate -> Catalog.apply -> Store.Update ->
// Store.commit -> Store.syncDirHook. This test injects a real StoreOptions
// SyncDirHook that fails (simulating a real directory-fsync error after the
// rename that already made the new file durably visible on disk), through
// the real OpenStore/NewCatalog/NewSessionCommandService wiring -- not an
// isolated helper.
//
// Per the round-6 non-negotiable constraint ("No command may return accepted
// success after a directory fsync failure"), ExecuteSessionCommand must
// return either a non-nil error or CommandResult.Accepted == false in this
// case. The CURRENT code (pkg/state/catalog.go apply(), see the
// errSyncDirFailedAfterRename branch) deliberately treats this as success:
// it adopts the new document and returns nil so the caller (executeCreate)
// returns CommandResult{Accepted: true} with a nil error. This test proves
// that current behavior is exactly backwards from the new invariant.
func TestCatalogSyncDirFailureDoesNotAcknowledgeOrPublish(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "v2store")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatal(err)
	}

	owner := NewOwnerID()
	syncDirCalls := 0
	// Store.commit's call sequence for this test's first real Update is:
	// (1) OpenStore's own initial-document write, (2) writeBackup's own
	// syncDirHook (since a current file already exists from (1)), (3) the
	// FINAL post-rename syncDirHook that errSyncDirFailedAfterRename wraps.
	// Only call 3 is the exact "durable rename succeeded, only the
	// directory-entry fsync is uncertain" scenario the constraint targets;
	// failing call 2 instead exercises a different (already-safe,
	// non-swallowed) error path and would prove nothing.
	failFrom := 3
	store, err := OpenStore(storeDir, "node-a", StoreOptions{
		Owner: owner,
		SyncDirHook: func(_ string) error {
			syncDirCalls++
			if syncDirCalls < failFrom {
				return nil
			}
			// The rename that makes the new file durably visible on disk has
			// ALREADY happened by the time this hook runs (see Store.commit) --
			// this simulates exactly the "durable but directory-entry fsync
			// uncertain" scenario the non-negotiable constraint targets, not a
			// total write failure.
			return errors.New("injected directory fsync failure")
		},
	})
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}

	catalog := NewCatalog(owner, store)
	if err := catalog.Load(); err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}

	backend := &testBackend{
		liveGen:    map[string]string{},
		terminated: map[string]bool{},
	}
	svc := NewSessionCommandService(catalog, backend, nil, SessionCommandServiceOptions{Owner: owner})

	params, _ := json.Marshal(CreateParams{Name: "durability-probe"})
	cmdID := NewCommandID()
	result, err := svc.ExecuteSessionCommand(t.Context(), SessionCommand{
		ID:     cmdID,
		Action: ActionCreate,
		Params: params,
	})

	if syncDirCalls == 0 {
		t.Fatal("test setup broken: injected SyncDirHook was never invoked, this run proves nothing")
	}

	if err == nil && result.Accepted {
		t.Fatalf("FAIL-CLOSED VIOLATION: ExecuteSessionCommand(create) returned Accepted=true with nil error "+
			"despite a real, injected post-rename directory-fsync failure (hook called %d time(s)) -- "+
			"the command was falsely acknowledged as durably successful. Catalog.apply's "+
			"errSyncDirFailedAfterRename branch currently swallows this error and returns nil instead of "+
			"failing closed. result=%+v", syncDirCalls, result)
	}

	// The first attempt left the store's in-memory document -- including the
	// success receipt executeCreate wrote for this exact CommandID -- adopted
	// but with durability unconfirmed (see Store.durabilityUncertain). A
	// caller retry with the SAME CommandID (e.g. an HTTP client retrying
	// after a timeout/5xx) must NOT be able to find that receipt and report
	// success without the store ever re-attempting a real fsync: that would
	// silently upgrade an explicitly-failed write to acknowledged success.
	// The injected SyncDirHook keeps failing every call, so the retry's
	// mandatory revalidation attempt must also fail closed.
	sameCmd := SessionCommand{
		ID:     cmdID,
		Action: ActionCreate,
		Params: params,
	}

	retryCallsBefore := syncDirCalls
	retryResult, retryErr := svc.ExecuteSessionCommand(t.Context(), sameCmd)

	if syncDirCalls <= retryCallsBefore {
		t.Fatal("test setup broken: retry never re-attempted a directory fsync, this run proves nothing")
	}

	if retryErr == nil && retryResult.Accepted {
		t.Fatalf("FAIL-CLOSED VIOLATION: retrying the SAME CommandID after a durability-uncertain write "+
			"returned Accepted=true with nil error without ever confirming a successful fsync -- the uncertain "+
			"receipt was replayed as authoritative success instead of re-attempting (and failing) durability. "+
			"retryResult=%+v", retryResult)
	}
}

// TestNonCreateReceiptReplayFailsClosedUnderPersistentDurabilityUncertainty is
// a regression proof for round-8 Finding A: kill/label/recover/dismiss
// replay via SessionCommandService.peekReceipt -> Catalog.CommandReceipt,
// which (before this fix) read a cached receipt directly from
// Catalog.commands without ever calling Store.Revalidate. A receipt
// committed while the store's durability was uncertain (see
// errSyncDirFailedAfterRename / Store.durabilityUncertain) would therefore
// be served back as accepted success on replay, even though the write that
// produced it was never durably confirmed.
//
// Real production path exercised: SessionCommandService.ExecuteSessionCommand
// (action=kill) -> executeKill -> peekReceipt -> Catalog.CommandReceipt, and
// (for the first, receipt-writing call) executeKill -> commitSessionReceipt ->
// Catalog.apply -> Store.Update -> Store.commit -> Store.syncDirHook, through
// the real OpenStore/NewCatalog/NewSessionCommandService wiring.
//
// The injected SyncDirHook fails PERSISTENTLY (every call, forever) once
// armed -- unlike the create test above, which only proves a single retry,
// this proves the fail-closed behavior holds across repeated retries of the
// exact same CommandID, since a real caller may retry more than once.
func TestNonCreateReceiptReplayFailsClosedUnderPersistentDurabilityUncertainty(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "v2store")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatal(err)
	}

	owner := NewOwnerID()

	var failing bool
	relCalls := 0
	totalCalls := 0
	store, err := OpenStore(storeDir, "node-a", StoreOptions{
		Owner: owner,
		SyncDirHook: func(_ string) error {
			totalCalls++
			if !failing {
				return nil
			}
			relCalls++
			// Let the pending-create removal (session/kill-pending) commit
			// fully succeed (its backup-sync and final rename-sync are the
			// first two calls after arming), and let the receipt commit's own
			// backup-sync succeed too (third call). Only the FOURTH call --
			// the receipt commit's post-rename directory fsync -- fails, which
			// is the exact "rename already succeeded, only the directory entry
			// fsync is uncertain" scenario errSyncDirFailedAfterRename targets.
			// Every call after that keeps failing (persistent), never resets.
			if relCalls < 4 {
				return nil
			}
			return errors.New("injected persistent directory fsync failure")
		},
	})
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}

	catalog := NewCatalog(owner, store)
	if err := catalog.Load(); err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}

	backend := &testBackend{
		liveGen:    map[string]string{},
		terminated: map[string]bool{},
	}
	svc := NewSessionCommandService(catalog, backend, nil, SessionCommandServiceOptions{Owner: owner})

	// Create a session intent (durability confirmed at this point, hook not
	// yet armed to fail). This leaves the session as a PendingCreateRecord,
	// which is enough for the subsequent kill to take the
	// "cancel pending create" branch in executeKill.
	createParams, _ := json.Marshal(CreateParams{Name: "non-create-durability-probe"})
	createResult, err := svc.ExecuteSessionCommand(t.Context(), SessionCommand{
		ID:     NewCommandID(),
		Action: ActionCreate,
		Params: createParams,
	})
	if err != nil || !createResult.Accepted {
		t.Fatalf("setup: create failed, this run proves nothing: err=%v result=%+v", err, createResult)
	}

	// Arm the persistent failure, then issue the kill whose receipt commit
	// will hit the uncertain-durability window.
	failing = true
	killCmd := SessionCommand{
		ID:     NewCommandID(),
		Action: ActionKill,
		Ref:    createResult.Ref,
	}

	firstResult, firstErr := svc.ExecuteSessionCommand(t.Context(), killCmd)
	if totalCalls == 0 || relCalls == 0 {
		t.Fatal("test setup broken: injected SyncDirHook was never invoked while armed, this run proves nothing")
	}
	if firstErr == nil && firstResult.Accepted {
		t.Fatalf("setup broken: first kill attempt should itself observe the injected fsync failure and fail closed, "+
			"got accepted result with nil error: %+v (relCalls=%d)", firstResult, relCalls)
	}
	if !store.DurabilityUncertain() {
		t.Fatal("test setup broken: store should be left durability-uncertain after the injected failure, this run proves nothing")
	}

	// Retry the SAME kill CommandID multiple times. Before this fix,
	// Catalog.CommandReceipt (via peekReceipt) would find the receipt that
	// commitSessionReceipt's failed Store.Update adopted into memory and
	// return it as accepted success, without ever calling Store.Revalidate.
	// Since the injected hook fails persistently, every retry's revalidation
	// attempt must also fail, and the command must fail closed every time.
	for attempt := 1; attempt <= 3; attempt++ {
		retryResult, retryErr := svc.ExecuteSessionCommand(t.Context(), killCmd)
		if retryErr == nil && retryResult.Accepted {
			t.Fatalf("FAIL-CLOSED VIOLATION (retry %d): replaying the same kill CommandID under persistent "+
				"durability uncertainty returned Accepted=true with nil error -- a receipt written during an "+
				"unconfirmed write was served back as success instead of failing closed. retryResult=%+v",
				attempt, retryResult)
		}
		if !store.DurabilityUncertain() {
			t.Fatalf("retry %d: store unexpectedly cleared durability-uncertain state despite the injected hook "+
				"still failing persistently -- test setup broken, this run proves nothing", attempt)
		}
	}
}
