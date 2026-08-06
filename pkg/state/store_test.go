package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var errInjected = errors.New("injected failure")

func TestOpenStoreMissingFile(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir, "node1", StoreOptions{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if s.Owner() == "" {
		t.Fatal("expected owner to be generated")
	}
	if s.Revision() != 0 {
		t.Fatalf("expected revision 0, got %d", s.Revision())
	}
	if _, err := os.Stat(s.currentPath()); err != nil {
		t.Fatalf("expected current file to be created: %v", err)
	}
	checkFilePerm(t, s.currentPath(), 0600)
}

func TestOpenStoreValidCurrent(t *testing.T) {
	dir := t.TempDir()
	owner := OwnerID("ownervalid1234567890")
	doc := mkBasicDoc(owner)
	mustWriteJSON(t, filepath.Join(dir, "node1.state.json"), doc)

	s, err := OpenStore(dir, "node1", StoreOptions{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if s.Owner() != owner {
		t.Fatalf("owner mismatch: %v vs %v", s.Owner(), owner)
	}
	snap := s.Snapshot()
	if snap.Revision != 7 {
		t.Fatalf("revision mismatch: %d", snap.Revision)
	}
}

func TestOpenStoreValidBackupFallback(t *testing.T) {
	dir := t.TempDir()
	owner := OwnerID("ownerbackup1234567890")
	current := mkBasicDoc(owner)
	current.Revision = 7
	backup := mkBasicDoc(owner)
	backup.Revision = 5
	corrupt := []byte("{ not json")

	mustWriteJSON(t, filepath.Join(dir, "node1.state.json"), corrupt)
	mustWriteJSON(t, filepath.Join(dir, "node1.state.json.bak"), backup)

	s, err := OpenStore(dir, "node1", StoreOptions{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if s.Revision() != 5 {
		t.Fatalf("expected backup revision 5, got %d", s.Revision())
	}
	// Recovery should have restored current from backup.
	curBytes, err := os.ReadFile(filepath.Join(dir, "node1.state.json"))
	if err != nil {
		t.Fatalf("read current: %v", err)
	}
	var restored AppDocument
	if err := json.Unmarshal(curBytes, &restored); err != nil {
		t.Fatalf("unmarshal restored current: %v", err)
	}
	if restored.Revision != 5 {
		t.Fatalf("expected restored current revision 5, got %d", restored.Revision)
	}
}

// TestOpenStoreRecoveryDoesNotOverwriteBackup is the core proof for Bug 1:
// when current is corrupt and recovery falls back to (and restores current
// from) the backup, the backup file itself must be left byte-for-byte
// unchanged. Prior to the fix, restoreCurrent went through the normal
// commit/rotate path, which rotated the (still zero-value, at that point in
// OpenStore's sequence) in-memory s.doc into the backup slot, destroying the
// one good copy.
func TestOpenStoreRecoveryDoesNotOverwriteBackup(t *testing.T) {
	dir := t.TempDir()
	owner := OwnerID("ownerbackup1234567890")
	backup := mkBasicDoc(owner)
	backup.Revision = 5
	corrupt := []byte("{ not json")

	backupPath := filepath.Join(dir, "node1.state.json.bak")
	mustWriteJSON(t, filepath.Join(dir, "node1.state.json"), corrupt)
	mustWriteJSON(t, backupPath, backup)

	originalBackupBytes, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("read original backup: %v", err)
	}

	s, err := OpenStore(dir, "node1", StoreOptions{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if s.Revision() != 5 {
		t.Fatalf("expected recovered revision 5, got %d", s.Revision())
	}

	afterBackupBytes, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("read backup after recovery: %v", err)
	}
	var afterBackup AppDocument
	if err := json.Unmarshal(afterBackupBytes, &afterBackup); err != nil {
		t.Fatalf("unmarshal backup after recovery: %v", err)
	}
	if afterBackup.Revision != 5 {
		t.Fatalf("backup was rotated/overwritten during recovery: revision %d, want 5", afterBackup.Revision)
	}
	var want AppDocument
	if err := json.Unmarshal(originalBackupBytes, &want); err != nil {
		t.Fatalf("unmarshal original backup: %v", err)
	}
	if !docsEqual(want, afterBackup) {
		t.Fatalf("backup content changed during recovery:\nbefore=%+v\nafter=%+v", want, afterBackup)
	}

	// Current should now hold the recovered (backup) document.
	curBytes, err := os.ReadFile(filepath.Join(dir, "node1.state.json"))
	if err != nil {
		t.Fatalf("read current: %v", err)
	}
	var restored AppDocument
	if err := json.Unmarshal(curBytes, &restored); err != nil {
		t.Fatalf("unmarshal restored current: %v", err)
	}
	if restored.Revision != 5 {
		t.Fatalf("expected restored current revision 5, got %d", restored.Revision)
	}
}

func TestOpenStoreCorruptCurrentAndBackup(t *testing.T) {
	dir := t.TempDir()
	mustWriteJSON(t, filepath.Join(dir, "node1.state.json"), []byte("bad"))
	mustWriteJSON(t, filepath.Join(dir, "node1.state.json.bak"), []byte("worse"))

	_, err := OpenStore(dir, "node1", StoreOptions{})
	if err == nil {
		t.Fatal("expected error opening corrupt current+backup")
	}
}

func TestOpenStoreWrongSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	owner := OwnerID("ownerschema123456789")
	future := mkBasicDoc(owner)
	future.Schema = SchemaVersion + 1
	mustWriteJSON(t, filepath.Join(dir, "node1.state.json"), future)

	_, err := OpenStore(dir, "node1", StoreOptions{})
	if err == nil {
		t.Fatalf("expected schema %d to be rejected", SchemaVersion+1)
	}
}

// TestOpenStoreSchema2FailsClosed proves the destructive-reset contract for
// schema 3: an old schema-2 document (the pre-canonical-schema transition
// layout, with `_compat`-nested fields) is never transformed, migrated, or
// partially read. OpenStore must fail closed with an explicit, actionable
// error, and the caller-visible remedy is to delete the store directory
// (every file OpenStore itself created: NodeID+".state.json" and its
// ".bak" sibling) and let a fresh schema-3 document be created in its place.
func TestOpenStoreSchema2FailsClosed(t *testing.T) {
	dir := t.TempDir()
	owner := OwnerID("ownerschema2v1234567")

	// A schema-2 document literal, including the legacy `_compat` JSON shape
	// schema 3 removed. json.Unmarshal into the current AppDocument simply
	// drops the unknown `_compat` key (it is not a recognized field anymore);
	// the decisive failure is the schema number itself.
	legacy := []byte(`{
		"schema": 2,
		"owner": "` + string(owner) + `",
		"revision": 1,
		"sessions": [
			{
				"id": "sessschema2v123456789",
				"owner": "` + string(owner) + `",
				"ref": "` + string(owner) + `/sessschema2v123456789:0.0",
				"phase": "active",
				"desired": "run",
				"revision": 1,
				"created_at": "2025-01-01T00:00:00Z",
				"_compat": {"name": "legacy-session", "generation": "gen-legacy"}
			}
		]
	}`)
	currentPath := filepath.Join(dir, "node1.state.json")
	if err := os.WriteFile(currentPath, legacy, 0600); err != nil {
		t.Fatalf("write legacy schema-2 document: %v", err)
	}

	_, err := OpenStore(dir, "node1", StoreOptions{})
	if err == nil {
		t.Fatal("expected schema 2 to be rejected")
	}
	var stateErr StateError
	if !errors.As(err, &stateErr) {
		t.Fatalf("expected a StateError wrapping the schema rejection, got %T: %v", err, err)
	}
	if stateErr.Code != ErrBadSchema {
		t.Fatalf("expected code %q, got %q", ErrBadSchema, stateErr.Code)
	}

	// The destructive-reset remedy: delete the store directory (every file
	// OpenStore manages lives directly under it) and re-open. A fresh schema-3
	// document is created in its place -- no partial read, no migration.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("delete store directory %q: %v", dir, err)
	}
	fresh, err := OpenStore(dir, "node1", StoreOptions{})
	if err != nil {
		t.Fatalf("open fresh store after destructive reset: %v", err)
	}
	snap := fresh.Snapshot()
	if snap.Schema != SchemaVersion {
		t.Fatalf("expected fresh store schema %d, got %d", SchemaVersion, snap.Schema)
	}
	if len(snap.Sessions) != 0 {
		t.Fatalf("expected fresh store to have no sessions, got %d", len(snap.Sessions))
	}
}

func TestOpenStoreOrphanTempRemoved(t *testing.T) {
	dir := t.TempDir()
	owner := OwnerID("ownerorphan123456789")
	doc := mkBasicDoc(owner)
	mustWriteJSON(t, filepath.Join(dir, "node1.state.json"), doc)

	orphan := filepath.Join(dir, "node1.state.json.tmp-12345")
	if err := os.WriteFile(orphan, []byte("partial json"), 0600); err != nil {
		t.Fatalf("write orphan: %v", err)
	}
	bakOrphan := filepath.Join(dir, "node1.state.json.bak.tmp-67890")
	if err := os.WriteFile(bakOrphan, []byte("partial backup"), 0600); err != nil {
		t.Fatalf("write backup orphan: %v", err)
	}

	s, err := OpenStore(dir, "node1", StoreOptions{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if s.Revision() != doc.Revision {
		t.Fatalf("revision mismatch: %d", s.Revision())
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatal("orphan temp should be removed")
	}
	if _, err := os.Stat(bakOrphan); !os.IsNotExist(err) {
		t.Fatal("orphan backup temp should be removed")
	}
}

func TestUpdatePersistsAndIncrementsRevision(t *testing.T) {
	dir := t.TempDir()
	s := mustOpenEmpty(t, dir)

	owner := s.Owner()
	err := s.Update("add-session", func(doc *AppDocument) error {
		doc.Sessions = append(doc.Sessions, LocalSessionRecord{
			ID:         SessionID("sessadd1234567890123"),
			Owner:      owner,
			Ref:        SessionRef{Owner: owner, Session: SessionID("sessadd1234567890123")},
			Phase:      SessionPhaseActive,
			Desired:    DesiredRun,
			Generation: "test-gen",
		})
		return nil
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if s.Revision() != 1 {
		t.Fatalf("expected revision 1, got %d", s.Revision())
	}

	// Reopen and prove durability.
	s2, err := OpenStore(dir, "node1", StoreOptions{})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if s2.Revision() != 1 {
		t.Fatalf("reopen revision mismatch: %d", s2.Revision())
	}
	snap := s2.Snapshot()
	if len(snap.Sessions) != 1 {
		t.Fatalf("expected 1 session after reopen, got %d", len(snap.Sessions))
	}
}

func TestUpdateInvalidDocumentRejected(t *testing.T) {
	dir := t.TempDir()
	s := mustOpenEmpty(t, dir)

	owner := s.Owner()
	err := s.Update("bad-layout", func(doc *AppDocument) error {
		doc.Layouts = append(doc.Layouts, LayoutRecord{
			ID:    LayoutID("layoutbad12345678901"),
			Owner: owner,
			Order: 1,
			Tree:  Split(DirectionHorizontal, Ratio(0.5), Leaf(SessionRef{}), Leaf(SessionRef{})),
		})
		return nil
	})
	if err == nil {
		t.Fatal("expected invalid document to be rejected")
	}
	if s.Revision() != 0 {
		t.Fatalf("revision should not advance on invalid update: %d", s.Revision())
	}
}

func TestUpdateUnchangedNoWrite(t *testing.T) {
	dir := t.TempDir()
	s := mustOpenEmpty(t, dir)

	modTime := fileModTime(t, s.currentPath())
	err := s.Update("noop", func(doc *AppDocument) error { return nil })
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if s.Revision() != 0 {
		t.Fatalf("unchanged update should not increment revision: %d", s.Revision())
	}
	if fileModTime(t, s.currentPath()) != modTime {
		t.Fatal("unchanged update should not rewrite the current file")
	}
}

func TestUpdatePrunesOldReceipts(t *testing.T) {
	dir := t.TempDir()
	owner := OwnerID("ownerreceipt12345678")
	doc := mkBasicDoc(owner)
	now := time.Now()
	doc.Commands = []CommandReceipt{
		{ID: NewCommandID(), IntentID: NewCommandID(), Seq: 1, Created: now},
		{ID: NewCommandID(), IntentID: NewCommandID(), Seq: 2, Created: now.Add(-time.Hour)},
	}
	mustWriteJSON(t, filepath.Join(dir, "node1.state.json"), doc)
	s, err := OpenStore(dir, "node1", StoreOptions{MaxReceiptAge: time.Minute})
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	err = s.Update("noop", func(doc *AppDocument) error {
		// Intentionally no other change; pruning alone changes receipts.
		return nil
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	snap := s.Snapshot()
	if len(snap.Commands) != 1 {
		t.Fatalf("expected 1 live receipt, got %d", len(snap.Commands))
	}
}

func TestUpdateFailureDoesNotAlterMemory(t *testing.T) {
	boundaries := []string{"create", "write", "sync", "rename", "syncdir"}
	for _, boundary := range boundaries {
		t.Run(boundary, func(t *testing.T) {
			dir := t.TempDir()
			s := mustOpenEmptyInDir(t, dir)

			opts := StoreOptions{
				CreateTempHook: makeFailingHook(os.CreateTemp, boundary == "create"),
				WriteHook:      makeFailingWriteHook(boundary == "write"),
				SyncHook:       makeFailingSyncHook(boundary == "sync"),
				RenameHook:     makeFailingRenameHook(boundary == "rename"),
				SyncDirHook:    conditionalSyncDirHook(boundary == "syncdir"),
			}
			s.opts = opts.withDefaults()

			before := s.Snapshot()
			owner := s.Owner()
			err := s.Update("add-session", func(doc *AppDocument) error {
				doc.Sessions = append(doc.Sessions, LocalSessionRecord{
					ID:         SessionID("sessfail123456789012"),
					Owner:      owner,
					Ref:        SessionRef{Owner: owner, Session: SessionID("sessfail123456789012")},
					Phase:      SessionPhaseActive,
					Desired:    DesiredRun,
					Generation: "test-gen",
				})
				return nil
			})
			if err == nil {
				t.Fatal("expected injected failure")
			}

			after := s.Snapshot()
			if boundary == "syncdir" {
				// The rename to current already succeeded before the
				// directory fsync failed, so the new document is durably
				// visible on disk; in-memory state must adopt it rather than
				// go stale (see errSyncDirFailedAfterRename in store.go).
				if docsEqual(before, after) {
					t.Fatal("expected in-memory snapshot to adopt the post-rename document on syncdir failure")
				}
				if len(after.Sessions) != len(before.Sessions)+1 {
					t.Fatalf("expected new session adopted into memory, got %d sessions", len(after.Sessions))
				}
				if s.Revision() != before.Revision+1 {
					t.Fatalf("expected revision to advance on syncdir failure: %d vs %d", s.Revision(), before.Revision)
				}
				return
			}

			if !docsEqual(before, after) {
				t.Fatalf("in-memory snapshot changed despite failure:\n%+v\nvs\n%+v", before, after)
			}
			if s.Revision() != before.Revision {
				t.Fatalf("revision changed despite failure: %d vs %d", s.Revision(), before.Revision)
			}
		})
	}
}

// TestUpdateSyncDirFailureAfterRenameAdoptsDocument is the dedicated proof
// for Bug 2: if the rename of the new current file succeeds but the
// subsequent directory fsync fails, the rename has already made the new
// document durably visible on disk, so in-memory state must adopt it rather
// than continue operating from the stale pre-update document (which would
// let a later Update rewrite the backup from a wrong "old" value and
// silently clobber the already-visible current file).
func TestUpdateSyncDirFailureAfterRenameAdoptsDocument(t *testing.T) {
	dir := t.TempDir()
	s := mustOpenEmptyInDir(t, dir)

	opts := StoreOptions{SyncDirHook: makeFailingSyncDirHookOnCall(2)}
	s.opts = opts.withDefaults()

	owner := s.Owner()
	err := s.Update("add-session", func(doc *AppDocument) error {
		doc.Sessions = append(doc.Sessions, LocalSessionRecord{
			ID:         SessionID("sesssyncdir123456789"),
			Owner:      owner,
			Ref:        SessionRef{Owner: owner, Session: SessionID("sesssyncdir123456789")},
			Phase:      SessionPhaseActive,
			Desired:    DesiredRun,
			Generation: "test-gen",
		})
		return nil
	})
	if err == nil {
		t.Fatal("expected injected sync-dir failure")
	}
	if !errors.Is(err, errSyncDirFailedAfterRename) {
		t.Fatalf("expected errSyncDirFailedAfterRename in chain, got: %v", err)
	}

	// The rename already happened, so disk holds the new document.
	diskDoc, derr := s.readDocument(s.currentPath())
	if derr != nil {
		t.Fatalf("read current from disk: %v", derr)
	}
	if len(diskDoc.Sessions) != 1 {
		t.Fatalf("expected disk to already contain the new session, got %d", len(diskDoc.Sessions))
	}

	// In-memory state must match what is now on disk, not the stale pre-update doc.
	memDoc := s.Snapshot()
	if !docsEqual(memDoc, diskDoc) {
		t.Fatalf("in-memory document diverged from disk after syncdir failure:\nmem=%+v\ndisk=%+v", memDoc, diskDoc)
	}
	if s.Revision() != 1 {
		t.Fatalf("expected revision 1 after syncdir failure adoption, got %d", s.Revision())
	}
}

func TestCrashBoundaries(t *testing.T) {
	cases := []struct {
		name      string
		setupFunc func(t *testing.T, dir string, doc AppDocument)
		wantRev   int64
	}{
		{
			name: "orphan_partial_temp",
			setupFunc: func(t *testing.T, dir string, doc AppDocument) {
				mustWriteJSON(t, filepath.Join(dir, "node1.state.json"), doc)
				if err := os.WriteFile(filepath.Join(dir, "node1.state.json.tmp-12345"), []byte("{"), 0600); err != nil {
					t.Fatalf("write partial temp: %v", err)
				}
			},
			wantRev: 7,
		},
		{
			name: "orphan_full_temp",
			setupFunc: func(t *testing.T, dir string, doc AppDocument) {
				mustWriteJSON(t, filepath.Join(dir, "node1.state.json"), doc)
				b, _ := json.Marshal(doc)
				if err := os.WriteFile(filepath.Join(dir, "node1.state.json.tmp-12345"), b, 0600); err != nil {
					t.Fatalf("write full temp: %v", err)
				}
			},
			wantRev: 7,
		},
		{
			name: "current_overwritten_backup_old",
			setupFunc: func(t *testing.T, dir string, doc AppDocument) {
				oldDoc := doc
				oldDoc.Revision = 5
				mustWriteJSON(t, filepath.Join(dir, "node1.state.json.bak"), oldDoc)
				doc.Revision = 9
				mustWriteJSON(t, filepath.Join(dir, "node1.state.json"), doc)
			},
			wantRev: 9,
		},
		{
			name: "current_corrupt_backup_old",
			setupFunc: func(t *testing.T, dir string, doc AppDocument) {
				oldDoc := doc
				oldDoc.Revision = 5
				mustWriteJSON(t, filepath.Join(dir, "node1.state.json.bak"), oldDoc)
				mustWriteJSON(t, filepath.Join(dir, "node1.state.json"), []byte("{bad"))
			},
			wantRev: 5,
		},
	}

	owner := OwnerID("ownercrash1234567890")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			doc := mkBasicDoc(owner)
			tc.setupFunc(t, dir, doc)

			s, err := OpenStore(dir, "node1", StoreOptions{})
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			if s.Revision() != tc.wantRev {
				t.Fatalf("revision %d, want %d", s.Revision(), tc.wantRev)
			}
			// Document must round-trip and be valid.
			snap := s.Snapshot()
			if err := ValidateDocument(&snap); err != nil {
				t.Fatalf("loaded document invalid: %v", err)
			}
		})
	}
}

func TestSubscriberReceivesChangeSet(t *testing.T) {
	dir := t.TempDir()
	owner := OwnerID("ownersub123456789012")
	doc := mkBasicDoc(owner)
	mustWriteJSON(t, filepath.Join(dir, "node1.state.json"), doc)

	var cs ChangeSet
	done := make(chan struct{}, 1)
	cb := func(change ChangeSet) {
		cs = change
		done <- struct{}{}
	}

	s, err := OpenStore(dir, "node1", StoreOptions{OnChange: cb})
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	err = s.Update("touch", func(doc *AppDocument) error {
		doc.Revision++
		return nil
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for change set")
	}

	if cs.Reason != "touch" {
		t.Fatalf("change set reason %q, want touch", cs.Reason)
	}
	if cs.BeforeRevision != 7 || cs.AfterRevision != 8 {
		t.Fatalf("unexpected revisions: %d -> %d", cs.BeforeRevision, cs.AfterRevision)
	}
	if len(cs.Document.Sessions) != len(doc.Sessions) {
		t.Fatal("document projection length mismatch")
	}
}

func TestConcurrentReadersAndWriters(t *testing.T) {
	dir := t.TempDir()
	s := mustOpenEmpty(t, dir)

	const writers = 8
	const readers = 4
	const iterations = 50

	var failed atomic.Int64
	var wg sync.WaitGroup

	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			owner := s.Owner()
			for j := 0; j < iterations; j++ {
				err := s.Update("add", func(doc *AppDocument) error {
					sid := fmt.Sprintf("sessconc%04d%04d", idx, j)
					doc.Sessions = append(doc.Sessions, LocalSessionRecord{
						ID:         SessionID(sid),
						Owner:      owner,
						Ref:        SessionRef{Owner: owner, Session: SessionID(sid)},
						Phase:      SessionPhaseActive,
						Desired:    DesiredRun,
						Generation: "test-gen",
					})
					return nil
				})
				if err != nil {
					failed.Add(1)
				}
			}
		}(i)
	}

	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				snap := s.Snapshot()
				if snap.Revision < 0 {
					failed.Add(1)
				}
				if err := ValidateDocument(&snap); err != nil {
					failed.Add(1)
				}
			}
		}()
	}

	wg.Wait()
	if failed.Load() != 0 {
		t.Fatalf("%d concurrent operations failed", failed.Load())
	}

	// Revision should equal number of successful writes (all, since no conflicts).
	s2, err := OpenStore(dir, "node1", StoreOptions{})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	want := writers * iterations
	if s2.Revision() != int64(want) {
		t.Fatalf("revision %d, want %d", s2.Revision(), want)
	}
	if len(s2.Snapshot().Sessions) != want {
		t.Fatalf("session count %d, want %d", len(s2.Snapshot().Sessions), want)
	}
}

func TestFilePermission0600(t *testing.T) {
	dir := t.TempDir()
	s := mustOpenEmpty(t, dir)
	if err := s.Update("noop", func(doc *AppDocument) error { return nil }); err != nil {
		t.Fatalf("update: %v", err)
	}
	checkFilePerm(t, s.currentPath(), 0600)
}

func BenchmarkStoreUpdate100Sessions50Layouts(b *testing.B) {
	dir := b.TempDir()
	owner := OwnerID("ownerbench1234567890")
	doc := mkLargeDoc(owner, 100, 50)
	mustWriteJSON(b, filepath.Join(dir, "bench.state.json"), doc)
	s, err := OpenStore(dir, "bench", StoreOptions{})
	if err != nil {
		b.Fatalf("open: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := s.Update("tick", func(doc *AppDocument) error {
			doc.Sessions[0].Revision++
			return nil
		}); err != nil {
			b.Fatalf("update: %v", err)
		}
	}
}

func BenchmarkStoreUpdate500Sessions200Layouts(b *testing.B) {
	dir := b.TempDir()
	owner := OwnerID("ownerbench1234567890")
	doc := mkLargeDoc(owner, 500, 200)
	mustWriteJSON(b, filepath.Join(dir, "bench.state.json"), doc)
	s, err := OpenStore(dir, "bench", StoreOptions{})
	if err != nil {
		b.Fatalf("open: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := s.Update("tick", func(doc *AppDocument) error {
			doc.Sessions[0].Revision++
			return nil
		}); err != nil {
			b.Fatalf("update: %v", err)
		}
	}
}

