package state

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/anh-chu/termyard/pkg/pty"
)

// DaemonBackend is the narrow interface the reconciler uses to inspect and
// drive daemon lifecycle. It is intentionally small so tests can inject a
// fake without implementing the full pty.Registry.
type DaemonBackend interface {
	Probe(binding pty.StableBinding) pty.ProbeEvidence
	Start(ctx context.Context, req pty.StartRequest) (pty.ReadyInfo, error)
	Terminate(ctx context.Context, binding pty.StableBinding) pty.TerminateOutcome
}

// Ensure *pty.Registry satisfies DaemonBackend at compile time.
var _ DaemonBackend = (*pty.Registry)(nil)

// ReconcilerOptions configures reconciler timing and retry policy.
type ReconcilerOptions struct {
	Tick                  time.Duration
	MaxCreateRetries      int
	RetryInitial          time.Duration
	RetryMax              time.Duration
	Now                   func() time.Time
	DisablePendingCreates bool // set true when a command service owns create work
}

func (o ReconcilerOptions) withDefaults() ReconcilerOptions {
	if o.Tick <= 0 {
		o.Tick = 2 * time.Second
	}
	if o.MaxCreateRetries <= 0 {
		o.MaxCreateRetries = MaxCreateRetries
	}
	if o.RetryInitial <= 0 {
		o.RetryInitial = CreateRetryInitial
	}
	if o.RetryMax <= 0 {
		o.RetryMax = CreateRetryMax
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

// Reconciler classifies session lifecycle evidence before publishing catalog
// snapshots. It is owner-authoritative: the catalog decides which sessions
// exist; discovery only supplies evidence.
type Reconciler struct {
	catalog  *Catalog
	backend  DaemonBackend
	enricher RuntimeEnricher
	opts     ReconcilerOptions
	mu       sync.RWMutex
	subs     []func(OwnerCatalogSnapshot)
	lastSnap OwnerCatalogSnapshot
	started  bool
}

// NewReconciler builds a reconciler backed by catalog and daemon backend.
func NewReconciler(catalog *Catalog, backend DaemonBackend, enricher RuntimeEnricher, opts ReconcilerOptions) *Reconciler {
	return &Reconciler{
		catalog:  catalog,
		backend:  backend,
		enricher: enricher,
		opts:     opts.withDefaults(),
	}
}

// Subscribe registers a callback for published catalog snapshots. Callbacks
// must not block.
func (r *Reconciler) Subscribe(fn func(OwnerCatalogSnapshot)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.subs = append(r.subs, fn)
}

// Run starts periodic reconciliation. It immediately resolves intents and
// publishes one classified snapshot so the first view after restart is
// complete.
func (r *Reconciler) Run(ctx context.Context) error {
	if err := r.ResolveIntents(ctx); err != nil {
		logrus.WithError(err).Warn("catalog intent resolution failed")
	}
	if err := r.ReconcileOnce(ctx); err != nil {
		logrus.WithError(err).Warn("initial reconciliation failed")
	}
	// Always publish once after the first reconciliation so consumers see a
	// complete, classified snapshot immediately after restart.
	r.publish(r.catalog.LocalCatalogSnapshot())

	r.mu.Lock()
	r.started = true
	r.mu.Unlock()

	ticker := time.NewTicker(r.opts.Tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := r.ReconcileOnce(ctx); err != nil {
				logrus.WithError(err).Warn("reconciliation failed")
			}
		}
	}
}

// ReconcileOnce inspects all records, classifies them, applies a single
// batch update if semantics changed, and publishes the snapshot.
func (r *Reconciler) ReconcileOnce(ctx context.Context) error {
	snapshot := r.catalog.LocalCatalogSnapshot()

	classified := make([]LocalSessionRecord, len(snapshot.Sessions))
	copy(classified, snapshot.Sessions)
	removals := make(map[SessionID]bool, len(classified))
	stopActions := []SessionID{}

	for i := range classified {
		rec := &classified[i]
		binding := bindingForRecord(rec)
		ev := r.backend.Probe(binding)

		newPhase, remove := classifyRecord(*rec, ev)
		if remove {
			removals[rec.ID] = true
			continue
		}

		// Stopping a live session is an intentional action, not a classification.
		if rec.Desired == DesiredStop {
			if ev.Status == pty.ProbeLive && livePhase(rec.Phase) {
				stopActions = append(stopActions, rec.ID)
			}
		}

		if newPhase != rec.Phase {
			logrus.WithFields(logrus.Fields{
				"session_id": rec.ID,
				"old_phase":  rec.Phase,
				"new_phase":  newPhase,
				"reason":     ev.Reason,
			}).Debug("classified session transition")
			rec.Phase = newPhase
		}
		if rec.Phase == SessionPhaseActive && rec.Desired == DesiredRestart {
			rec.Desired = DesiredRun
		}
	}

	// Apply any stop actions first; they do not change the catalog directly.
	for _, id := range stopActions {
		rec, ok := r.catalog.Session(id)
		if !ok {
			continue
		}
		r.backend.Terminate(ctx, bindingForRecord(&rec))
	}

	// Resolve pending creates whose daemon side effect is already complete.
	// The adopt-via-probe step below is gated behind !DisablePendingCreates:
	// when a command service owns create work (every production caller --
	// see pkg/commands/server/runtime.go -- sets DisablePendingCreates: true
	// precisely so this reconciler never touches pending creates), letting it
	// probe-and-adopt anyway raced the command service's OWN in-flight
	// executePendingCreate/Start/waitReady call for the exact same pending
	// record. bindingForRef uses a generation-less binding (the reconciler
	// never knows the assigned generation up front) to discover *any* live
	// daemon for the owner/session and adopts it immediately -- which could
	// win the race against the command service's own spawn attempt before
	// that attempt's waitReady() ever observes ITS expected generation on the
	// socket (causing it to time out and mark the session crashed even
	// though a real daemon is live), and, prior to the Probe fix above that
	// backfills the real discovered generation, committed the adopted
	// session with a permanently empty Generation (which then can
	// never satisfy mayRemoveClean, so a later kill of that exact session
	// could classify it terminal but never actually be removed from the
	// catalog). Only the simple "pending record whose session already
	// exists" cleanup below is unconditional -- it is pure idempotent garbage
	// collection, not a second actor resolving the create.
	for _, pending := range r.catalog.PendingCreates() {
		if _, exists := r.catalog.Session(pending.Ref.Session); exists {
			_ = r.catalog.RemovePendingCreate(pending.IntentID)
			continue
		}
		if r.opts.DisablePendingCreates {
			continue
		}
		binding := bindingForRef(pending.Ref)
		ev := r.backend.Probe(binding)
		if ev.Status == pty.ProbeLive && generationMatches(ev, pending.Ref) {
			if err := r.adoptLivePending(ctx, pending, ev); err != nil {
				logrus.WithError(err).WithField("intent", pending.IntentID).Warn("failed to adopt live pending create")
			}
		}
	}

	// Compare against current catalog; only apply if semantics changed.
	changed := false
	for _, rec := range classified {
		cur, ok := r.catalog.Session(rec.ID)
		if !ok || !sessionEqual(cur, rec) {
			changed = true
			break
		}
	}
	if !changed {
		for id := range removals {
			if _, ok := r.catalog.Session(id); ok {
				changed = true
				break
			}
		}
	}
	// NOTE: this used to also force an apply+publish whenever any pending
	// create existed, even if nothing about the classified sessions actually
	// changed. That meant every tick (default 2s) re-committed and
	// re-published an identical no-op snapshot for the entire lifetime of a
	// slow-to-resolve pending create (e.g. a real daemon spawn taking
	// several seconds under load) -- and because a layout leaf is inserted
	// for the new session's ref before the session record itself lands in
	// doc.Sessions (see SessionCommandService.executeCreate ->
	// placeSessionInWorkspace), that repeatedly-republished snapshot is
	// catalog-invariant-invalid (layout leaf references an as-yet-unknown
	// session) and gets dropped by every connected peer's
	// validateCatalogInvariants on every single tick, spamming
	// "dropping v2 snapshot: layout leaf references unknown session"
	// warnings without ever making progress. Pending-create resolution
	// itself (adoptLivePending, above) already performs and publishes its
	// own catalog.apply the moment it actually resolves; this batch
	// reclassification commit only needs to run when something it
	// classifies has actually changed.
	if !changed {
		return nil
	}

	if err := r.catalog.apply("reconcile", func(doc *AppDocument) error {
		for id := range removals {
			var ref SessionRef
			for _, rec := range doc.Sessions {
				if rec.ID == id {
					ref = rec.Ref
					break
				}
			}
			if ref.Session == "" {
				continue
			}
			if err := removeSessionFromWorkspacesLocked(doc, ref); err != nil {
				return err
			}
		}
		doc.Sessions = make([]LocalSessionRecord, 0, len(classified))
		for i := range classified {
			if removals[classified[i].ID] {
				continue
			}
			doc.Sessions = append(doc.Sessions, classified[i])
		}
		return nil
	}); err != nil {
		return fmt.Errorf("apply reconciliation: %w", err)
	}

	snap := r.catalog.LocalCatalogSnapshot()
	r.publish(snap)
	return nil
}

// ResolveIntents attempts to start sessions that are requested but not yet
// live. It runs once at startup and whenever explicitly called.
func (r *Reconciler) ResolveIntents(ctx context.Context) error {
	now := r.opts.Now()

	for _, rec := range r.catalog.Sessions() {
		if rec.Desired != DesiredRun && rec.Desired != DesiredRestart {
			continue
		}
		binding := bindingForRecord(&rec)
		ev := r.backend.Probe(binding)
		if ev.Status == pty.ProbeLive {
			continue
		}
		if shouldStartPhase(rec.Phase) {
			if err := r.startRecord(ctx, rec); err != nil {
				logrus.WithError(err).WithField("session_id", rec.ID).Warn("failed to start session from intent")
			}
		}
	}

	if !r.opts.DisablePendingCreates {
		for _, pending := range r.catalog.PendingCreates() {
			if pending.NextAttempt.After(now) {
				continue
			}
			if _, exists := r.catalog.Session(pending.Ref.Session); exists {
				_ = r.catalog.RemovePendingCreate(pending.IntentID)
				continue
			}
			if err := r.startPending(ctx, pending); err != nil {
				if pending.Retries >= r.opts.MaxCreateRetries {
					_ = r.catalog.RemovePendingCreate(pending.IntentID)
					logrus.WithField("intent_id", pending.IntentID).Warn("pending create exceeded max retries; dropped")
					continue
				}
				pending.Retries++
				backoff := r.opts.RetryInitial * time.Duration(1<<min(pending.Retries, 10))
				if backoff > r.opts.RetryMax {
					backoff = r.opts.RetryMax
				}
				pending.NextAttempt = now.Add(backoff)
				_ = r.catalog.PutPendingCreate(pending)
			}
		}
	}
	return nil
}

// LocalCatalogSnapshot returns the canonical owner catalog snapshot.
func (r *Reconciler) LocalCatalogSnapshot() OwnerCatalogSnapshot {
	return r.catalog.LocalCatalogSnapshot()
}

// EnrichedSnapshot returns runtime-enriched views for every catalog record.
func (r *Reconciler) EnrichedSnapshot() []SessionView {
	snap := r.catalog.LocalCatalogSnapshot()
	out := make([]SessionView, len(snap.Sessions))
	for i, rec := range snap.Sessions {
		out[i] = NewSessionView(rec, r.enricher)
	}
	return out
}

func (r *Reconciler) publish(snap OwnerCatalogSnapshot) {
	r.mu.RLock()
	subs := make([]func(OwnerCatalogSnapshot), len(r.subs))
	copy(subs, r.subs)
	r.mu.RUnlock()
	for _, fn := range subs {
		fn(snap)
	}
	r.mu.Lock()
	r.lastSnap = snap
	r.mu.Unlock()
}

func (r *Reconciler) startRecord(ctx context.Context, rec LocalSessionRecord) error {
	req := startRequestForRecord(rec)
	info, err := r.backend.Start(ctx, req)
	if err != nil {
		return err
	}
	rec.Phase = SessionPhaseActive
	rec.Desired = DesiredRun
	rec.DaemonPID = info.DaemonPID
	rec.Generation = info.Generation
	return r.catalog.PutSession(rec)
}

func (r *Reconciler) startPending(ctx context.Context, pending PendingCreateRecord) error {
	req := pty.StartRequest{
		StableBinding: pty.StableBinding{
			Owner:      string(pending.Ref.Owner),
			SessionID:  string(pending.Ref.Session),
			DaemonKey:  string(pending.Ref.Session),
			Generation: "",
		},
		Shell: pending.Shell,
		Cwd:   pending.Cwd,
		Cols:  pending.Cols,
		Rows:  pending.Rows,
	}
	info, err := r.backend.Start(ctx, req)
	if err != nil {
		return err
	}
	rec := LocalSessionRecord{
		ID:         pending.Ref.Session,
		Owner:      pending.Ref.Owner,
		Ref:        pending.Ref,
		Phase:      SessionPhaseActive,
		Desired:    DesiredRun,
		Created:    r.opts.Now(),
		Name:       pendingDisplayName(pending),
		Shell:      pending.Shell,
		Cwd:        pending.Cwd,
		Cols:       pending.Cols,
		Rows:       pending.Rows,
		DaemonPID:  info.DaemonPID,
		Generation: info.Generation,
	}
	if err := r.catalog.PutSession(rec); err != nil {
		return err
	}
	return r.catalog.RemovePendingCreate(pending.IntentID)
}

func (r *Reconciler) adoptLivePending(ctx context.Context, pending PendingCreateRecord, ev pty.ProbeEvidence) error {
	rec := LocalSessionRecord{
		ID:         pending.Ref.Session,
		Owner:      pending.Ref.Owner,
		Ref:        pending.Ref,
		Phase:      SessionPhaseActive,
		Desired:    DesiredRun,
		Created:    r.opts.Now(),
		Name:       pendingDisplayName(pending),
		Shell:      pending.Shell,
		Cwd:        pending.Cwd,
		Cols:       pending.Cols,
		Rows:       pending.Rows,
		DaemonPID:  ev.DaemonPID,
		Generation: ev.Binding.Generation,
	}
	if err := r.catalog.PutSession(rec); err != nil {
		return err
	}
	return r.catalog.RemovePendingCreate(pending.IntentID)
}

// pendingDisplayName returns the user-requested display name recorded on the
// pending create, falling back to the raw session ID only when no display
// name was ever set (mirrors the fallback in session_commands.go's create
// path, which always populates DisplayName -- this is defensive belt-and-
// braces for any pending record that predates that guarantee).
func pendingDisplayName(pending PendingCreateRecord) string {
	if pending.DisplayName != "" {
		return pending.DisplayName
	}
	return string(pending.Ref.Session)
}

func bindingForRecord(rec *LocalSessionRecord) pty.StableBinding {
	return pty.StableBinding{
		Owner:      string(rec.Ref.Owner),
		SessionID:  string(rec.Ref.Session),
		Generation: rec.Generation,
		DaemonKey:  string(rec.Ref.Session),
	}
}

func bindingForRef(ref SessionRef) pty.StableBinding {
	return pty.StableBinding{
		Owner:      string(ref.Owner),
		SessionID:  string(ref.Session),
		DaemonKey:  string(ref.Session),
		Generation: "",
	}
}

func generationMatches(ev pty.ProbeEvidence, ref SessionRef) bool {
	if ev.Binding.Generation == "" {
		return true
	}
	return true
}

func livePhase(phase SessionPhase) bool {
	switch phase {
	case SessionPhasePending, SessionPhaseStarting, SessionPhaseActive, SessionPhaseCrashed:
		return true
	}
	return false
}

func shouldStartPhase(phase SessionPhase) bool {
	switch phase {
	case SessionPhasePending, SessionPhaseStarting, SessionPhaseCrashed:
		return true
	}
	return false
}

// classifyRecord returns the canonical phase for a record given probe evidence.
// It never calls the daemon; it only interprets evidence. The second return
// value is true when the record should be removed.
func classifyRecord(rec LocalSessionRecord, ev pty.ProbeEvidence) (SessionPhase, bool) {
	switch rec.Desired {
	case DesiredStop:
		return classifyStop(rec, ev)
	case DesiredRun, DesiredRestart:
		return classifyRun(rec, ev)
	}
	return rec.Phase, false
}

func classifyStop(rec LocalSessionRecord, ev pty.ProbeEvidence) (SessionPhase, bool) {
	switch ev.Status {
	case pty.ProbeClean:
		return SessionPhaseCleanlyEnded, mayRemoveClean(rec.Generation, ev.Binding.Generation)
	case pty.ProbeCrashed:
		return SessionPhaseCrashed, false
	case pty.ProbeLive:
		// Keep the current phase: the stop action is issued elsewhere. Unknown
		// evidence would also preserve the record.
		return rec.Phase, false
	case pty.ProbeUnknown:
		return rec.Phase, false
	}
	return rec.Phase, false
}

func classifyRun(rec LocalSessionRecord, ev pty.ProbeEvidence) (SessionPhase, bool) {
	switch ev.Status {
	case pty.ProbeLive:
		return SessionPhaseActive, false
	case pty.ProbeClean:
		return SessionPhaseCleanlyEnded, mayRemoveClean(rec.Generation, ev.Binding.Generation)
	case pty.ProbeCrashed:
		return SessionPhaseCrashed, false
	case pty.ProbeUnknown:
		return rec.Phase, false
	}
	return rec.Phase, false
}

// mayRemoveClean allows removal when the evidence is for the current
// generation. The record may have just become cleanly ended in this cycle.
func mayRemoveClean(recordGeneration, evidenceGeneration string) bool {
	if recordGeneration == "" {
		return false
	}
	if evidenceGeneration == "" {
		return false
	}
	return recordGeneration == evidenceGeneration
}

func startRequestForRecord(rec LocalSessionRecord) pty.StartRequest {
	return pty.StartRequest{
		StableBinding: pty.StableBinding{
			Owner:      string(rec.Ref.Owner),
			SessionID:  string(rec.Ref.Session),
			Generation: "",
			DaemonKey:  string(rec.Ref.Session),
		},
		Shell: rec.Shell,
		Cwd:   rec.Cwd,
		Cols:  rec.Cols,
		Rows:  rec.Rows,
	}
}

func sessionEqual(a, b LocalSessionRecord) bool {
	return a.ID == b.ID &&
		a.Phase == b.Phase &&
		a.Desired == b.Desired &&
		a.Generation == b.Generation &&
		a.DaemonPID == b.DaemonPID
}
