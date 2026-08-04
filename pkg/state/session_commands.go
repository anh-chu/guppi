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
	ActionCreate  = "create"
	ActionKill    = "kill"
	ActionLabel   = "label"
	ActionRecover = "recover"
	ActionDismiss = "dismiss"
	ActionRetry   = "retry"
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

// CreateParams carries a local create request.
type CreateParams struct {
	Name           string   `json:"name,omitempty"`
	Shell          string   `json:"shell,omitempty"`
	Cwd            string   `json:"cwd,omitempty"`
	WorktreeBranch string   `json:"worktree_branch,omitempty"`
	Cols           uint16   `json:"cols,omitempty"`
	Rows           uint16   `json:"rows,omitempty"`
	LayoutID       LayoutID `json:"layout_id,omitempty"`
	AgentType      string   `json:"agent_type,omitempty"`
}

// RecoverParams overrides the saved shell/cwd for crash recovery.
type RecoverParams struct {
	Shell string `json:"shell,omitempty"`
	Cwd   string `json:"cwd,omitempty"`
}

// LabelParams updates the mutable display label for a session.
type LabelParams struct {
	Label string `json:"label"`
}

// CommandResult acknowledges an accepted command and gives the caller the
// stable identity that was durably committed.
type CommandResult struct {
	ID          CommandID
	Ref         SessionRef
	DisplayName string
	Path        string
	Accepted    bool
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
	default:
		return CommandResult{}, StateError{Code: ErrMalformedSplit, Field: "action", Detail: fmt.Sprintf("unknown session action %q", cmd.Action)}
	}
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

// LookupRefByDisplayName returns the canonical session ref for a display name
// or session id. It is used by v1 route adapters that only know the UI label.
func (s *SessionCommandService) LookupRefByDisplayName(name string) (SessionRef, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return SessionRef{}, false
	}
	for _, rec := range s.catalog.Sessions() {
		if rec.Compat.Name == name || string(rec.ID) == name {
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
		// Idempotent replay: same command ID accepted once.
		for _, r := range doc.Commands {
			if r.ID == cmd.ID {
				result = s.existingResultFromDocLocked(doc, cmd.ID)
				if result.Accepted {
					return nil
				}
				return StateError{Code: ErrDuplicateIdentity, Field: "id", Detail: fmt.Sprintf("command %q accepted for a different session", cmd.ID)}
			}
		}

		// Same session ref may only be created once.
		result = s.resultFromDocLocked(doc, ref, displayName, path)
		if result.Accepted {
			return nil
		}

		displayName = s.uniqueDisplayNameLocked(doc, displayName)
		if err := placeSessionInWorkspace(doc, params.LayoutID, ref); err != nil {
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
		})
		doc.Commands = append(doc.Commands, CommandReceipt{
			ID:       cmd.ID,
			IntentID: cmd.ID,
			Seq:      nextCommandSeq(doc),
			Created:  s.opts.Now(),
		})
		result = CommandResult{ID: cmd.ID, Ref: ref, DisplayName: displayName, Path: path, Accepted: true}
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
			return CommandResult{Ref: rec.Ref, DisplayName: rec.Compat.Name, Path: path, Accepted: true}
		}
	}
	for _, p := range doc.PendingCreates {
		if p.Ref.Session == ref.Session {
			return CommandResult{Ref: p.Ref, DisplayName: p.DisplayName, Path: path, Accepted: true}
		}
	}
	return CommandResult{DisplayName: displayName, Path: path}
}

func (s *SessionCommandService) existingResultFromDocLocked(doc *AppDocument, id CommandID) CommandResult {
	for _, p := range doc.PendingCreates {
		if p.IntentID == id {
			return CommandResult{Ref: p.Ref, DisplayName: p.DisplayName, Path: expandPath(p.Cwd), Accepted: true}
		}
	}
	for _, rec := range doc.Sessions {
		for _, r := range doc.Commands {
			if r.ID == id {
				return CommandResult{Ref: rec.Ref, DisplayName: rec.Compat.Name, Path: expandPath(rec.Compat.Cwd), Accepted: true}
			}
		}
	}
	return CommandResult{}
}

