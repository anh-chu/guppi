package state

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/anh-chu/termyard/pkg/git"
	"github.com/anh-chu/termyard/pkg/model"
	"github.com/anh-chu/termyard/pkg/pty"
)

// Session command actions.
const (
	ActionCreate          = "create"
	ActionKill            = "kill"
	ActionLabel           = "label"
	ActionRecover         = "recover"
	ActionDismiss         = "dismiss"
	ActionRetry           = "retry"
	ActionSetPresentation = "set_presentation"
)

// CrashPoint labels the exact phases where tests may inject a fault. They are
// intentionally generic so the same test harness covers process crashes,
// panics, and context cancellation.
type CrashPoint string

const (
	CrashBeforeIntentCommit     CrashPoint = "before_intent_commit"
	CrashAfterIntentCommit      CrashPoint = "after_intent_commit"
	CrashAfterWorktreePrepare   CrashPoint = "after_worktree_prepare"
	CrashAfterChildSpawn        CrashPoint = "after_child_spawn"
	CrashAfterDaemonReady       CrashPoint = "after_daemon_ready"
	CrashBeforeRunningCommit    CrashPoint = "before_running_commit"
	CrashAfterCommitBeforeReply CrashPoint = "after_commit_before_reply"
)

// CreateParams carries a local create request. Target/Direction/NewFirst are
// optional and mirror RemoteCreateRequest's identical fields (see
// remote_create.go): when Target names an existing leaf in LayoutID, the new
// session is placed by splitting that leaf in Direction (NewFirst controls
// which side of the split the new leaf lands on), in the SAME atomic
// transaction that commits the create -- not as a separate, later workspace
// command. This is what lets a caller request "create and split next to X"
// as one indivisible placement instead of create-then-split, which could
// otherwise place the ref via the default "first free leaf" heuristic and
// then have the follow-up split rejected as a duplicate-leaf insert of a ref
// that is already placed. When Target is empty, placement falls back to the
// existing default-leaf heuristic, unchanged.
// Target is a pointer (not a plain SessionRef) because SessionRef's custom
// MarshalJSON always produces a non-empty string (":0.0" for the zero
// value) -- json's "omitempty" tag option does not apply to struct values,
// so a plain SessionRef field would always be present on the wire and
// always fail to decode back (ParseSessionRef rejects an empty session id).
// A pointer is genuinely absent when nil, matching the optional semantics.
type CreateParams struct {
	Name           string         `json:"name,omitempty"`
	Shell          string         `json:"shell,omitempty"`
	Cwd            string         `json:"cwd,omitempty"`
	WorktreeBranch string         `json:"worktree_branch,omitempty"`
	Cols           uint16         `json:"cols,omitempty"`
	Rows           uint16         `json:"rows,omitempty"`
	LayoutID       LayoutID       `json:"layout_id,omitempty"`
	AgentType      string         `json:"agent_type,omitempty"`
	Target         *SessionRef    `json:"target,omitempty"`
	Direction      SplitDirection `json:"direction,omitempty"`
	NewFirst       bool           `json:"new_first,omitempty"`
	// ScheduleID, when non-empty, identifies the scheduler job requesting this
	// create. It is carried through to PendingCreateRecord.ScheduleID so a
	// schedule can be correlated with the session it produces; ValidateDocument
	// rejects more than one in-flight pending create sharing the same
	// ScheduleID.
	ScheduleID string `json:"schedule_id,omitempty"`
}

// RecoverParams overrides the saved shell/cwd for crash recovery.
type RecoverParams struct {
	Shell string `json:"shell,omitempty"`
	Cwd   string `json:"cwd,omitempty"`
}

// PresentationParams updates the mutable Hidden/Background display flags for
// a session. Both are pointers so a caller can change just one without
// clobbering the other -- an omitted (nil) field leaves the existing stored
// value untouched, mirroring the optional-field convention CreateParams.Target
// already uses for the same JSON-omitempty-on-structs reason (a plain bool
// can't distinguish "explicitly false" from "not sent").
type PresentationParams struct {
	Hidden     *bool `json:"hidden,omitempty"`
	Background *bool `json:"background,omitempty"`
}

// KillParams optionally extends a kill command with worktree cleanup.
type KillParams struct {
	// RemoveWorktree, when true, removes the session's worktree (via `git
	// worktree remove`, falling back to directory removal) as part of this
	// same kill command instead of a separate, later, non-atomic step. The
	// canonical CWD is captured from the catalog record BEFORE the daemon
	// termination side effect runs, and the removal itself only runs once per
	// command ID (replay of an already-committed kill returns the stored
	// receipt and never re-attempts removal).
	RemoveWorktree bool `json:"remove_worktree,omitempty"`
}

// LabelParams updates the mutable display label for a session.
type LabelParams struct {
	Label string `json:"label"`
}

// CommandResult acknowledges an accepted command and gives the caller the
// stable identity that was durably committed. Fields carry explicit JSON
// tags (snake_case, matching every other v2 wire type) because without them
// Go would encode the Go field names verbatim ("ID", "Ref", "DisplayName",
// ...) -- an inconsistent, undocumented wire shape that the browser had no
// codec for. Ref is a SessionRef, which itself marshals to a canonical
// STRING (see SessionRef.MarshalJSON in ids.go), not a JSON object; the
// browser-side codec that decodes it into the object shape lives in
// web/src/state/v2/wireCodec.ts's decodeCommandResult.
type CommandResult struct {
	ID          CommandID  `json:"id"`
	Ref         SessionRef `json:"ref"`
	DisplayName string     `json:"display_name,omitempty"`
	Path        string     `json:"path,omitempty"`
	Accepted    bool       `json:"accepted"`
}