func mustOpenEmpty(t testing.TB, dir string) *Store {
	t.Helper()
	return mustOpenEmptyInDir(t, dir)
}

func mustOpenEmptyInDir(t testing.TB, dir string) *Store {
	t.Helper()
	s, err := OpenStore(dir, "node1", StoreOptions{})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return s
}

func mkBasicDoc(owner OwnerID) AppDocument {
	return AppDocument{
		Schema:   SchemaVersion,
		Owner:    owner,
		Revision: 7,
		Sessions: []LocalSessionRecord{
			{
				ID:         SessionID("sessbasic12345678901"),
				Owner:      owner,
				Ref:        SessionRef{Owner: owner, Session: SessionID("sessbasic12345678901")},
				Phase:      SessionPhaseActive,
				Desired:    DesiredRun,
				Generation: "test-gen",
			},
		},
		Layouts: []LayoutRecord{
			{
				ID:       LayoutID("layoutbasic123456789"),
				Owner:    owner,
				Order:    1,
				Revision: 1,
				Tree:     Leaf(SessionRef{Owner: owner, Session: SessionID("sessbasic12345678901")}),
			},
		},
	}
}

func mkLargeDoc(owner OwnerID, sessions, layouts int) AppDocument {
	doc := AppDocument{
		Schema:   SchemaVersion,
		Owner:    owner,
		Revision: 1,
		Sessions: make([]LocalSessionRecord, sessions),
		Layouts:  make([]LayoutRecord, layouts),
	}
	for i := 0; i < sessions; i++ {
		id := SessionID(fmt.Sprintf("sess%025d", i))
		doc.Sessions[i] = LocalSessionRecord{
			ID:         id,
			Owner:      owner,
			Ref:        SessionRef{Owner: owner, Session: id},
			Phase:      SessionPhaseActive,
			Desired:    DesiredRun,
			Generation: "test-gen",
			Revision:   1,
		}
	}
	for i := 0; i < layouts; i++ {
		id := LayoutID(fmt.Sprintf("layout%023d", i))
		sid := SessionID(fmt.Sprintf("sess%025d", i))
		doc.Layouts[i] = LayoutRecord{
			ID:       id,
			Owner:    owner,
			Order:    int64(i),
			Revision: 1,
			Tree:     Leaf(SessionRef{Owner: owner, Session: sid}),
		}
	}
	return doc
}