func (s *SessionCommandService) uniqueDisplayNameLocked(doc *AppDocument, name string) string {
	used := make(map[string]struct{}, len(doc.Sessions)+len(doc.PendingCreates))
	for _, rec := range doc.Sessions {
		if n := rec.Compat.Name; n != "" {
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
			ID:      p.Ref.Session,
			Owner:   p.Ref.Owner,
			Ref:     p.Ref,
			Phase:   SessionPhaseActive,
			Desired: DesiredRun,
			Created: now,
			Compat: CompatLocalSession{
				Name:       p.DisplayName,
				Shell:      p.Shell,
				Cwd:        cwd,
				Cols:       p.Cols,
				Rows:       p.Rows,
				DaemonPID:  info.DaemonPID,
				Generation: info.Generation,
			},
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
				Compat: CompatLocalSession{
					Name:  p.DisplayName,
					Shell: p.Shell,
					Cwd:   p.Cwd,
					Cols:  p.Cols,
					Rows:  p.Rows,
				},
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

	if rec, ok := s.catalog.Session(ref.Session); ok {
		if rec.Phase == SessionPhaseCleanlyEnded || rec.Phase == SessionPhaseDismissed {
			return CommandResult{ID: cmd.ID, Ref: ref, Accepted: true}, nil
		}
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
			"generation": rec.Compat.Generation,
			"outcome":    outcome,
		}).Info("session kill issued")
		return CommandResult{ID: cmd.ID, Ref: ref, Accepted: true}, nil
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
		return CommandResult{ID: cmd.ID, Ref: ref, Accepted: true}, nil
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

	if rec, ok := s.catalog.Session(ref.Session); ok {
		rec.Compat.Name = label
		if err := s.catalog.PutSession(rec); err != nil {
			return CommandResult{}, err
		}
		return CommandResult{ID: cmd.ID, Ref: ref, DisplayName: label, Accepted: true}, nil
	}

	if pending, ok := s.findPending(ref.Session); ok {
		pending.DisplayName = label
		if err := s.catalog.PutPendingCreate(pending); err != nil {
			return CommandResult{}, err
		}
		return CommandResult{ID: cmd.ID, Ref: ref, DisplayName: label, Accepted: true}, nil
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

	rec, ok := s.catalog.Session(ref.Session)
	if !ok {
		return CommandResult{}, StateError{Code: ErrUnknownLayout, Field: "ref.session", Detail: fmt.Sprintf("session %q not found", ref.Session)}
	}
	if rec.Phase != SessionPhaseCrashed {
		return CommandResult{}, StateError{Code: ErrMalformedSplit, Field: "phase", Detail: fmt.Sprintf("session is %q, not crashed", rec.Phase)}
	}

	shell := rec.Compat.Shell
	cwd := rec.Compat.Cwd
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
		Cols:  rec.Compat.Cols,
		Rows:  rec.Compat.Rows,
	})
	if err != nil {
		return CommandResult{}, err
	}

	rec.Phase = SessionPhaseActive
	rec.Desired = DesiredRun
	rec.Compat.Generation = info.Generation
	rec.Compat.DaemonPID = info.DaemonPID
	if err := s.catalog.PutSession(rec); err != nil {
		return CommandResult{}, err
	}

	return CommandResult{ID: cmd.ID, Ref: ref, DisplayName: rec.Compat.Name, Accepted: true}, nil
}

func (s *SessionCommandService) executeDismiss(ctx context.Context, cmd SessionCommand) (CommandResult, error) {
	ref := cmd.Ref
	if err := ref.Session.Validate(); err != nil {
		return CommandResult{}, StateError{Code: ErrInvalidIdentity, Field: "ref.session", Detail: err.Error()}
	}

	if pending, ok := s.findPending(ref.Session); ok && pending.WorktreeBranch != "" {
		s.cleanupWorktree(pending.Cwd, pending.WorktreeBranch)
	}

	if err := s.removeSessionAndRefs(ref.Session, ref); err != nil {
		return CommandResult{}, err
	}
	return CommandResult{ID: cmd.ID, Ref: ref, Accepted: true}, nil
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
	if rec.Phase != SessionPhaseDismissed && rec.Phase != SessionPhaseCrashed {
		return CommandResult{}, StateError{Code: ErrMalformedSplit, Field: "phase", Detail: fmt.Sprintf("session is %q, cannot retry", rec.Phase)}
	}

	err := s.catalog.apply("session/retry", func(doc *AppDocument) error {
		doc.PendingCreates = removePendingByID(doc.PendingCreates, cmd.ID)
		doc.PendingCreates = append(doc.PendingCreates, PendingCreateRecord{
			IntentID:       cmd.ID,
			Ref:            rec.Ref,
			Inserted:       s.opts.Now(),
			Shell:          rec.Compat.Shell,
			Cwd:            rec.Compat.Cwd,
			Cols:           rec.Compat.Cols,
			Rows:           rec.Compat.Rows,
			DisplayName:    rec.Compat.Name,
			WorktreeBranch: "", // original branch context is gone; caller can recover with explicit cwd
		})
		// Keep the logical session record as dismissed until the worker succeeds.
		return nil
	})
	if err != nil {
		return CommandResult{}, err
	}
	return CommandResult{ID: cmd.ID, Ref: ref, DisplayName: rec.Compat.Name, Accepted: true}, nil
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
// layout when none exists.
func placeSessionInWorkspace(doc *AppDocument, layoutID LayoutID, ref SessionRef) error {
	if layoutID != "" {
		for i := range doc.Layouts {
			if doc.Layouts[i].ID == layoutID {
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