// SessionCommandService turns session-level requests into atomic catalog
// mutations and daemon effects. It is the single local owner of create, kill,
// recover, and label semantics.
type SessionCommandService struct {
	catalog  *Catalog
	backend  DaemonBackend
	enricher RuntimeEnricher
	owner    OwnerID
	opts     SessionCommandServiceOptions
	mu       sync.Mutex

	// inFlight de-duplicates truly concurrent calls sharing the same command
	// ID so at most one goroutine ever runs a command's mutation/side-effect
	// body. Receipt-based replay (peekReceipt/commitSessionReceipt) alone is
	// not sufficient: two callers can both race past the "does a receipt
	// already exist" check before either commits one, and for actions with a
	// non-idempotent side effect (recover's backend.Start spawns a brand new
	// daemon process/generation on every call) that would leak a duplicate,
	// unreferenced daemon. inFlight closes that window by making the second
	// caller wait for the first to finish and reuse its exact result instead
	// of independently invoking the side effect.
	inFlight map[CommandID]*inFlightCommand
}

// inFlightCommand is the single in-progress execution shared by every
// concurrent caller presenting the same command ID.
type inFlightCommand struct {
	done   chan struct{}
	result CommandResult
	err    error
}

// SessionCommandServiceOptions configures timing, retry policy, and test
// fault injection for the command service.
type SessionCommandServiceOptions struct {
	Owner            OwnerID
	Tick             time.Duration
	MaxCreateRetries int
	RetryInitial     time.Duration
	RetryMax         time.Duration
	Now              func() time.Time
	CrashHook        func(CrashPoint)
	BeforeStartHook  func(PendingCreateRecord)
	AfterStartHook   func(PendingCreateRecord, pty.ReadyInfo, error)
}

func (o SessionCommandServiceOptions) withDefaults() SessionCommandServiceOptions {
	if o.Tick <= 0 {
		o.Tick = 200 * time.Millisecond
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

// NewSessionCommandService builds a command service bound to one owner catalog.
func NewSessionCommandService(catalog *Catalog, backend DaemonBackend, enricher RuntimeEnricher, opts SessionCommandServiceOptions) *SessionCommandService {
	opts = opts.withDefaults()
	if opts.Owner == "" {
		opts.Owner = catalog.Owner()
	}
	return &SessionCommandService{
		catalog:  catalog,
		backend:  backend,
		enricher: enricher,
		owner:    opts.Owner,
		opts:     opts,
	}
}

// ExecuteSessionCommand applies one command. Accepted commands are durable
// before the method returns; create work and recovery are driven by Run.
func (s *SessionCommandService) ExecuteSessionCommand(ctx context.Context, cmd SessionCommand) (CommandResult, error) {
	if err := cmd.ID.Validate(); err != nil {
		return CommandResult{}, StateError{Code: ErrInvalidIdentity, Field: "id", Detail: err.Error()}
	}
	return s.runSingleFlight(cmd.ID, func() (CommandResult, error) {
		switch cmd.Action {
		case ActionCreate:
			return s.executeCreate(ctx, cmd)
		case ActionKill:
			return s.executeKill(ctx, cmd)
		case ActionLabel:
			return s.executeLabel(ctx, cmd)
		case ActionRecover:
			return s.executeRecover(ctx, cmd)
		case ActionDismiss:
			return s.executeDismiss(ctx, cmd)
		case ActionRetry:
			return s.executeRetry(ctx, cmd)
		case ActionSetPresentation:
			return s.executeSetPresentation(ctx, cmd)
		default:
			return CommandResult{}, StateError{Code: ErrMalformedSplit, Field: "action", Detail: fmt.Sprintf("unknown session action %q", cmd.Action)}
		}
	})
}

// runSingleFlight ensures at most one goroutine executes fn for a given
// command ID at a time. Concurrent callers presenting the same ID block on
// the first caller's in-progress execution and receive its exact result
// instead of independently re-running fn (and any side effect it performs).
// The in-flight entry is removed once fn returns, so a later, non-concurrent
// replay of the same ID takes the normal peekReceipt/commitSessionReceipt
// fast path unchanged; this only closes the narrow true-concurrency window.
func (s *SessionCommandService) runSingleFlight(id CommandID, fn func() (CommandResult, error)) (CommandResult, error) {
	s.mu.Lock()
	if s.inFlight == nil {
		s.inFlight = make(map[CommandID]*inFlightCommand)
	}
	if existing, ok := s.inFlight[id]; ok {
		s.mu.Unlock()
		<-existing.done
		return existing.result, existing.err
	}
	entry := &inFlightCommand{done: make(chan struct{})}
	s.inFlight[id] = entry
	s.mu.Unlock()

	entry.result, entry.err = fn()

	s.mu.Lock()
	delete(s.inFlight, id)
	s.mu.Unlock()
	close(entry.done)

	return entry.result, entry.err
}

// ExecuteSessionCommandFromPeer applies a session command that arrived over
// an authenticated peer connection. peerID must be the connection's
// authenticated identity (empty means "trusted local caller", which is left
// to ExecuteSessionCommand's existing, unchanged behavior). Because this
// service always mutates its own local catalog, a peer-originated command may
// only target a ref owned by this node; a forged or mismatched Ref.Owner is
// rejected with a typed error BEFORE any mutation is attempted. Local
// (non-peer) callers must keep calling ExecuteSessionCommand directly so this
// check is never applied to already-trusted local paths.
func (s *SessionCommandService) ExecuteSessionCommandFromPeer(ctx context.Context, cmd SessionCommand, peerID string) (CommandResult, error) {
	if peerID != "" {
		if cmd.Ref.Owner == "" || cmd.Ref.Owner != s.owner {
			return CommandResult{}, StateError{
				Code:   ErrOwnershipMismatch,
				Field:  "ref.owner",
				Detail: fmt.Sprintf("ref owner %q does not match this node's owner %q", cmd.Ref.Owner, s.owner),
			}
		}
	}
	return s.ExecuteSessionCommand(ctx, cmd)
}

// Owner returns this node's own catalog owner ID. Callers (route handlers)
// use it to decide whether an inbound command's Ref.Owner targets this
// node's own catalog or must be forwarded to a remote peer.
func (s *SessionCommandService) Owner() OwnerID {
	return s.owner
}

// LookupRefByDisplayName returns the canonical session ref for a display name
// or session id. It is used by v1 route adapters that only know the UI label.
func (s *SessionCommandService) LookupRefByDisplayName(name string) (SessionRef, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return SessionRef{}, false
	}
	for _, rec := range s.catalog.Sessions() {
		if rec.Name == name || string(rec.ID) == name {
			return rec.Ref, true
		}
	}
	for _, p := range s.catalog.PendingCreates() {
		if p.DisplayName == name || string(p.Ref.Session) == name {
			return p.Ref, true
		}
	}
	return SessionRef{}, false
}

// Run drives create intents and retries under the runtime context. It returns
// when ctx is cancelled.
func (s *SessionCommandService) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.opts.Tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			s.runPendingCreates(ctx)
		}
	}
}