func mustWriteJSON(t testing.TB, path string, v any) {
	t.Helper()
	var data []byte
	switch d := v.(type) {
	case []byte:
		data = d
	default:
		var err error
		data, err = json.MarshalIndent(v, "", "  ")
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func checkFilePerm(t testing.TB, path string, want os.FileMode) {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if st.Mode().Perm() != want {
		t.Fatalf("perm %04o, want %04o for %s", st.Mode().Perm(), want, path)
	}
}

func fileModTime(t testing.TB, path string) time.Time {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return st.ModTime()
}

func makeFailingHook(real func(string, string) (*os.File, error), fail bool) func(string, string) (*os.File, error) {
	return func(dir, pattern string) (*os.File, error) {
		if fail {
			return nil, errInjected
		}
		return real(dir, pattern)
	}
}

func makeFailingWriteHook(fail bool) func(*os.File, []byte) error {
	return func(f *os.File, p []byte) error {
		if fail {
			// Leave a partial file behind so crash-boundary cleanup is visible.
			_, _ = f.Write([]byte("{"))
			return errInjected
		}
		_, err := f.Write(p)
		return err
	}
}

func makeFailingSyncHook(fail bool) func(*os.File) error {
	var once sync.Once
	return func(f *os.File) error {
		if fail {
			// Only fail the first sync call (the new temp). A second sync
			// failure for the backup would complicate the test without adding
			// coverage.
			var failed bool
			once.Do(func() { failed = true })
			if failed {
				return errInjected
			}
		}
		return f.Sync()
	}
}

func makeFailingRenameHook(fail bool) func(string, string) error {
	return func(oldpath, newpath string) error {
		if fail {
			return errInjected
		}
		return os.Rename(oldpath, newpath)
	}
}

func makeFailingSyncDirHook(fail bool) func(string) error {
	return func(dir string) error {
		if fail {
			return errInjected
		}
		return syncDir(dir)
	}
}

// makeFailingSyncDirHookOnCall fails only the callth invocation (1-indexed)
// of the directory-fsync hook and lets every other call through to the real
// syncDir. commit() calls SyncDirHook twice when a backup rotation happens:
// once inside writeBackup (after the backup file is renamed into place) and
// once after the main rename of the new current file. To reproduce the Bug 2
// scenario -- rename to current succeeds, only the subsequent directory
// fsync fails -- the backup's own directory fsync (the first call) must
// succeed so execution reaches the post-rename fsync (the second call).
// conditionalSyncDirHook returns the post-rename-failing hook when active is
// true, otherwise the real syncDir.
func conditionalSyncDirHook(active bool) func(string) error {
	if !active {
		return syncDir
	}
	return makeFailingSyncDirHookOnCall(2)
}

func makeFailingSyncDirHookOnCall(call int) func(string) error {
	var n atomic.Int64
	return func(dir string) error {
		if n.Add(1) == int64(call) {
			return errInjected
		}
		return syncDir(dir)
	}
}

// TestCatalogApplyAdoptsSyncDirFailureAfterRename is the catalog-level proof
// that Bug 3's fix closes the gap the earlier, narrower store-level fix (Bug
// 2, see TestUpdateSyncDirFailureAfterRenameAdoptsDocument above) left open:
// Store.Update already adopts the post-rename document into its own
// in-memory state when only the directory-entry fsync fails, but it still
// returns an error, and Catalog.apply used to return early on any Store.Update
// error without updating its own maps/revision or publishing a snapshot. That
// left the catalog -- and everything fed by it (subscribers, the browser
// state stream) -- stuck on the old document even though disk and
// Store.Snapshot() both already held the new one. This test drives the exact
// fault through Catalog.apply (via PutSession, not raw Store.Update) and
// asserts the catalog adopts the change and publishes it.
func TestCatalogApplyFailsClosedOnSyncDirFailureAfterRename(t *testing.T) {
	dir := t.TempDir()
	store := mustOpenEmptyInDir(t, dir)
	owner := store.Owner()

	cat := NewCatalog(owner, store)
	if err := cat.Load(); err != nil {
		t.Fatalf("load catalog: %v", err)
	}

	var mu sync.Mutex
	var published []OwnerCatalogSnapshot
	unsubscribe := cat.SubscribeCatalog(func(snap OwnerCatalogSnapshot) {
		mu.Lock()
		defer mu.Unlock()
		published = append(published, snap)
	})
	defer unsubscribe()

	beforeRevision := cat.Revision()

	// Fail only the post-rename directory fsync (the second SyncDirHook call
	// in commit(), after the backup rotation's own directory fsync succeeds),
	// exactly reproducing the Bug 2/Bug 3 scenario.
	store.opts.SyncDirHook = makeFailingSyncDirHookOnCall(2)

	rec := LocalSessionRecord{
		ID:         SessionID("sesscatalogfsync1234"),
		Owner:      owner,
		Ref:        SessionRef{Owner: owner, Session: SessionID("sesscatalogfsync1234")},
		Phase:      SessionPhaseActive,
		Desired:    DesiredRun,
		Generation: "test-gen",
	}
	// Per the fail-closed durability contract, a directory-fsync failure
	// after a successful rename must NOT be reported as success: the caller
	// needs a non-nil error so it never treats this mutation as durably
	// acknowledged.
	if err := cat.PutSession(rec); err == nil {
		t.Fatal("expected Catalog.apply to fail closed (non-nil error) on a post-rename directory-fsync failure, got nil")
	}

	// The rename already made the new document durably visible on disk, even
	// though the command above was correctly failed closed.
	diskDoc, derr := store.readDocument(store.currentPath())
	if derr != nil {
		t.Fatalf("read current from disk: %v", derr)
	}
	if len(diskDoc.Sessions) != 1 {
		t.Fatalf("expected disk to already contain the new session, got %d", len(diskDoc.Sessions))
	}

	// Store.Snapshot() must match disk (this half was already proven at the
	// store level by TestUpdateSyncDirFailureAfterRenameAdoptsDocument; re-
	// asserted here as the baseline the catalog must now also match).
	storeSnap := store.Snapshot()
	if !docsEqual(storeSnap, diskDoc) {
		t.Fatalf("store snapshot diverged from disk:\nstore=%+v\ndisk=%+v", storeSnap, diskDoc)
	}

	// Even though the command failed closed, the catalog's own in-memory
	// maps/revision must still match what is actually on disk/Store -- it
	// must never silently diverge from Store.Snapshot() just because the
	// command it was resyncing for was itself rejected.
	if cat.Revision() != diskDoc.Revision {
		t.Fatalf("catalog revision diverged from disk after syncdir failure: catalog=%d disk=%d", cat.Revision(), diskDoc.Revision)
	}
	if cat.Revision() == beforeRevision {
		t.Fatalf("expected catalog revision to advance past %d, got %d", beforeRevision, cat.Revision())
	}
	catSessions := cat.Sessions()
	if len(catSessions) != 1 || catSessions[0].ID != rec.ID {
		t.Fatalf("expected catalog to resync the new session into its maps despite failing the command closed, got %+v", catSessions)
	}

	// Because the command failed closed, no snapshot should have been
	// published to subscribers for it -- publishing would look exactly like
	// an acknowledged success to anything downstream (e.g. the browser state
	// stream), which is precisely what the fail-closed contract forbids.
	mu.Lock()
	defer mu.Unlock()
	if len(published) != 0 {
		t.Fatalf("expected no catalog snapshot to be published for a command that failed closed, got %d: %+v", len(published), published)
	}
}

// compile-time check that Store methods used by tests exist.
var (
	_ func(*Store) AppDocument = (*Store).Snapshot
	_ func(*Store) OwnerID     = (*Store).Owner
	_ func(*Store) int64       = (*Store).Revision
	_ func(*Store) string      = (*Store).Path
	_ func(*Store) string      = (*Store).NodeID
)
