package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// StoreOptions configures a Store. A nil option selects the production default.
type StoreOptions struct {
	// Owner is persisted as the document owner when a new document is created.
	// If empty, a fresh OwnerID is generated.
	Owner OwnerID

	// MaxReceiptAge bounds how old a command receipt may be. Defaults to MaxCommandReceiptAge.
	MaxReceiptAge time.Duration

	// MaxReceiptCount bounds how many receipts are kept. Defaults to MaxPendingCommands.
	MaxReceiptCount int

	// OnChange is called after each successful durable commit. It receives a
	// deep clone of the committed document; the store does not wait for it.
	OnChange func(ChangeSet)

	// Authority labels the writer for logging and change-set causality.
	Authority string

	// Test hooks. nil means use the real filesystem operation.
	CreateTempHook func(dir, pattern string) (*os.File, error)
	WriteHook      func(f *os.File, p []byte) error
	SyncHook       func(f *os.File) error
	RenameHook     func(oldpath, newpath string) error
	SyncDirHook    func(dir string) error
}

const storeAuthorityLocal = "local"

// ChangeSet describes one committed change. Document is a deep clone and is
// safe for subscribers to retain or project from.
type ChangeSet struct {
	Reason              string
	Authority           string
	BeforeRevision      int64
	AfterRevision       int64
	Document            AppDocument
	RecoveredFromBackup bool
}

// Store is a crash-safe atomic app-state store.
//
// It persists exactly one versioned JSON document per node. Writes are atomic
// (temp file, fsync, rename, directory fsync) and keep a last-good backup.
// The store assumes one writer process per path; it serializes in-process
// writers with a mutex.
type Store struct {
	mu     sync.RWMutex
	path   string
	nodeID string
	opts   StoreOptions
	doc    AppDocument
}

// OpenStore opens or creates a node store in path using nodeID as the filename stem.
func OpenStore(path, nodeID string, opts StoreOptions) (*Store, error) {
	opts = opts.withDefaults()
	if path == "" {
		return nil, errors.New("store path is empty")
	}
	if nodeID == "" {
		return nil, errors.New("store nodeID is empty")
	}

	s := &Store{
		path:   path,
		nodeID: nodeID,
		opts:   opts,
	}

	if err := os.MkdirAll(path, 0700); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}

	s.removeOrphanTemps()

	doc, recovered, err := s.loadBestDocument()
	if err != nil {
		return nil, err
	}

	s.doc = doc

	// Persist an initial empty document so there is always a current file.
	if _, err := os.Stat(s.currentPath()); os.IsNotExist(err) {
		if err := s.commit(&s.doc); err != nil {
			return nil, fmt.Errorf("persist initial document: %w", err)
		}
	}

	size, _ := fileSize(s.currentPath())
	logrus.WithFields(logrus.Fields{
		"path":             path,
		"node_id":          nodeID,
		"schema":           s.doc.Schema,
		"file_size":        size,
		"revision":         s.doc.Revision,
		"backup_recovered": recovered,
		"migration_needed": false,
		"authority":        opts.Authority,
	}).Info("opened v2 state store")

	return s, nil
}

// Snapshot returns an immutable deep clone of the current document.
func (s *Store) Snapshot() AppDocument {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snap, err := cloneDoc(&s.doc)
	if err != nil {
		// cloneDoc only fails on corrupt in-memory state, which cannot happen
		// because every mutation validates the document before accepting it.
		return AppDocument{}
	}
	return snap
}

// Update applies mutate to a deep clone of the current document, validates it,
// prunes stale command receipts, persists it atomically, and publishes a
// ChangeSet. If mutate returns an error, or the resulting document is invalid,
// nothing is persisted and the in-memory snapshot is unchanged.
//
// An unchanged mutation (deep-equal result) is a no-op: no file is written and
// no revision is incremented.
func (s *Store) Update(reason string, mutate func(*AppDocument) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	proposed, err := cloneDoc(&s.doc)
	if err != nil {
		return fmt.Errorf("clone current document: %w", err)
	}

	if err := mutate(&proposed); err != nil {
		return fmt.Errorf("mutation %q rejected: %w", reason, err)
	}

	pruneReceipts(&proposed, s.opts.MaxReceiptAge, s.opts.MaxReceiptCount)

	if err := ValidateDocument(&proposed); err != nil {
		return fmt.Errorf("invalid document after %q: %w", reason, err)
	}
	if err := CheckSessionMembershipAcrossLayouts(&proposed); err != nil {
		return fmt.Errorf("layout membership conflict after %q: %w", reason, err)
	}

	if docsEqual(s.doc, proposed) {
		return nil
	}

	before := s.doc.Revision

	// Authority-specific revision: for the local owner the persisted catalog
	// revision must advance monotonically. Remote/cache updates are not
	// persisted through this path.
	if s.opts.Authority == storeAuthorityLocal {
		if proposed.Revision <= before {
			proposed.Revision = before + 1
		}
	}

	if err := s.commit(&proposed); err != nil {
		return fmt.Errorf("commit %q failed: %w", reason, err)
	}

	s.doc = proposed

	if s.opts.OnChange != nil {
		snapshot, _ := cloneDoc(&s.doc)
		go s.opts.OnChange(ChangeSet{
			Reason:         reason,
			Authority:      s.opts.Authority,
			BeforeRevision: before,
			AfterRevision:  s.doc.Revision,
			Document:       snapshot,
		})
	}

	return nil
}