func (s *SessionCommandService) runPendingCreates(ctx context.Context) {
	now := s.opts.Now()
	for _, p := range s.catalog.PendingCreates() {
		if p.NextAttempt.After(now) {
			continue
		}
		s.executePendingCreate(ctx, p)
	}
}

func (s *SessionCommandService) executeCreate(ctx context.Context, cmd SessionCommand) (CommandResult, error) {
	var params CreateParams
	if err := json.Unmarshal(cmd.Params, &params); err != nil {
		return CommandResult{}, StateError{Code: ErrMalformedSplit, Field: "params", Detail: err.Error()}
	}

	ref := cmd.Ref
	if ref.Owner == "" {
		ref.Owner = s.owner
	}
	if ref.Session == "" {
		ref.Session = NewSessionID()
	}
	if err := ref.Session.Validate(); err != nil {
		return CommandResult{}, StateError{Code: ErrInvalidIdentity, Field: "ref.session", Detail: err.Error()}
	}
	if ref.Owner != s.owner {
		return CommandResult{}, StateError{Code: ErrInvalidIdentity, Field: "ref.owner", Detail: "session ref owner does not match catalog owner"}
	}

	displayName := strings.TrimSpace(params.Name)
	if displayName == "" {
		displayName = string(ref.Session)
	}
	if err := model.ValidateSessionName(displayName); err != nil {
		return CommandResult{}, StateError{Code: ErrInvalidIdentity, Field: "name", Detail: err.Error()}
	}

	s.hook(CrashBeforeIntentCommit)

	path := expandPath(params.Cwd)
	var result CommandResult
	err := s.catalog.apply("session/create", func(doc *AppDocument) error {
		// Idempotent replay FIRST, before any lookup by ref or side effect:
		// a command ID that was already durably accepted returns its own
		// exact stored outcome, never a value re-derived by scanning current
		// sessions (that scan is what let a replayed create return an
		// arbitrary unrelated session once multiple creates were in flight).
		if r, ok := findCommandReceipt(doc, cmd.ID); ok {
			return r.DecodeResult(&result)
		}

		// Same session ref may only be created once (a distinct command ID
		// targeting a ref that already exists is not an idempotent replay of
		// THIS command, but must still not create a second session).
		if existing := s.resultFromDocLocked(doc, ref, displayName, path); existing.Accepted {
			result = existing
			return nil
		}

		displayName = s.uniqueDisplayNameLocked(doc, displayName)
		var target SessionRef
		if params.Target != nil {
			target = *params.Target
		}
		if err := placeSessionInWorkspace(doc, params.LayoutID, ref, target, params.Direction, params.NewFirst); err != nil {
			return err
		}

		doc.PendingCreates = append(doc.PendingCreates, PendingCreateRecord{
			IntentID:       cmd.ID,
			Ref:            ref,
			Inserted:       s.opts.Now(),
			Shell:          params.Shell,
			Cwd:            params.Cwd,
			Cols:           params.Cols,
			Rows:           params.Rows,
			DisplayName:    displayName,
			WorktreeBranch: params.WorktreeBranch,
			ScheduleID:     params.ScheduleID,
		})
		result = CommandResult{ID: cmd.ID, Ref: ref, DisplayName: displayName, Path: path, Accepted: true}
		receipt, err := newSuccessReceipt(cmd.ID, "session:"+ActionCreate, ref.MapKey(), nextCommandSeq(doc), s.opts.Now(), result)
		if err != nil {
			return err
		}
		doc.Commands = append(doc.Commands, receipt)
		return nil
	})
	if err != nil {
		return CommandResult{}, err
	}

	s.hook(CrashAfterIntentCommit)
	return result, nil
}

func (s *SessionCommandService) resultFromDocLocked(doc *AppDocument, ref SessionRef, displayName, path string) CommandResult {
	for _, rec := range doc.Sessions {
		if rec.ID == ref.Session {
			return CommandResult{Ref: rec.Ref, DisplayName: rec.Name, Path: path, Accepted: true}
		}
	}
	for _, p := range doc.PendingCreates {
		if p.Ref.Session == ref.Session {
			return CommandResult{Ref: p.Ref, DisplayName: p.DisplayName, Path: path, Accepted: true}
		}
	}
	return CommandResult{DisplayName: displayName, Path: path}
}

