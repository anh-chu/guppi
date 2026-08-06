package state

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/anh-chu/termyard/pkg/git"
	"github.com/anh-chu/termyard/pkg/model"
	"github.com/anh-chu/termyard/pkg/pty"
)

// RemoteCreateCrashPoint labels the exact phases where tests may inject a
// fault for the remote-create coordinator. They are intentionally generic so
// the same test harness covers process crashes, panics, and context
// cancellation.
type RemoteCreateCrashPoint string

const (
	CrashBeforePendingCommit  RemoteCreateCrashPoint = "before_pending_commit"
	CrashBeforeSend           RemoteCreateCrashPoint = "before_send"
	CrashOwnerAcceptedNoReply RemoteCreateCrashPoint = "owner_accepted_no_reply"
	CrashOwnerStarting        RemoteCreateCrashPoint = "owner_starting"
	CrashOwnerRunning         RemoteCreateCrashPoint = "owner_running"
	CrashPendingCleanup       RemoteCreateCrashPoint = "pending_cleanup"
	CrashCoordinatorRestart   RemoteCreateCrashPoint = "coordinator_restart"
)

// RemoteCreateStatus is the persisted status of a remote create saga.
type RemoteCreateStatus string

const (
	RemoteCreateStatusPending   RemoteCreateStatus = "pending"
	RemoteCreateStatusAccepted  RemoteCreateStatus = "accepted"
	RemoteCreateStatusRunning   RemoteCreateStatus = "running"
	RemoteCreateStatusFailed    RemoteCreateStatus = "failed"
	RemoteCreateStatusCompleted RemoteCreateStatus = "completed"
)

// RemoteCreateRequest asks the workspace owner to allocate a stable ref, place
// it in the singleton workspace, and start the daemon. It is the only distributed
// command request type that mutates shared workspace state.
type RemoteCreateRequest struct {
	IntentID       CommandID      `json:"intent_id"`
	Requester      OwnerID        `json:"requester"`
	Name           string         `json:"name,omitempty"`
	Shell          string         `json:"shell,omitempty"`
	Cwd            string         `json:"cwd,omitempty"`
	WorktreeBranch string         `json:"worktree_branch,omitempty"`
	Cols           uint16         `json:"cols,omitempty"`
	Rows           uint16         `json:"rows,omitempty"`
	Target         *SessionRef    `json:"target,omitempty"`
	Direction      SplitDirection `json:"direction,omitempty"`
	NewFirst       bool           `json:"new_first,omitempty"`
	AgentType      string         `json:"agent_type,omitempty"`
	// ScheduleID mirrors CreateParams.ScheduleID for a remote create; it is
	// carried through to PendingRemoteCreateRecord.ScheduleID.
	ScheduleID string `json:"schedule_id,omitempty"`
}

// RemoteCreateResult acknowledges an accepted remote create and gives the
// caller the stable identity that was durably committed by the owner.
type RemoteCreateResult struct {
	ID          CommandID  `json:"id"`
	Ref         SessionRef `json:"ref"`
	DisplayName string     `json:"display_name,omitempty"`
	Path        string     `json:"path,omitempty"`
	Degraded    bool       `json:"degraded,omitempty"`
	Accepted    bool       `json:"accepted"`
}

// RemoteCreateCoordinatorOptions configures timing, retry policy, and test
// fault injection for the remote-create coordinator.
type RemoteCreateCoordinatorOptions struct {
	Owner           OwnerID
	Tick            time.Duration
	MaxRetries      int
	RetryInitial    time.Duration
	RetryMax        time.Duration
	Now             func() time.Time
	CrashHook       func(RemoteCreateCrashPoint)
	BeforeStartHook func(PendingRemoteCreateRecord)
	AfterStartHook  func(PendingRemoteCreateRecord, pty.ReadyInfo, error)
}