// Path returns the directory that holds the store files.
func (s *Store) Path() string { return s.path }

// NodeID returns the filename stem used for store files.
func (s *Store) NodeID() string { return s.nodeID }

// Owner returns the document owner.
func (s *Store) Owner() OwnerID {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.doc.Owner
}

// Revision returns the current persisted catalog revision.
func (s *Store) Revision() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.doc.Revision
}

func (opts StoreOptions) withDefaults() StoreOptions {
	if opts.MaxReceiptAge <= 0 {
		opts.MaxReceiptAge = MaxCommandReceiptAge
	}
	if opts.MaxReceiptCount <= 0 {
		opts.MaxReceiptCount = MaxPendingCommands
	}
	if opts.Authority == "" {
		opts.Authority = storeAuthorityLocal
	}
	if opts.CreateTempHook == nil {
		opts.CreateTempHook = os.CreateTemp
	}
	if opts.WriteHook == nil {
		opts.WriteHook = func(f *os.File, p []byte) error {
			_, err := f.Write(p)
			return err
		}
	}
	if opts.SyncHook == nil {
		opts.SyncHook = func(f *os.File) error { return f.Sync() }
	}
	if opts.RenameHook == nil {
		opts.RenameHook = os.Rename
	}
	if opts.SyncDirHook == nil {
		opts.SyncDirHook = syncDir
	}
	return opts
}

func (s *Store) currentPath() string {
	return filepath.Join(s.path, s.nodeID+".state.json")
}

func (s *Store) backupPath() string {
	return filepath.Join(s.path, s.nodeID+".state.json.bak")
}

func (s *Store) tempPattern() string {
	return s.nodeID + ".state.json.tmp-*"
}

func (s *Store) removeOrphanTemps() {
	entries, err := os.ReadDir(s.path)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, s.nodeID+".state.json.tmp-") ||
			strings.HasPrefix(name, s.nodeID+".state.json.bak.tmp-") {
			_ = os.Remove(filepath.Join(s.path, name))
		}
	}
}

// loadBestDocument returns the best available valid document. It prefers the
// current file, then the backup. If the current file is corrupt or an unknown
// schema, it is restored from the backup on success.
func (s *Store) loadBestDocument() (AppDocument, bool, error) {
	current := s.currentPath()
	backup := s.backupPath()

	curDoc, curErr := s.readDocument(current)
	if curErr == nil {
		if err := ValidateDocument(&curDoc); err == nil {
			if err := CheckSessionMembershipAcrossLayouts(&curDoc); err == nil {
				return curDoc, false, nil
			}
		}
	}

	bakDoc, bakErr := s.readDocument(backup)
	if bakErr == nil {
		if err := ValidateDocument(&bakDoc); err == nil {
			if err := CheckSessionMembershipAcrossLayouts(&bakDoc); err == nil {
				if curErr == nil {
					logrus.WithFields(logrus.Fields{
						"path":   current,
						"backup": backup,
					}).Warn("current state corrupt or incompatible; recovered from backup")
				}
				// Restore current from backup so the next write starts clean.
				if err := s.restoreCurrent(&bakDoc); err == nil {
					return bakDoc, true, nil
				}
				// The backup is usable even if we cannot rewrite current.
				return bakDoc, true, nil
			}
		}
	}

	if _, statErr := os.Stat(current); statErr == nil {
		return AppDocument{}, false, fmt.Errorf("current state at %q unusable: %w; backup also unusable: %w", current, curErr, bakErr)
	}

	return s.newDocument(), false, nil
}

func (s *Store) newDocument() AppDocument {
	owner := s.opts.Owner
	if owner == "" {
		owner = NewOwnerID()
	}
	return AppDocument{
		Schema:   SchemaVersion,
		Owner:    owner,
		Revision: 0,
		Sessions: []LocalSessionRecord{},
		Layouts:  []LayoutRecord{},
	}
}