// peekReceipt returns the durable result of a previously accepted command
// with this exact ID, if any. It is a read-only lookup; callers that go on
// to mutate state must still guard against a race with a concurrent
// identical command by re-checking (or committing) inside a catalog.apply
// transaction -- see commitSessionReceipt.
func (s *SessionCommandService) peekReceipt(id CommandID) (CommandResult, bool, error) {
	r, ok, err := s.catalog.CommandReceipt(id)
	if err != nil {
		// Durability of a prior write is uncertain: fail closed rather than
		// treat any receipt as present/accepted. ok=true forces callers'
		// "if result, ok, err := s.peekReceipt(...); ok { return result, err }"
		// pattern to return this error instead of falling through to redo
		// (or re-derive a possibly different answer for) the side effect.
		return CommandResult{}, true, err
	}
	if !ok {
		return CommandResult{}, false, nil
	}
	var result CommandResult
	if err := r.DecodeResult(&result); err != nil {
		return CommandResult{}, true, err
	}
	return result, true, nil
}

// commitSessionReceipt durably records result as the outcome of cmd,
// atomically re-checking for a receipt written by a concurrent identical
// command first. If a concurrent call already committed a receipt for this
// exact command ID, that stored result is returned instead of overwriting it
// -- this keeps the return value correct (never "wrong session"/duplicate
// data) even in the narrow window where two racing identical requests both
// passed peekReceipt's earlier check before either had committed. It does
// NOT undo an external side effect (e.g. backend.Terminate/Start) that may
// have already run for both racers; only session:create's side effects are
// deferred (see PendingCreateRecord), so this race only matters for
// kill/label/recover/dismiss, whose side effects are themselves safe to
// invoke more than once for the same target.
func (s *SessionCommandService) commitSessionReceipt(cmd SessionCommand, kind string, ref SessionRef, result CommandResult) (CommandResult, error) {
	var final CommandResult
	err := s.catalog.apply("session/receipt-"+cmd.Action, func(doc *AppDocument) error {
		if r, ok := findCommandReceipt(doc, cmd.ID); ok {
			return r.DecodeResult(&final)
		}
		receipt, err := newSuccessReceipt(cmd.ID, kind, ref.MapKey(), nextCommandSeq(doc), s.opts.Now(), result)
		if err != nil {
			return err
		}
		doc.Commands = append(doc.Commands, receipt)
		final = result
		return nil
	})
	if err != nil {
		return CommandResult{}, err
	}
	return final, nil
}

func (s *SessionCommandService) uniqueDisplayNameLocked(doc *AppDocument, name string) string {
	used := make(map[string]struct{}, len(doc.Sessions)+len(doc.PendingCreates))
	for _, rec := range doc.Sessions {
		if n := rec.Name; n != "" {
			used[n] = struct{}{}
		}
		used[string(rec.ID)] = struct{}{}
	}
	for _, p := range doc.PendingCreates {
		if n := p.DisplayName; n != "" {
			used[n] = struct{}{}
		}
	}
	candidate := name
	for i := 2; ; i++ {
		if _, exists := used[candidate]; !exists {
			return candidate
		}
		candidate = fmt.Sprintf("%s-%d", name, i)
	}
}

func (s *SessionCommandService) executePendingCreate(ctx context.Context, p PendingCreateRecord) {
	cwd := expandPath(p.Cwd)
	if p.WorktreeBranch != "" {
		base := cwd
		sanitized := sanitizeWorktreeBranch(p.WorktreeBranch)
		worktreePath := filepath.Join(base, ".worktrees", sanitized)
		if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
			if err := git.CreateWorktree(base, p.WorktreeBranch, worktreePath); err != nil {
				s.failPendingCreate(p, err)
				return
			}
		}
		cwd = worktreePath
		s.hook(CrashAfterWorktreePrepare)
	}

	// If a prior attempt already spawned the daemon (e.g. crash after ready),
	// adopt it instead of spawning a duplicate.
	probeBinding := pty.StableBinding{
		Owner:     string(p.Ref.Owner),
		SessionID: string(p.Ref.Session),
		DaemonKey: string(p.Ref.Session),
	}
	var info pty.ReadyInfo
	if ev := s.backend.Probe(probeBinding); ev.Status == pty.ProbeLive {
		info = pty.ReadyInfo{DaemonPID: ev.DaemonPID, ShellPID: ev.ShellPID, Generation: ev.Binding.Generation}
	} else {
		if s.opts.BeforeStartHook != nil {
			s.opts.BeforeStartHook(p)
		}
		var err error
		info, err = s.backend.Start(ctx, pty.StartRequest{
			StableBinding: probeBinding,
			Shell:         p.Shell,
			Cwd:           cwd,
			Cols:          p.Cols,
			Rows:          p.Rows,
		})
		if s.opts.AfterStartHook != nil {
			s.opts.AfterStartHook(p, info, err)
		}
		if err != nil {
			s.failPendingCreate(p, err)
			return
		}
	}

	s.hook(CrashAfterDaemonReady)
	s.hook(CrashBeforeRunningCommit)

	now := s.opts.Now()
	err := s.catalog.apply("session/create-running", func(doc *AppDocument) error {
		// Remove the pending intent.
		filtered := doc.PendingCreates[:0]
		for _, x := range doc.PendingCreates {
			if x.IntentID != p.IntentID {
				filtered = append(filtered, x)
			}
		}
		doc.PendingCreates = filtered

		rec := LocalSessionRecord{
			ID:         p.Ref.Session,
			Owner:      p.Ref.Owner,
			Ref:        p.Ref,
			Phase:      SessionPhaseActive,
			Desired:    DesiredRun,
			Created:    now,
			Name:       p.DisplayName,
			Shell:      p.Shell,
			Cwd:        cwd,
			Cols:       p.Cols,
			Rows:       p.Rows,
			DaemonPID:  info.DaemonPID,
			Generation: info.Generation,
			ScheduleID: p.ScheduleID,
		}
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
	})
	if err != nil {
		logrus.WithError(err).WithField("intent", p.IntentID).Error("failed to commit running session")
		return
	}

	s.hook(CrashAfterCommitBeforeReply)
	logrus.WithFields(logrus.Fields{
		"command_id":   p.IntentID,
		"session_id":   p.Ref.Session,
		"display_name": p.DisplayName,
		"generation":   info.Generation,
	}).Info("session create completed")
}

