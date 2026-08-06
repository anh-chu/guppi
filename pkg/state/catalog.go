package state

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// Catalog is the local, owner-authoritative session/workspace catalog. It
// keeps a deterministic in-memory view of the persisted AppDocument and
// applies mutations through the atomic store when one is configured.
//
// All public getters return value copies so callers cannot stamp the internal
// records.
type Catalog struct {
	mu                 sync.RWMutex
	owner              OwnerID
	revision           int64
	sessions           map[SessionID]LocalSessionRecord
	layouts            map[LayoutID]LayoutRecord
	pending            map[CommandID]PendingCreateRecord
	remotePending      map[CommandID]PendingRemoteCreateRecord
	workspaceSubs      []workspaceSubscription
	nextWorkspaceSubID int
	catalogSubs        []catalogSubscription
	nextCatalogSubID   int
	store              *Store

	// activeKey stores the selected leaf ref per layout. It is purely
	// in-memory and intentionally not persisted.
	activeKeys map[LayoutID]*SessionRef

	// commands mirrors the last known receipts so in-memory catalogs can
	// participate in the bounded receipt mechanism.
	commands []CommandReceipt

	// presentations is an in-memory map keyed by layout ID. Previews and
	// selection state live here and are intentionally never persisted.
	presentations map[LayoutID]map[string]PresentationRecord
}

// NewCatalog creates an empty catalog for owner. If store is non-nil, the
// catalog loads from and persists through it.
func NewCatalog(owner OwnerID, store *Store) *Catalog {
	if owner == "" {
		owner = NewOwnerID()
	}
	return &Catalog{
		owner:         owner,
		sessions:      make(map[SessionID]LocalSessionRecord),
		layouts:       make(map[LayoutID]LayoutRecord),
		pending:       make(map[CommandID]PendingCreateRecord),
		remotePending: make(map[CommandID]PendingRemoteCreateRecord),
		store:         store,
	}
}

// Load reads the latest persisted document into the catalog. If no store is
// configured it is a no-op.
func (c *Catalog) Load() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	var doc AppDocument
	if c.store != nil {
		doc = c.store.Snapshot()
	} else {
		doc = c.emptyDoc()
	}
	return c.resetLocked(doc)
}

// Owner returns the catalog owner.
func (c *Catalog) Owner() OwnerID {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.owner
}

// Revision returns the current catalog revision.
func (c *Catalog) Revision() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.revision
}

// Session returns a copy of one session record.
func (c *Catalog) Session(id SessionID) (LocalSessionRecord, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	s, ok := c.sessions[id]
	return s, ok
}

// Sessions returns a sorted copy of all session records.
func (c *Catalog) Sessions() []LocalSessionRecord {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sortedSessionsLocked()
}

