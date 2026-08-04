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
	result, err := svc.ExecuteSessionCommand(t.Context(), SessionCommand{
		ID:     NewCommandID(),
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
}