func (s *SessionCommandService) failPendingCreate(p PendingCreateRecord, cause error) {
	logrus.WithError(cause).WithFields(logrus.Fields{
		"command_id": p.IntentID,
		"session_id": p.Ref.Session,
		"retries":    p.Retries,
	}).Warn("pending create failed")

	retries := p.Retries + 1
	if retries > s.opts.MaxCreateRetries {
		_ = s.catalog.apply("session/create-failed", func(doc *AppDocument) error {
			doc.PendingCreates = removePendingByID(doc.PendingCreates, p.IntentID)
			rec := LocalSessionRecord{
				ID:      p.Ref.Session,
				Owner:   p.Ref.Owner,
				Ref:     p.Ref,
				Phase:   SessionPhaseDismissed,
				Desired: DesiredStop,
				Created: s.opts.Now(),
				Name:    p.DisplayName,
				Shell:   p.Shell,
				Cwd:     p.Cwd,
				Cols:    p.Cols,
				Rows:    p.Rows,
			}
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
		})
		if p.WorktreeBranch != "" {
			s.cleanupWorktree(p.Cwd, p.WorktreeBranch)
		}
		return
	}

	backoff := s.opts.RetryInitial * time.Duration(1<<minInt(retries, 10))
	if backoff > s.opts.RetryMax {
		backoff = s.opts.RetryMax
	}
	p.Retries = retries
	p.NextAttempt = s.opts.Now().Add(backoff)
	_ = s.catalog.PutPendingCreate(p)
}

func (s *SessionCommandService) executeKill(ctx context.Context, cmd SessionCommand) (CommandResult, error) {
	ref := cmd.Ref
	if err := ref.Session.Validate(); err != nil {
		return CommandResult{}, StateError{Code: ErrInvalidIdentity, Field: "ref.session", Detail: err.Error()}
	}

	var params KillParams
	if len(cmd.Params) > 0 {
		if err := json.Unmarshal(cmd.Params, &params); err != nil {
			return CommandResult{}, StateError{Code: ErrMalformedSplit, Field: "params", Detail: err.Error()}
		}
	}

	// Idempotent replay: return the original outcome before re-issuing
	// termination, worktree removal, or touching catalog state.
	if result, ok, err := s.peekReceipt(cmd.ID); ok {
		return result, err
	}

	if rec, ok := s.catalog.Session(ref.Session); ok {
		if rec.Phase == SessionPhaseCleanlyEnded || rec.Phase == SessionPhaseDismissed {
			if params.RemoveWorktree {
				s.removeWorktreeForCwd(rec.Cwd)
			}
			return s.commitSessionReceipt(cmd, "session:"+ActionKill, ref, CommandResult{ID: cmd.ID, Ref: ref, Accepted: true})
		}
		// Capture the canonical CWD BEFORE the daemon termination side effect so
		// worktree removal always targets the session's real working directory,
		// not one re-derived after the record may have changed.
		cwd := rec.Cwd
		// Persist stop intent before issuing exact-generation termination.
		rec.Desired = DesiredStop
		if err := s.catalog.PutSession(rec); err != nil {
			return CommandResult{}, err
		}
		binding := bindingForRecord(&rec)
		outcome := s.backend.Terminate(ctx, binding)
		logrus.WithFields(logrus.Fields{
			"command_id": cmd.ID,
			"session_id": ref.Session,
			"generation": rec.Generation,
			"outcome":    outcome,
		}).Info("session kill issued")
		if params.RemoveWorktree {
			s.removeWorktreeForCwd(cwd)
		}
		return s.commitSessionReceipt(cmd, "session:"+ActionKill, ref, CommandResult{ID: cmd.ID, Ref: ref, Accepted: true})
	}

	// A pending create can be cancelled before work starts.
	if pending, ok := s.findPending(ref.Session); ok {
		_ = s.catalog.apply("session/kill-pending", func(doc *AppDocument) error {
			doc.PendingCreates = removePendingByID(doc.PendingCreates, pending.IntentID)
			return removeSessionFromWorkspacesLocked(doc, ref)
		})
		if pending.WorktreeBranch != "" {
			s.cleanupWorktree(pending.Cwd, pending.WorktreeBranch)
		}
		if params.RemoveWorktree {
			s.removeWorktreeForCwd(pending.Cwd)
		}
		return s.commitSessionReceipt(cmd, "session:"+ActionKill, ref, CommandResult{ID: cmd.ID, Ref: ref, Accepted: true})
	}

	return CommandResult{}, StateError{Code: ErrUnknownLayout, Field: "ref.session", Detail: fmt.Sprintf("session %q not found", ref.Session)}
}