// SessionsByScheduleID returns every session record tagged with scheduleID
// (LocalSessionRecord.ScheduleID), ordered oldest-Created-first. It is the
// canonical, SessionRef-keyed replacement for the legacy display-name-keyed
// sessionattrs lookup: callers enforcing a schedule's MaxConcurrency use this
// to find the oldest excess sessions and kill them by stable SessionRef,
// never by display name.
func (c *Catalog) SessionsByScheduleID(scheduleID string) []LocalSessionRecord {
	if scheduleID == "" {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []LocalSessionRecord
	for _, s := range c.sessions {
		if s.ScheduleID == scheduleID {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Created.Equal(out[j].Created) {
			return out[i].Created.Before(out[j].Created)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Layout returns a copy of one layout record.
func (c *Catalog) Layout(id LayoutID) (LayoutRecord, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	l, ok := c.layouts[id]
	return l, ok
}

// Layouts returns a sorted copy of all layout records.
func (c *Catalog) Layouts() []LayoutRecord {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sortedLayoutsLocked()
}

// PendingCreates returns a copy of unresolved create intents.
func (c *Catalog) PendingCreates() []PendingCreateRecord {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]PendingCreateRecord, 0, len(c.pending))
	for _, p := range c.pending {
		out = append(out, p)
	}
	return out
}

// CommandReceipt returns a copy of the durable receipt for id, if one is
// currently live (not yet pruned by MaxCommandReceiptAge/MaxPendingCommands).
// Callers use this to check for an already-accepted command BEFORE
// attempting any side effect, so a retried request returns the exact
// original outcome instead of redoing work or re-deriving a possibly
// different answer from current state.
//
// Before returning, this re-validates the store's durability state
// (Store.Revalidate). A receipt written during a window where a prior
// write's durability was uncertain (see Store.durabilityUncertain) must
// not be handed back as an accepted result on replay -- doing so would
// silently upgrade an unconfirmed write to acknowledged success. Callers
// must treat a non-nil error as fail-closed: do not treat the receipt as
// present or accepted.
func (c *Catalog) CommandReceipt(id CommandID) (CommandReceipt, bool, error) {
	if c.store != nil {
		if err := c.store.Revalidate(); err != nil {
			return CommandReceipt{}, false, err
		}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, r := range c.commands {
		if r.ID == id {
			return r, true, nil
		}
	}
	return CommandReceipt{}, false, nil
}

// PutSession stores or replaces a session record.
func (c *Catalog) PutSession(rec LocalSessionRecord) error {
	if err := rec.ID.Validate(); err != nil {
		return err
	}
	if rec.Owner != c.Owner() {
		return fmt.Errorf("session owner %q does not match catalog owner %q", rec.Owner, c.Owner())
	}
	return c.apply("put-session", func(doc *AppDocument) error {
		return c.upsertSessionLocked(doc, rec)
	})
}

// RemoveSession deletes a session record.
func (c *Catalog) RemoveSession(id SessionID) error {
	return c.apply("remove-session", func(doc *AppDocument) error {
		return c.removeSessionLocked(doc, id)
	})
}

// PutLayout stores or replaces a layout record.
func (c *Catalog) PutLayout(rec LayoutRecord) error {
	if err := rec.ID.Validate(); err != nil {
		return err
	}
	if rec.Owner != c.Owner() {
		return fmt.Errorf("layout owner %q does not match catalog owner %q", rec.Owner, c.Owner())
	}
	return c.apply("put-layout", func(doc *AppDocument) error {
		return c.upsertLayoutLocked(doc, rec)
	})
}

// RemoveLayout deletes a layout record.
func (c *Catalog) RemoveLayout(id LayoutID) error {
	return c.apply("remove-layout", func(doc *AppDocument) error {
		return c.removeLayoutLocked(doc, id)
	})
}

// PutPendingCreate stores an unresolved create intent.
func (c *Catalog) PutPendingCreate(rec PendingCreateRecord) error {
	if err := rec.IntentID.Validate(); err != nil {
		return err
	}
	return c.apply("put-pending-create", func(doc *AppDocument) error {
		return c.upsertPendingLocked(doc, rec)
	})
}

// RemovePendingCreate deletes a pending create intent.
func (c *Catalog) RemovePendingCreate(id CommandID) error {
	return c.apply("remove-pending-create", func(doc *AppDocument) error {
		return c.removePendingLocked(doc, id)
	})
}

// PendingRemoteCreates returns a copy of unresolved remote create intents.
func (c *Catalog) PendingRemoteCreates() []PendingRemoteCreateRecord {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]PendingRemoteCreateRecord, 0, len(c.remotePending))
	for _, p := range c.remotePending {
		out = append(out, p)
	}
	return out
}

// PutPendingRemoteCreate stores an unresolved remote create intent.
func (c *Catalog) PutPendingRemoteCreate(rec PendingRemoteCreateRecord) error {
	if err := rec.IntentID.Validate(); err != nil {
		return err
	}
	return c.apply("put-pending-remote-create", func(doc *AppDocument) error {
		return c.upsertPendingRemoteLocked(doc, rec)
	})
}

// RemovePendingRemoteCreate deletes a pending remote create intent.
func (c *Catalog) RemovePendingRemoteCreate(id CommandID) error {
	return c.apply("remove-pending-remote-create", func(doc *AppDocument) error {
		return c.removePendingRemoteLocked(doc, id)
	})
}

// LocalCatalogSnapshot returns the canonical, owner-authoritative catalog
// snapshot. Values are copies; consumers may retain them safely.
func (c *Catalog) LocalCatalogSnapshot() OwnerCatalogSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.localCatalogSnapshotLocked()
}

func (c *Catalog) localCatalogSnapshotLocked() OwnerCatalogSnapshot {
	return OwnerCatalogSnapshot{
		Owner:    c.owner,
		Revision: c.revision,
		Sessions: c.sortedSessionsLocked(),
		Layouts:  c.sortedLayoutsLocked(),
	}
}

type catalogSubscription struct {
	id int
	fn func(OwnerCatalogSnapshot)
}

// SubscribeCatalog registers a callback that receives the complete catalog
// snapshot after every accepted mutation (session/layout/pending-create
// changes). The returned function unsubscribes. Like SubscribeWorkspace, this
// always emits whole snapshots so consumers never observe partial state.
func (c *Catalog) SubscribeCatalog(fn func(OwnerCatalogSnapshot)) func() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextCatalogSubID++
	sub := catalogSubscription{id: c.nextCatalogSubID, fn: fn}
	c.catalogSubs = append(c.catalogSubs, sub)
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		filtered := c.catalogSubs[:0]
		for _, s := range c.catalogSubs {
			if s.id != sub.id {
				filtered = append(filtered, s)
			}
		}
		c.catalogSubs = filtered
	}
}