func (o RemoteCreateCoordinatorOptions) withDefaults() RemoteCreateCoordinatorOptions {
	if o.Tick <= 0 {
		o.Tick = 200 * time.Millisecond
	}
	if o.MaxRetries <= 0 {
		o.MaxRetries = MaxCreateRetries
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

// RemoteCreateCoordinator executes remote create requests on the workspace
// owner. It persists the narrow PendingRemoteCreateRecord, resumes after
// crashes, and guarantees a single stable ref for each intent id.
type RemoteCreateCoordinator struct {
	catalog *Catalog
	backend DaemonBackend
	opts    RemoteCreateCoordinatorOptions
	mu      sync.Mutex
}

// NewRemoteCreateCoordinator builds a coordinator bound to one owner catalog.
func NewRemoteCreateCoordinator(catalog *Catalog, backend DaemonBackend, opts RemoteCreateCoordinatorOptions) *RemoteCreateCoordinator {
	opts = opts.withDefaults()
	if opts.Owner == "" {
		opts.Owner = catalog.Owner()
	}
	return &RemoteCreateCoordinator{
		catalog: catalog,
		backend: backend,
		opts:    opts,
	}
}

// ExecuteRemoteCreateFromPeer applies a remote create request that arrived
// over an authenticated peer connection. peerID must be the connection's
// authenticated identity (empty means "trusted local caller", which is left
// to ExecuteRemoteCreate's existing, unchanged behavior). req.Requester
// records who is asking for the create and must equal the authenticated
// sender; a peer cannot claim to be requesting on behalf of a different
// owner. The check runs BEFORE any mutation. Local (non-peer) callers must
// keep calling ExecuteRemoteCreate directly so this check is never applied to
// already-trusted local paths.
func (c *RemoteCreateCoordinator) ExecuteRemoteCreateFromPeer(ctx context.Context, req RemoteCreateRequest, peerID string) (RemoteCreateResult, error) {
	if peerID != "" {
		if req.Requester == "" || req.Requester != OwnerIDFromFingerprint(peerID) {
			return RemoteCreateResult{}, StateError{
				Code:   ErrOwnershipMismatch,
				Field:  "requester",
				Detail: fmt.Sprintf("requester %q does not match authenticated peer %q", req.Requester, peerID),
			}
		}
	}
	return c.ExecuteRemoteCreate(ctx, req)
}

// ExecuteRemoteCreate applies one remote create request. Accepted requests are
// durable before the method returns; daemon work is driven by Run.
func (c *RemoteCreateCoordinator) ExecuteRemoteCreate(ctx context.Context, req RemoteCreateRequest) (RemoteCreateResult, error) {
	if err := req.IntentID.Validate(); err != nil {
		return RemoteCreateResult{}, StateError{Code: ErrInvalidIdentity, Field: "intent_id", Detail: err.Error()}
	}
	if req.Requester != "" && req.Requester != c.opts.Owner {
		// Local node is the workspace authority but the request came from a
		// different owner. Accept it; that is the normal proxy case.
	}

	displayName := strings.TrimSpace(req.Name)
	if displayName == "" {
		displayName = string(NewSessionID())
	}
	if err := model.ValidateSessionName(displayName); err != nil {
		return RemoteCreateResult{}, StateError{Code: ErrInvalidIdentity, Field: "name", Detail: err.Error()}
	}

	path := expandPath(req.Cwd)
	var result RemoteCreateResult
	err := c.catalog.apply("remote-create/intent", func(doc *AppDocument) error {
		// Idempotent replay FIRST, before any lookup or side effect: a
		// command ID that was already durably accepted returns its own exact
		// stored outcome, never a value re-derived by scanning current
		// sessions (that scan is what let a replayed remote create return an
		// arbitrary unrelated session once multiple creates were in flight).
		if r, ok := findCommandReceipt(doc, req.IntentID); ok {
			return r.DecodeResult(&result)
		}

		ref := SessionRef{Owner: c.opts.Owner, Session: NewSessionID()}
		displayName = c.uniqueDisplayNameLocked(doc, displayName)

		// Place ref in the singleton workspace tree
		if err := c.placeRefInWorkspaceLocked(doc, ref, req); err != nil {
			return err
		}

		now := c.opts.Now()
		doc.PendingRemoteCreates = append(doc.PendingRemoteCreates, PendingRemoteCreateRecord{
			IntentID:       req.IntentID,
			Owner:          c.opts.Owner,
			Requester:      req.Requester,
			Ref:            ref,
			DisplayName:    displayName,
			Shell:          req.Shell,
			Cwd:            req.Cwd,
			Cols:           req.Cols,
			Rows:           req.Rows,
			Status:         string(RemoteCreateStatusPending),
			Inserted:       now,
			UpdatedAt:      now,
			WorktreeBranch: req.WorktreeBranch,
			ScheduleID:     req.ScheduleID,
			AgentType:      req.AgentType,
		})
		doc.Sessions = append(doc.Sessions, LocalSessionRecord{
			ID:             ref.Session,
			Owner:          c.opts.Owner,
			Ref:            ref,
			Phase:          SessionPhasePending,
			Desired:        DesiredRun,
			Created:        now,
			Name:           displayName,
			Shell:          req.Shell,
			Cwd:            path,
			Cols:           req.Cols,
			Rows:           req.Rows,
			ScheduleID:     req.ScheduleID,
			AgentType:      req.AgentType,
			WorktreeBranch: req.WorktreeBranch,
		})
		result = RemoteCreateResult{ID: req.IntentID, Ref: ref, DisplayName: displayName, Path: path, Accepted: true}
		receipt, err := newSuccessReceipt(req.IntentID, "remote-create:create", ref.MapKey(), nextCommandSeq(doc), now, result)
		if err != nil {
			return err
		}
		doc.Commands = append(doc.Commands, receipt)
		return nil
	})
	if err != nil {
		return RemoteCreateResult{}, err
	}

	c.hook(CrashBeforePendingCommit)
	return result, nil
}

// Run drives remote create intents and retries under the runtime context. It
// returns when ctx is cancelled.
func (c *RemoteCreateCoordinator) Run(ctx context.Context) error {
	c.hook(CrashCoordinatorRestart)
	c.resumePending(ctx)

	ticker := time.NewTicker(c.opts.Tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			c.runPendingRemoteCreates(ctx)
		}
	}
}

func (c *RemoteCreateCoordinator) resumePending(ctx context.Context) {
	for _, p := range c.catalog.PendingRemoteCreates() {
		c.executePendingRemoteCreate(ctx, p)
	}
}

func (c *RemoteCreateCoordinator) runPendingRemoteCreates(ctx context.Context) {
	now := c.opts.Now()
	for _, p := range c.catalog.PendingRemoteCreates() {
		if p.NextAttempt.After(now) {
			continue
		}
		c.executePendingRemoteCreate(ctx, p)
	}
}

func (c *RemoteCreateCoordinator) executePendingRemoteCreate(ctx context.Context, p PendingRemoteCreateRecord) {
	// Crash before send is a proxy-side fault; keep it here for test symmetry.
	c.hook(CrashBeforeSend)

	cwd := expandPath(p.Cwd)
	if p.WorktreeBranch != "" {
		base := cwd
		sanitized := sanitizeWorktreeBranch(p.WorktreeBranch)
		worktreePath := filepath.Join(base, ".worktrees", sanitized)
		if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
			if err := git.CreateWorktree(base, p.WorktreeBranch, worktreePath); err != nil {
				c.failPendingRemoteCreate(p, err)
				return
			}
		}
		cwd = worktreePath
	}

	probeBinding := pty.StableBinding{
		Owner:     string(p.Ref.Owner),
		SessionID: string(p.Ref.Session),
		DaemonKey: string(p.Ref.Session),
	}
	var info pty.ReadyInfo
	if ev := c.backend.Probe(probeBinding); ev.Status == pty.ProbeLive {
		info = pty.ReadyInfo{DaemonPID: ev.DaemonPID, ShellPID: ev.ShellPID, Generation: ev.Binding.Generation}
	} else {
		if c.opts.BeforeStartHook != nil {
			c.opts.BeforeStartHook(p)
		}
		var err error
		info, err = c.backend.Start(ctx, pty.StartRequest{
			StableBinding: probeBinding,
			Shell:         p.Shell,
			Cwd:           cwd,
			Cols:          p.Cols,
			Rows:          p.Rows,
		})
		if c.opts.AfterStartHook != nil {
			c.opts.AfterStartHook(p, info, err)
		}
		if err != nil {
			c.failPendingRemoteCreate(p, err)
			return
		}
	}

	c.hook(CrashOwnerStarting)

	now := c.opts.Now()
	err := c.catalog.apply("remote-create/running", func(doc *AppDocument) error {
		filtered := doc.PendingRemoteCreates[:0]
		var rec *PendingRemoteCreateRecord
		for i := range doc.PendingRemoteCreates {
			if doc.PendingRemoteCreates[i].IntentID == p.IntentID {
				rec = &doc.PendingRemoteCreates[i]
				continue
			}
			filtered = append(filtered, doc.PendingRemoteCreates[i])
		}
		if rec == nil {
			return nil
		}
		if rec.Status == string(RemoteCreateStatusCompleted) {
			doc.PendingRemoteCreates = filtered
			return nil
		}
		rec.Status = string(RemoteCreateStatusRunning)
		rec.UpdatedAt = now
		filtered = append(filtered, *rec)
		doc.PendingRemoteCreates = filtered

		session := LocalSessionRecord{
			ID:             rec.Ref.Session,
			Owner:          rec.Ref.Owner,
			Ref:            rec.Ref,
			Phase:          SessionPhaseActive,
			Desired:        DesiredRun,
			Created:        now,
			Name:           rec.DisplayName,
			Shell:          rec.Shell,
			Cwd:            cwd,
			Cols:           rec.Cols,
			Rows:           rec.Rows,
			DaemonPID:      info.DaemonPID,
			Generation:     info.Generation,
			ScheduleID:     rec.ScheduleID,
			AgentType:      rec.AgentType,
			WorktreeBranch: rec.WorktreeBranch,
		}
		found := false
		for i := range doc.Sessions {
			if doc.Sessions[i].ID == session.ID {
				doc.Sessions[i] = session
				found = true
				break
			}
		}
		if !found {
			doc.Sessions = append(doc.Sessions, session)
		}
		return nil
	})
	if err != nil {
		logrus.WithError(err).WithField("intent", p.IntentID).Error("failed to commit running remote create")
		return
	}

	c.hook(CrashOwnerRunning)

	err = c.catalog.apply("remote-create/cleanup", func(doc *AppDocument) error {
		doc.PendingRemoteCreates = removeRemotePendingByID(doc.PendingRemoteCreates, p.IntentID)
		return nil
	})
	if err != nil {
		logrus.WithError(err).WithField("intent", p.IntentID).Error("failed to clean up remote create pending record")
		return
	}

	c.hook(CrashPendingCleanup)
	c.hook(CrashOwnerAcceptedNoReply)

	logrus.WithFields(logrus.Fields{
		"command_id":   p.IntentID,
		"session_id":   p.Ref.Session,
		"display_name": p.DisplayName,
		"generation":   info.Generation,
	}).Info("remote create completed")
}