// removeWorktreeForCwd removes the git worktree at cwd itself (the session's
// own working directory), as opposed to cleanupWorktree, which removes a
// branch-derived `.worktrees/<branch>` child directory beneath a create's
// base cwd. KillParams.RemoveWorktree targets an already-running session's
// existing cwd directly, so no branch/base-path derivation applies. Best
// effort: failures are logged, never returned, so a kill is never blocked or
// reported as failed by a worktree that is already gone or dirty.
func (s *SessionCommandService) removeWorktreeForCwd(cwd string) {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return
	}
	cwd = expandPath(cwd)
	if _, err := os.Stat(cwd); os.IsNotExist(err) {
		return
	}
	repoRoot := filepath.Dir(filepath.Dir(cwd)) // strip "<repo>/.worktrees/<branch>" -> "<repo>", best-effort
	if err := runGitWorktreeRemove(repoRoot, cwd); err != nil {
		logrus.WithError(err).WithField("path", cwd).Debug("git worktree remove failed, falling back to directory removal")
	}
	if err := os.RemoveAll(cwd); err != nil {
		logrus.WithError(err).WithField("path", cwd).Warn("failed to remove worktree directory on kill")
	}
}

func (s *SessionCommandService) executeSetPresentation(ctx context.Context, cmd SessionCommand) (CommandResult, error) {
	var params PresentationParams
	if err := json.Unmarshal(cmd.Params, &params); err != nil {
		return CommandResult{}, StateError{Code: ErrMalformedSplit, Field: "params", Detail: err.Error()}
	}
	ref := cmd.Ref
	if err := ref.Session.Validate(); err != nil {
		return CommandResult{}, StateError{Code: ErrInvalidIdentity, Field: "ref.session", Detail: err.Error()}
	}

	if result, ok, err := s.peekReceipt(cmd.ID); ok {
		return result, err
	}

	if rec, ok := s.catalog.Session(ref.Session); ok {
		if params.Hidden != nil {
			rec.Hidden = *params.Hidden
		}
		if params.Background != nil {
			rec.Background = *params.Background
		}
		if err := s.catalog.PutSession(rec); err != nil {
			return CommandResult{}, err
		}
		return s.commitSessionReceipt(cmd, "session:"+ActionSetPresentation, ref, CommandResult{ID: cmd.ID, Ref: ref, DisplayName: rec.Name, Accepted: true})
	}

	return CommandResult{}, StateError{Code: ErrUnknownLayout, Field: "ref.session", Detail: fmt.Sprintf("session %q not found", ref.Session)}
}

func (s *SessionCommandService) executeLabel(ctx context.Context, cmd SessionCommand) (CommandResult, error) {
	var params LabelParams
	if err := json.Unmarshal(cmd.Params, &params); err != nil {
		return CommandResult{}, StateError{Code: ErrMalformedSplit, Field: "params", Detail: err.Error()}
	}
	label := strings.TrimSpace(params.Label)
	if label == "" {
		return CommandResult{}, StateError{Code: ErrInvalidIdentity, Field: "label", Detail: "label cannot be empty"}
	}
	if err := model.ValidateSessionName(label); err != nil {
		return CommandResult{}, StateError{Code: ErrInvalidIdentity, Field: "label", Detail: err.Error()}
	}
	ref := cmd.Ref
	if err := ref.Session.Validate(); err != nil {
		return CommandResult{}, StateError{Code: ErrInvalidIdentity, Field: "ref.session", Detail: err.Error()}
	}

	if result, ok, err := s.peekReceipt(cmd.ID); ok {
		return result, err
	}

	if rec, ok := s.catalog.Session(ref.Session); ok {
		rec.Name = label
		if err := s.catalog.PutSession(rec); err != nil {
			return CommandResult{}, err
		}
		return s.commitSessionReceipt(cmd, "session:"+ActionLabel, ref, CommandResult{ID: cmd.ID, Ref: ref, DisplayName: label, Accepted: true})
	}

	if pending, ok := s.findPending(ref.Session); ok {
		pending.DisplayName = label
		if err := s.catalog.PutPendingCreate(pending); err != nil {
			return CommandResult{}, err
		}
		return s.commitSessionReceipt(cmd, "session:"+ActionLabel, ref, CommandResult{ID: cmd.ID, Ref: ref, DisplayName: label, Accepted: true})
	}

	return CommandResult{}, StateError{Code: ErrUnknownLayout, Field: "ref.session", Detail: fmt.Sprintf("session %q not found", ref.Session)}
}