// publishCatalog emits a catalog snapshot to all subscribers.
func (c *Catalog) publishCatalog(snap OwnerCatalogSnapshot) {
	c.mu.RLock()
	subs := make([]catalogSubscription, len(c.catalogSubs))
	copy(subs, c.catalogSubs)
	c.mu.RUnlock()
	for _, s := range subs {
		s.fn(snap)
	}
}

// AggregateCatalogSnapshot returns the local, owner-authoritative snapshot
// only. It intentionally does not know about peers; multi-owner aggregation
// (local + cached remote-owner catalogs) is done by the package-level
// AggregateCatalog, which composes this with a RemoteCatalogSource.
func (c *Catalog) AggregateCatalogSnapshot() OwnerCatalogSnapshot {
	return c.LocalCatalogSnapshot()
}

// RemoteCatalogSource is implemented by anything that exposes cached
// catalog snapshots received from peers (e.g. pkg/peer.Manager). It is a
// read-only accessor: implementations must not perform validation or
// storage here -- that happens exclusively where the snapshots are
// accepted (e.g. peer.Manager.UpdateRemoteCatalog).
type RemoteCatalogSource interface {
	// AllRemoteCatalogSnapshots returns the latest cached snapshot for every
	// known remote owner. Implementations return defensive copies.
	AllRemoteCatalogSnapshots() []OwnerCatalogSnapshot
}

// MultiOwnerCatalogSnapshot combines this node's local catalog with the
// catalogs cached from its peers. Each owner (local and every remote owner)
// carries its own independent Revision -- revisions are never conflated
// across owners, since they are produced by independent, unrelated catalogs.
type MultiOwnerCatalogSnapshot struct {
	Local  OwnerCatalogSnapshot   `json:"local"`
	Remote []OwnerCatalogSnapshot `json:"remote,omitempty"`
}

// AggregateCatalog combines local's own snapshot with whatever remote-owner
// catalogs source currently has cached. A nil source yields Remote == nil,
// i.e. identical behavior to a single-node deployment. This is the single
// place local and remote catalog projections are merged for downstream
// (bootstrap/state-stream) consumption.
func AggregateCatalog(local *Catalog, source RemoteCatalogSource) MultiOwnerCatalogSnapshot {
	snap := MultiOwnerCatalogSnapshot{Local: local.AggregateCatalogSnapshot()}
	if source != nil {
		snap.Remote = source.AllRemoteCatalogSnapshots()
	}
	return snap
}

