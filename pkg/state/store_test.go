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
	future.Schema = 3
	mustWriteJSON(t, filepath.Join(dir, "node1.state.json"), future)

	_, err := OpenStore(dir, "node1", StoreOptions{})
	if err == nil {
		t.Fatal("expected schema 3 to be rejected")
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
			ID:      SessionID("sessadd1234567890123"),
			Owner:   owner,
			Ref:     SessionRef{Owner: owner, Session: SessionID("sessadd1234567890123")},
			Phase:   SessionPhaseActive,
			Desired: DesiredRun,
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
				SyncDirHook:    makeFailingSyncDirHook(boundary == "syncdir"),
			}
			s.opts = opts.withDefaults()

			before := s.Snapshot()
			owner := s.Owner()
			err := s.Update("add-session", func(doc *AppDocument) error {
				doc.Sessions = append(doc.Sessions, LocalSessionRecord{
					ID:      SessionID("sessfail123456789012"),
					Owner:   owner,
					Ref:     SessionRef{Owner: owner, Session: SessionID("sessfail123456789012")},
					Phase:   SessionPhaseActive,
					Desired: DesiredRun,
				})
				return nil
			})
			if err == nil {
				t.Fatal("expected injected failure")
			}

			after := s.Snapshot()
			if !docsEqual(before, after) {
				t.Fatalf("in-memory snapshot changed despite failure:\n%+v\nvs\n%+v", before, after)
			}
			if s.Revision() != before.Revision {
				t.Fatalf("revision changed despite failure: %d vs %d", s.Revision(), before.Revision)
			}
		})
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
						ID:      SessionID(sid),
						Owner:   owner,
						Ref:     SessionRef{Owner: owner, Session: SessionID(sid)},
						Phase:   SessionPhaseActive,
						Desired: DesiredRun,
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
				ID:      SessionID("sessbasic12345678901"),
				Owner:   owner,
				Ref:     SessionRef{Owner: owner, Session: SessionID("sessbasic12345678901")},
				Phase:   SessionPhaseActive,
				Desired: DesiredRun,
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
			ID:       id,
			Owner:    owner,
			Ref:      SessionRef{Owner: owner, Session: id},
			Phase:    SessionPhaseActive,
			Desired:  DesiredRun,
			Revision: 1,
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

// compile-time check that Store methods used by tests exist.
var (
	_ func(*Store) AppDocument = (*Store).Snapshot
	_ func(*Store) OwnerID     = (*Store).Owner
	_ func(*Store) int64       = (*Store).Revision
	_ func(*Store) string      = (*Store).Path
	_ func(*Store) string      = (*Store).NodeID
)