func (s *SessionCommandService) executeRecover(ctx context.Context, cmd SessionCommand) (CommandResult, error) {
	var params RecoverParams
	_ = json.Unmarshal(cmd.Params, &params) // empty body is allowed

	ref := cmd.Ref
	if err := ref.Session.Validate(); err != nil {
		return CommandResult{}, StateError{Code: ErrInvalidIdentity, Field: "ref.session", Detail: err.Error()}
	}

	if result, ok, err := s.peekReceipt(cmd.ID); ok {
		return result, err
	}

	rec, ok := s.catalog.Session(ref.Session)
	if !ok {
		return CommandResult{}, StateError{Code: ErrUnknownLayout, Field: "ref.session", Detail: fmt.Sprintf("session %q not found", ref.Session)}
	}
	if rec.Phase != SessionPhaseCrashed {
		return CommandResult{}, StateError{Code: ErrMalformedSplit, Field: "phase", Detail: fmt.Sprintf("session is %q, not crashed", rec.Phase)}
	}

	shell := rec.Shell
	cwd := rec.Cwd
	if params.Shell != "" {
		shell = params.Shell
	}
	if params.Cwd != "" {
		cwd = params.Cwd
	}
	cwd = expandPath(cwd)

	info, err := s.backend.Start(ctx, pty.StartRequest{
		StableBinding: pty.StableBinding{
			Owner:     string(ref.Owner),
			SessionID: string(ref.Session),
			DaemonKey: string(ref.Session),
		},
		Shell: shell,
		Cwd:   cwd,
		Cols:  rec.Cols,
		Rows:  rec.Rows,
	})
	if err != nil {
		return CommandResult{}, err
	}

	rec.Phase = SessionPhaseActive
	rec.Desired = DesiredRun
	rec.Generation = info.Generation
	rec.DaemonPID = info.DaemonPID
	if err := s.catalog.PutSession(rec); err != nil {
		return CommandResult{}, err
	}

	return s.commitSessionReceipt(cmd, "session:"+ActionRecover, ref, CommandResult{ID: cmd.ID, Ref: ref, DisplayName: rec.Name, Accepted: true})
}

func (s *SessionCommandService) executeDismiss(ctx context.Context, cmd SessionCommand) (CommandResult, error) {
	ref := cmd.Ref
	if err := ref.Session.Validate(); err != nil {
		return CommandResult{}, StateError{Code: ErrInvalidIdentity, Field: "ref.session", Detail: err.Error()}
	}

	if result, ok, err := s.peekReceipt(cmd.ID); ok {
		return result, err
	}

	if pending, ok := s.findPending(ref.Session); ok && pending.WorktreeBranch != "" {
		s.cleanupWorktree(pending.Cwd, pending.WorktreeBranch)
	}

	if err := s.removeSessionAndRefs(ref.Session, ref); err != nil {
		return CommandResult{}, err
	}
	return s.commitSessionReceipt(cmd, "session:"+ActionDismiss, ref, CommandResult{ID: cmd.ID, Ref: ref, Accepted: true})
}

func (s *SessionCommandService) executeRetry(ctx context.Context, cmd SessionCommand) (CommandResult, error) {
	ref := cmd.Ref
	if err := ref.Session.Validate(); err != nil {
		return CommandResult{}, StateError{Code: ErrInvalidIdentity, Field: "ref.session", Detail: err.Error()}
	}

	rec, ok := s.catalog.Session(ref.Session)
	if !ok {
		return CommandResult{}, StateError{Code: ErrUnknownLayout, Field: "ref.session", Detail: fmt.Sprintf("session %q not found", ref.Session)}
	}

	var result CommandResult
	err := s.catalog.apply("session/retry", func(doc *AppDocument) error {
		// Idempotent replay FIRST: a retried "retry" command returns its own
		// original stored outcome, before re-checking phase or re-queuing a
		// second pending create for the same intent.
		if r, ok := findCommandReceipt(doc, cmd.ID); ok {
			return r.DecodeResult(&result)
		}
		if rec.Phase != SessionPhaseDismissed && rec.Phase != SessionPhaseCrashed {
			return StateError{Code: ErrMalformedSplit, Field: "phase", Detail: fmt.Sprintf("session is %q, cannot retry", rec.Phase)}
		}
		doc.PendingCreates = removePendingByID(doc.PendingCreates, cmd.ID)
		doc.PendingCreates = append(doc.PendingCreates, PendingCreateRecord{
			IntentID:       cmd.ID,
			Ref:            rec.Ref,
			Inserted:       s.opts.Now(),
			Shell:          rec.Shell,
			Cwd:            rec.Cwd,
			Cols:           rec.Cols,
			Rows:           rec.Rows,
			DisplayName:    rec.Name,
			WorktreeBranch: "", // original branch context is gone; caller can recover with explicit cwd
		})
		// Keep the logical session record as dismissed until the worker succeeds.
		result = CommandResult{ID: cmd.ID, Ref: ref, DisplayName: rec.Name, Accepted: true}
		receipt, err := newSuccessReceipt(cmd.ID, "session:"+ActionRetry, ref.MapKey(), nextCommandSeq(doc), s.opts.Now(), result)
		if err != nil {
			return err
		}
		doc.Commands = append(doc.Commands, receipt)
		return nil
	})
	if err != nil {
		return CommandResult{}, err
	}
	return result, nil
}

func (s *SessionCommandService) removeSessionAndRefs(id SessionID, ref SessionRef) error {
	return s.catalog.apply("session/remove", func(doc *AppDocument) error {
		doc.Sessions = removeSessionByID(doc.Sessions, id)
		return removeSessionFromWorkspacesLocked(doc, ref)
	})
}

func (s *SessionCommandService) findPending(session SessionID) (PendingCreateRecord, bool) {
	for _, p := range s.catalog.PendingCreates() {
		if p.Ref.Session == session {
			return p, true
		}
	}
	return PendingCreateRecord{}, false
}

func (s *SessionCommandService) cleanupWorktree(cwd, branch string) {
	base := expandPath(cwd)
	sanitized := sanitizeWorktreeBranch(branch)
	path := filepath.Join(base, ".worktrees", sanitized)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return
	}
	// Best-effort git worktree removal keeps the main repo registry clean.
	if err := runGitWorktreeRemove(base, path); err != nil {
		logrus.WithError(err).WithField("path", path).Debug("git worktree remove failed, falling back to directory removal")
	}
	// Always remove the directory so a failed create does not leave an orphan.
	if err := os.RemoveAll(path); err != nil {
		logrus.WithError(err).WithField("path", path).Warn("failed to remove failed-create worktree directory")
	}
}