// apply runs a mutation against the current document. If the mutation does
// not change the document, no durable write happens. On success, the
// complete resulting catalog snapshot is published to CatalogSubscribers
// after the lock is released so a slow subscriber cannot stall mutations.
func (c *Catalog) apply(reason string, mutate func(*AppDocument) error) error {
	if c.store != nil {
		// If a prior write left durability uncertain, re-attempt it BEFORE
		// taking a snapshot for mutate. Otherwise a command-ID replay's
		// mutate closure (e.g. executeCreate's findCommandReceipt check)
		// would read the unconfirmed document, find its own receipt, and
		// report success without Store.Update ever being invoked again --
		// silently upgrading an uncertain write to acknowledged success.
		if err := c.store.Revalidate(); err != nil {
			return fmt.Errorf("catalog commit %q rejected: %w", reason, err)
		}
	}

	c.mu.Lock()

	var doc AppDocument
	if c.store != nil {
		doc = c.store.Snapshot()
	} else {
		doc = c.docFromMapsLocked()
	}

	if err := mutate(&doc); err != nil {
		c.mu.Unlock()
		return fmt.Errorf("mutation %q rejected: %w", reason, err)
	}

	if c.store != nil {
		if err := c.store.Update(reason, func(target *AppDocument) error {
			*target = doc
			return nil
		}); err != nil {
			// Regardless of which failure this is, Store.Update may have
			// already adopted its own new document into memory (it does so
			// specifically for errSyncDirFailedAfterRename, see store.go).
			// Resync the catalog's maps/revision from Store.Snapshot() so
			// this process's own read-side state never diverges from what
			// Store itself now believes on-disk, even though the command is
			// about to fail closed below.
			if resetErr := c.resetLocked(c.store.Snapshot()); resetErr != nil {
				logrus.WithError(resetErr).WithField("reason", reason).Warn("failed to resync catalog after store commit error")
			}
			c.mu.Unlock()
			if errors.Is(err, errSyncDirFailedAfterRename) {
				// The rename that made the new document durably visible on
				// disk already succeeded; only the directory-entry fsync is
				// uncertain. Durability of this write cannot be confirmed,
				// so the command must NOT be acknowledged as a success: fail
				// closed and let the caller retry/replay. Silently treating
				// this as success (the prior behavior) risks reporting a
				// mutation as committed when a crash before the directory
				// fsync completes could still lose it.
				logrus.WithError(err).WithField("reason", reason).Warn("catalog commit durable-write succeeded but directory fsync uncertain after rename; failing closed")
			}
			return err
		}
		doc = c.store.Snapshot()
	} else {
		oldDoc := c.docFromMapsLocked()
		if err := ValidateDocument(&doc); err != nil {
			c.mu.Unlock()
			return fmt.Errorf("invalid document after %q: %w", reason, err)
		}
		if err := CheckSessionMembershipAcrossLayouts(&doc); err != nil {
			c.mu.Unlock()
			return fmt.Errorf("layout membership conflict after %q: %w", reason, err)
		}
		if !docsEqual(oldDoc, doc) {
			doc.Revision = c.revision + 1
		}
	}

	if err := c.resetLocked(doc); err != nil {
		c.mu.Unlock()
		return err
	}
	snap := c.localCatalogSnapshotLocked()
	c.mu.Unlock()

	c.publishCatalog(snap)
	return nil
}

func (c *Catalog) resetLocked(doc AppDocument) error {
	if err := ValidateDocument(&doc); err != nil {
		return fmt.Errorf("catalog reset refused: %w", err)
	}
	c.owner = doc.Owner
	c.revision = doc.Revision
	c.sessions = make(map[SessionID]LocalSessionRecord, len(doc.Sessions))
	for _, s := range doc.Sessions {
		c.sessions[s.ID] = s
	}
	c.layouts = make(map[LayoutID]LayoutRecord, len(doc.Layouts))
	for _, l := range doc.Layouts {
		c.layouts[l.ID] = l
	}
	c.pending = make(map[CommandID]PendingCreateRecord, len(doc.PendingCreates))
	for _, p := range doc.PendingCreates {
		c.pending[p.IntentID] = p
	}
	c.remotePending = make(map[CommandID]PendingRemoteCreateRecord, len(doc.PendingRemoteCreates))
	for _, p := range doc.PendingRemoteCreates {
		c.remotePending[p.IntentID] = p
	}
	if c.activeKeys == nil {
		c.activeKeys = make(map[LayoutID]*SessionRef)
	}
	c.commands = make([]CommandReceipt, len(doc.Commands))
	copy(c.commands, doc.Commands)
	return nil
}

func (c *Catalog) emptyDoc() AppDocument {
	return AppDocument{
		Schema:   SchemaVersion,
		Owner:    c.owner,
		Revision: 0,
		Sessions: []LocalSessionRecord{},
		Layouts:  []LayoutRecord{},
	}
}

func (c *Catalog) docFromMapsLocked() AppDocument {
	doc := c.emptyDoc()
	doc.Revision = c.revision
	doc.Sessions = c.sortedSessionsLocked()
	doc.Layouts = c.sortedLayoutsLocked()
	doc.PendingCreates = c.sortedPendingLocked()
	doc.PendingRemoteCreates = c.sortedPendingRemoteLocked()
	doc.Commands = c.sortedCommandsLocked()
	return doc
}