func (c *RemoteCreateCoordinator) failPendingRemoteCreate(p PendingRemoteCreateRecord, cause error) {
	logrus.WithError(cause).WithFields(logrus.Fields{
		"command_id": p.IntentID,
		"session_id": p.Ref.Session,
		"retries":    p.Attempts,
	}).Warn("remote pending create failed")

	retries := p.Attempts + 1
	if retries > c.opts.MaxRetries {
		_ = c.catalog.apply("remote-create/failed", func(doc *AppDocument) error {
			doc.PendingRemoteCreates = removeRemotePendingByID(doc.PendingRemoteCreates, p.IntentID)
			rec := LocalSessionRecord{
				ID:             p.Ref.Session,
				Owner:          p.Ref.Owner,
				Ref:            p.Ref,
				Phase:          SessionPhaseDismissed,
				Desired:        DesiredStop,
				Created:        c.opts.Now(),
				Name:           p.DisplayName,
				Shell:          p.Shell,
				Cwd:            p.Cwd,
				Cols:           p.Cols,
				Rows:           p.Rows,
				ScheduleID:     p.ScheduleID,
				AgentType:      p.AgentType,
				WorktreeBranch: p.WorktreeBranch,
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
			c.cleanupWorktree(p.Cwd, p.WorktreeBranch)
		}
		return
	}

	backoff := c.opts.RetryInitial * time.Duration(1<<minInt(retries, 10))
	if backoff > c.opts.RetryMax {
		backoff = c.opts.RetryMax
	}
	p.Attempts = retries
	p.NextAttempt = c.opts.Now().Add(backoff)
	p.Status = string(RemoteCreateStatusPending)
	_ = c.catalog.PutPendingRemoteCreate(p)
}

func (c *RemoteCreateCoordinator) hook(p RemoteCreateCrashPoint) {
	if c.opts.CrashHook != nil {
		c.opts.CrashHook(p)
	}
}

// placeRefInWorkspaceLocked places ref into the singleton workspace tree.
// If a target is specified, the ref is placed by splitting the target leaf.
// Otherwise, it is placed as a new leaf in the tree or creates the tree if empty.
func (c *RemoteCreateCoordinator) placeRefInWorkspaceLocked(doc *AppDocument, ref SessionRef, req RemoteCreateRequest) error {
	if doc.Workspace == nil {
		doc.Workspace = &WorkspaceRecord{
			Owner:    doc.Owner,
			Revision: doc.Revision + 1,
			Tree:     &PaneNode{Type: "leaf", Ref: &ref},
		}
		return nil
	}

	if req.Target != nil && req.Target.Session != "" {
		// Split the target leaf
		if doc.Workspace.Tree == nil {
			return StateError{Code: ErrMissingTarget, Field: "target", Detail: "workspace tree is empty"}
		}
		if !findLeaf(*doc.Workspace.Tree, *req.Target) {
			return StateError{Code: ErrMissingTarget, Field: "target", Detail: fmt.Sprintf("target leaf %q not in workspace", req.Target.MapKey())}
		}
		if findLeaf(*doc.Workspace.Tree, ref) {
			return StateError{Code: ErrDuplicateLeaf, Field: "new", Detail: fmt.Sprintf("session %q already in workspace", ref.MapKey())}
		}
		tree, err := splitTree(*doc.Workspace.Tree, *req.Target, req.Direction, ref, req.NewFirst)
		if err != nil {
			return err
		}
		doc.Workspace.Tree = &tree
		doc.Workspace.Revision = doc.Revision + 1
		return nil
	}

	// Add as a new leaf to the workspace tree
	if doc.Workspace.Tree == nil {
		doc.Workspace.Tree = &PaneNode{Type: "leaf", Ref: &ref}
	} else {
		// Add as a sibling split of an existing leaf
		if findLeaf(*doc.Workspace.Tree, ref) {
			return nil // Already present, no-op
		}
		targetLeaf := findAnyLeaf(*doc.Workspace.Tree)
		if targetLeaf == nil {
			doc.Workspace.Tree = &PaneNode{Type: "leaf", Ref: &ref}
		} else {
			tree, err := splitTree(*doc.Workspace.Tree, *targetLeaf, DirectionHorizontal, ref, false)
			if err != nil {
				return err
			}
			doc.Workspace.Tree = &tree
		}
	}
	doc.Workspace.Revision = doc.Revision + 1
	return nil
}

func (c *RemoteCreateCoordinator) uniqueDisplayNameLocked(doc *AppDocument, name string) string {
	used := make(map[string]struct{}, len(doc.Sessions)+len(doc.PendingCreates)+len(doc.PendingRemoteCreates))
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
	for _, p := range doc.PendingRemoteCreates {
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

func (c *RemoteCreateCoordinator) cleanupWorktree(cwd, branch string) {
	base := expandPath(cwd)
	sanitized := sanitizeWorktreeBranch(branch)
	path := filepath.Join(base, ".worktrees", sanitized)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return
	}
	if err := runGitWorktreeRemove(base, path); err != nil {
		logrus.WithError(err).WithField("path", path).Debug("git worktree remove failed, falling back to directory removal")
	}
	if err := os.RemoveAll(path); err != nil {
		logrus.WithError(err).WithField("path", path).Warn("failed to remove failed-create worktree directory")
	}
}

// RemoteCreateDiagnostic is a read-only view of a stuck saga for alerting.
type RemoteCreateDiagnostic struct {
	IntentID CommandID     `json:"intent_id"`
	Ref      SessionRef    `json:"ref"`
	Age      time.Duration `json:"age"`
	Attempts int           `json:"attempts"`
	Status   string        `json:"status"`
}

// Diagnostics returns age/attempt information for genuinely stuck pending
// remote creates. Automatic retry remains the default; this is for visibility.
func (c *RemoteCreateCoordinator) Diagnostics() []RemoteCreateDiagnostic {
	now := c.opts.Now()
	pending := c.catalog.PendingRemoteCreates()
	out := make([]RemoteCreateDiagnostic, 0, len(pending))
	for _, p := range pending {
		out = append(out, RemoteCreateDiagnostic{
			IntentID: p.IntentID,
			Ref:      p.Ref,
			Age:      now.Sub(p.Inserted),
			Attempts: p.Attempts,
			Status:   p.Status,
		})
	}
	return out
}

func removeRemotePendingByID(list []PendingRemoteCreateRecord, id CommandID) []PendingRemoteCreateRecord {
	filtered := list[:0]
	for _, x := range list {
		if x.IntentID != id {
			filtered = append(filtered, x)
		}
	}
	return filtered
}