func (s *Store) readDocument(path string) (AppDocument, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return AppDocument{}, err
	}
	var doc AppDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return AppDocument{}, fmt.Errorf("decode %q: %w", path, err)
	}
	return doc, nil
}

// restoreCurrent writes the given document to the current path using the same
// atomic temp/rename path as a normal commit.
func (s *Store) restoreCurrent(doc *AppDocument) error {
	return s.commit(doc)
}

func cloneDoc(doc *AppDocument) (AppDocument, error) {
	data, err := json.Marshal(doc)
	if err != nil {
		return AppDocument{}, err
	}
	var clone AppDocument
	if err := json.Unmarshal(data, &clone); err != nil {
		return AppDocument{}, err
	}
	return clone, nil
}

func docsEqual(a, b AppDocument) bool {
	aJSON, err := json.Marshal(a)
	if err != nil {
		return false
	}
	bJSON, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return string(aJSON) == string(bJSON)
}

// pruneReceipts removes expired receipts and caps the live receipt count,
// keeping the newest receipts.
func pruneReceipts(doc *AppDocument, maxAge time.Duration, maxCount int) {
	now := time.Now()
	live := doc.Commands[:0]
	for _, r := range doc.Commands {
		if now.Sub(r.Created) <= maxAge {
			live = append(live, r)
		}
	}

	if len(live) > maxCount {
		sort.Slice(live, func(i, j int) bool {
			return live[i].Created.After(live[j].Created)
		})
		live = live[:maxCount]
	}

	doc.Commands = live
}

// commit persists newDoc atomically and updates the last-good backup.
func (s *Store) commit(newDoc *AppDocument) error {
	newBytes, err := json.Marshal(newDoc)
	if err != nil {
		return fmt.Errorf("marshal document: %w", err)
	}

	tmpPath, err := s.writeTemp(s.tempPattern(), newBytes)
	if err != nil {
		// s.writeTemp cleans up its own temp on failure.
		return fmt.Errorf("write temp: %w", err)
	}

	// If a current file exists, copy the in-memory old document to the backup.
	// The old document is the last-good state, so this cannot corrupt the backup.
	if _, statErr := os.Stat(s.currentPath()); statErr == nil {
		oldBytes, err := json.Marshal(&s.doc)
		if err != nil {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("marshal backup: %w", err)
		}
		if err := s.writeBackup(oldBytes); err != nil {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("write backup: %w", err)
		}
	}

	if err := s.renameHook(tmpPath, s.currentPath()); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename temp to current: %w", err)
	}

	if err := s.syncDirHook(s.path); err != nil {
		return fmt.Errorf("sync directory: %w", err)
	}
	return nil
}

func (s *Store) writeTemp(pattern string, data []byte) (string, error) {
	f, err := s.createTempHook(s.path, pattern)
	if err != nil {
		return "", fmt.Errorf("create temp: %w", err)
	}
	tmpPath := f.Name()
	cleanup := func() { _ = f.Close(); _ = os.Remove(tmpPath) }

	if err := s.writeHook(f, data); err != nil {
		cleanup()
		return "", fmt.Errorf("write temp: %w", err)
	}
	if err := s.syncHook(f); err != nil {
		cleanup()
		return "", fmt.Errorf("sync temp: %w", err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", fmt.Errorf("close temp: %w", err)
	}
	return tmpPath, nil
}

func (s *Store) writeBackup(data []byte) error {
	bakTmpPath, err := s.writeTemp(s.nodeID+".state.json.bak.tmp-*", data)
	if err != nil {
		return err
	}
	if err := s.renameHook(bakTmpPath, s.backupPath()); err != nil {
		_ = os.Remove(bakTmpPath)
		return fmt.Errorf("rename backup temp: %w", err)
	}
	if err := s.syncDirHook(s.path); err != nil {
		return fmt.Errorf("sync directory after backup: %w", err)
	}
	return nil
}

func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func fileSize(path string) (int64, error) {
	st, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

// storeHook wrappers exposed for tests so the Store can be compiled with both
// real and injected backends.
func (s *Store) createTempHook(dir, pattern string) (*os.File, error) {
	return s.opts.CreateTempHook(dir, pattern)
}

func (s *Store) writeHook(f *os.File, p []byte) error {
	return s.opts.WriteHook(f, p)
}

func (s *Store) syncHook(f *os.File) error {
	return s.opts.SyncHook(f)
}

func (s *Store) renameHook(oldpath, newpath string) error {
	return s.opts.RenameHook(oldpath, newpath)
}

func (s *Store) syncDirHook(dir string) error {
	return s.opts.SyncDirHook(dir)
}