func (c *Catalog) sortedSessionsLocked() []LocalSessionRecord {
	out := make([]LocalSessionRecord, 0, len(c.sessions))
	for _, s := range c.sessions {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (c *Catalog) sortedLayoutsLocked() []LayoutRecord {
	out := make([]LayoutRecord, 0, len(c.layouts))
	for _, l := range c.layouts {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (c *Catalog) sortedPendingLocked() []PendingCreateRecord {
	out := make([]PendingCreateRecord, 0, len(c.pending))
	for _, p := range c.pending {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IntentID < out[j].IntentID })
	return out
}

func (c *Catalog) sortedPendingRemoteLocked() []PendingRemoteCreateRecord {
	out := make([]PendingRemoteCreateRecord, 0, len(c.remotePending))
	for _, p := range c.remotePending {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IntentID < out[j].IntentID })
	return out
}

func (c *Catalog) sortedCommandsLocked() []CommandReceipt {
	out := make([]CommandReceipt, len(c.commands))
	copy(out, c.commands)
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

func (c *Catalog) upsertSessionLocked(doc *AppDocument, rec LocalSessionRecord) error {
	found := false
	for i := range doc.Sessions {
		if doc.Sessions[i].ID == rec.ID {
			doc.Sessions[i] = rec
			found = true
			break
		}
	}
	if !found {
		doc.Sessions = append(doc.Sessions, rec)
	}
	return nil
}

func (c *Catalog) removeSessionLocked(doc *AppDocument, id SessionID) error {
	filtered := doc.Sessions[:0]
	for _, s := range doc.Sessions {
		if s.ID != id {
			filtered = append(filtered, s)
		}
	}
	doc.Sessions = filtered
	return nil
}

func (c *Catalog) upsertLayoutLocked(doc *AppDocument, rec LayoutRecord) error {
	found := false
	for i := range doc.Layouts {
		if doc.Layouts[i].ID == rec.ID {
			doc.Layouts[i] = rec
			found = true
			break
		}
	}
	if !found {
		doc.Layouts = append(doc.Layouts, rec)
	}
	return nil
}

func (c *Catalog) removeLayoutLocked(doc *AppDocument, id LayoutID) error {
	filtered := doc.Layouts[:0]
	for _, l := range doc.Layouts {
		if l.ID != id {
			filtered = append(filtered, l)
		}
	}
	doc.Layouts = filtered
	return nil
}

func (c *Catalog) upsertPendingLocked(doc *AppDocument, rec PendingCreateRecord) error {
	found := false
	for i := range doc.PendingCreates {
		if doc.PendingCreates[i].IntentID == rec.IntentID {
			doc.PendingCreates[i] = rec
			found = true
			break
		}
	}
	if !found {
		doc.PendingCreates = append(doc.PendingCreates, rec)
	}
	return nil
}

func (c *Catalog) removePendingLocked(doc *AppDocument, id CommandID) error {
	filtered := doc.PendingCreates[:0]
	for _, p := range doc.PendingCreates {
		if p.IntentID != id {
			filtered = append(filtered, p)
		}
	}
	doc.PendingCreates = filtered
	return nil
}

func (c *Catalog) upsertPendingRemoteLocked(doc *AppDocument, rec PendingRemoteCreateRecord) error {
	found := false
	for i := range doc.PendingRemoteCreates {
		if doc.PendingRemoteCreates[i].IntentID == rec.IntentID {
			doc.PendingRemoteCreates[i] = rec
			found = true
			break
		}
	}
	if !found {
		doc.PendingRemoteCreates = append(doc.PendingRemoteCreates, rec)
	}
	return nil
}

func (c *Catalog) removePendingRemoteLocked(doc *AppDocument, id CommandID) error {
	filtered := doc.PendingRemoteCreates[:0]
	for _, p := range doc.PendingRemoteCreates {
		if p.IntentID != id {
			filtered = append(filtered, p)
		}
	}
	doc.PendingRemoteCreates = filtered
	return nil
}

// SessionRuntime carries live, non-persisted enrichment for a session.
type SessionRuntime struct {
	CurrentPath    string
	CurrentCommand string
	DaemonPID      int
	ShellPID       int
	PromptPreview  string
	LastActivity   time.Time
}

// SessionView is a runtime-enriched, read-only view of one catalog record.
type SessionView struct {
	LocalSessionRecord
	Runtime SessionRuntime
}

// RuntimeEnricher returns live runtime fields for a session reference without
// mutating the persisted record.
type RuntimeEnricher interface {
	Enrich(ref SessionRef, rec LocalSessionRecord) SessionRuntime
}

// NewSessionView builds a SessionView from a record and an enricher. A nil
// enricher is allowed and yields an empty SessionRuntime.
func NewSessionView(rec LocalSessionRecord, enricher RuntimeEnricher) SessionView {
	var rt SessionRuntime
	if enricher != nil {
		rt = enricher.Enrich(rec.Ref, rec)
	}
	return SessionView{LocalSessionRecord: rec, Runtime: rt}
}