func runGitWorktreeRemove(repoRoot, worktreePath string) error {
	args := []string{"-C", repoRoot, "worktree", "remove", worktreePath}
	cmd := exec.Command("git", args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// Retry with force for dirty worktrees.
		cmd2 := exec.Command("git", "-C", repoRoot, "worktree", "remove", "--force", worktreePath)
		cmd2.Stderr = &stderr
		if err2 := cmd2.Run(); err2 != nil {
			return errors.New(stderr.String())
		}
	}
	return nil
}

func (s *SessionCommandService) hook(p CrashPoint) {
	if s.opts.CrashHook != nil {
		s.opts.CrashHook(p)
	}
}

// placeSessionInWorkspace adds ref to the requested layout, creating a default
// layout when none exists. When target names an existing leaf in layoutID,
// ref is placed by splitting that leaf in direction (mirroring
// RemoteCreateCoordinator.placeRemoteRefLocked in remote_create.go) instead
// of using the default "first free leaf" heuristic -- this is what makes
// "create a session as a split next to X" one atomic placement instead of a
// separate create-then-split that could race with, or duplicate, the
// create's own default placement.
func placeSessionInWorkspace(doc *AppDocument, layoutID LayoutID, ref SessionRef, target SessionRef, direction SplitDirection, newFirst bool) error {
	if layoutID != "" {
		for i := range doc.Layouts {
			if doc.Layouts[i].ID == layoutID {
				if target.Session != "" {
					if !findLeaf(doc.Layouts[i].Tree, target) {
						return StateError{Code: ErrMissingTarget, Field: "target", Detail: fmt.Sprintf("target leaf %q not in layout", target.MapKey())}
					}
					if key := ref.MapKey(); findLeaf(doc.Layouts[i].Tree, ref) {
						return StateError{Code: ErrDuplicateLeaf, Field: "target", Detail: fmt.Sprintf("duplicate leaf %q", key)}
					}
					tree, err := splitTree(doc.Layouts[i].Tree, target, direction, ref, newFirst)
					if err != nil {
						return err
					}
					doc.Layouts[i].Tree = tree
					doc.Layouts[i].Revision = doc.Revision + 1
					return nil
				}
				return addLeafToLayout(doc, &doc.Layouts[i], ref)
			}
		}
		return StateError{Code: ErrUnknownLayout, Field: "layout_id", Detail: fmt.Sprintf("layout %q not found", layoutID)}
	}

	if len(doc.Layouts) == 0 {
		doc.Layouts = append(doc.Layouts, LayoutRecord{
			ID:       NewLayoutID(),
			Owner:    doc.Owner,
			Order:    1,
			Revision: doc.Revision + 1,
			Tree:     Leaf(ref),
		})
		return nil
	}

	return addLeafToLayout(doc, &doc.Layouts[0], ref)
}

func addLeafToLayout(doc *AppDocument, rec *LayoutRecord, ref SessionRef) error {
	if findLeaf(rec.Tree, ref) {
		return nil
	}
	target := findAnyLeaf(rec.Tree)
	if target == nil {
		rec.Tree = Leaf(ref)
		rec.Revision = doc.Revision + 1
		return nil
	}
	tree, err := splitTree(rec.Tree, *target, DirectionHorizontal, ref, false)
	if err != nil {
		return err
	}
	rec.Tree = tree
	rec.Revision = doc.Revision + 1
	return nil
}

func findAnyLeaf(tree PaneNode) *SessionRef {
	if tree.IsLeaf() {
		if tree.Ref != nil {
			r := *tree.Ref
			return &r
		}
		return nil
	}
	if tree.First != nil {
		if ref := findAnyLeaf(*tree.First); ref != nil {
			return ref
		}
	}
	if tree.Second != nil {
		return findAnyLeaf(*tree.Second)
	}
	return nil
}

func nextCommandSeq(doc *AppDocument) int64 {
	seq := int64(1)
	for _, r := range doc.Commands {
		if r.Seq >= seq {
			seq = r.Seq + 1
		}
	}
	return seq
}

func removePendingByID(list []PendingCreateRecord, id CommandID) []PendingCreateRecord {
	filtered := list[:0]
	for _, x := range list {
		if x.IntentID != id {
			filtered = append(filtered, x)
		}
	}
	return filtered
}

func removeSessionByID(list []LocalSessionRecord, id SessionID) []LocalSessionRecord {
	filtered := list[:0]
	for _, x := range list {
		if x.ID != id {
			filtered = append(filtered, x)
		}
	}
	return filtered
}

func sanitizeWorktreeBranch(value string) string {
	return strings.ReplaceAll(strings.TrimSpace(value), "/", "-")
}

func expandPath(p string) string {
	if strings.HasPrefix(p, "~") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			p = home + p[1:]
		}
	}
	if !filepath.IsAbs(p) {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			p = filepath.Join(home, p)
		}
	}
	return p
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func commandIDFromBytes(b []byte) CommandID {
	h := sha256.Sum256(b)
	return CommandID(strings.ToLower(idEncoding.EncodeToString(h[:16])))
}

// NewCommandIDFromSchedule derives a stable command ID from the schedule
// identity, owner, and fire time. The result is a base32-encoded digest so it
// satisfies the same validation rules as generated IDs.
func NewCommandIDFromSchedule(owner OwnerID, scheduleID string, fireTime time.Time) CommandID {
	var b []byte
	b = append(b, []byte(owner)...)
	b = append(b, []byte(scheduleID)...)
	b = append(b, []byte(fireTime.UTC().Format(time.RFC3339Nano))...)
	return commandIDFromBytes(b)
}
