package state

import (
	"encoding/json"
	"fmt"
	"time"
)

// SchemaVersion is the only supported canonical document schema.
//
// Schema 4 completes the workspace refactor to a singleton model:
// - AppDocument has one Workspace (not plural Layouts)
// - WorkspaceRecord has no ID field (it is the singular, implicit workspace)
// - Tree is nullable (*PaneNode, can be nil for an empty workspace)
// - TmuxCatalogRevision is removed
// Schema 3 was the canonical schema with multiple layouts; schema 2 and earlier
// are unsupported and fail closed.
const SchemaVersion = 4

// SessionPhase is the observed runtime phase of a session.
type SessionPhase string

const (
	SessionPhasePending      SessionPhase = "pending"
	SessionPhaseStarting     SessionPhase = "starting"
	SessionPhaseActive       SessionPhase = "active"
	SessionPhaseCrashed      SessionPhase = "crashed"
	SessionPhaseCleanlyEnded SessionPhase = "cleanly_ended"
	SessionPhaseDismissed    SessionPhase = "dismissed"
)

// DesiredSessionState is the user-requested state for a session.
type DesiredSessionState string

const (
	DesiredRun     DesiredSessionState = "run"
	DesiredStop    DesiredSessionState = "stop"
	DesiredRestart DesiredSessionState = "restart"
)

// AppDocument is the persisted owner-scope state. It contains the session
// catalog and a singleton workspace for exactly one owner. Every field is
// canonical and direct -- schema 4 enforces a single workspace (no layouts array).
type AppDocument struct {
	Schema               int                         `json:"schema"`
	Owner                OwnerID                     `json:"owner"`
	Revision             int64                       `json:"revision"`
	Sessions             []LocalSessionRecord        `json:"sessions"`
	Workspace            *WorkspaceRecord            `json:"workspace,omitempty"`
	Commands             []CommandReceipt            `json:"commands,omitempty"`
	PendingCreates       []PendingCreateRecord       `json:"pending_creates,omitempty"`
	PendingRemoteCreates []PendingRemoteCreateRecord `json:"pending_remote_creates,omitempty"`
}

// LocalSessionRecord is the canonical per-session row known to an owner.
type LocalSessionRecord struct {
	ID       SessionID           `json:"id"`
	Owner    OwnerID             `json:"owner"`
	Ref      SessionRef          `json:"ref"`
	Phase    SessionPhase        `json:"phase"`
	Desired  DesiredSessionState `json:"desired"`
	Revision int64               `json:"revision"`
	Created  time.Time           `json:"created_at"`

	// Name is the mutable, user-facing display label.
	Name string `json:"name,omitempty"`
	// Shell/Cwd/Cols/Rows are the daemon spawn parameters last used (or to be
	// used) for this session.
	Shell string `json:"shell,omitempty"`
	Cwd   string `json:"cwd,omitempty"`
	Cols  uint16 `json:"cols,omitempty"`
	Rows  uint16 `json:"rows,omitempty"`
	// DaemonPID is the last known daemon process id.
	DaemonPID int `json:"daemon_pid,omitempty"`
	// SystemdUnit is the last known systemd unit name managing the daemon.
	SystemdUnit string `json:"systemd_unit,omitempty"`
	// Generation is the daemon generation identity used for stable binding and
	// exact-generation termination/adoption (see reconciler.go).
	Generation string `json:"generation,omitempty"`

	// Hidden/Background are mutable presentation flags set via
	// ActionSetPresentation (see session_commands.go). They carry the same
	// semantics as the equivalent fields of an older, separate per-session
	// attribute store, but live directly on the canonical record instead.
	Hidden     bool `json:"hidden,omitempty"`
	Background bool `json:"background,omitempty"`

	// ScheduleID, when non-empty, is the identity of the scheduler job that
	// created this session (carried over from PendingCreateRecord.ScheduleID
	// when the create completes). It lets schedule-cap enforcement query the
	// canonical catalog directly by SessionRef instead of a separate
	// display-name-keyed attribute store.
	ScheduleID string `json:"schedule_id,omitempty"`

	// AgentType/WorktreeBranch are canonical creation metadata carried over
	// from the pending create record (local or remote) when the create
	// completes, so they survive through pending, active, failed, retry,
	// recovery, and reconciler-adoption states instead of being discarded
	// once the daemon starts.
	AgentType      string `json:"agent_type,omitempty"`
	WorktreeBranch string `json:"worktree_branch,omitempty"`
}

// WorkspaceRecord is the singleton workspace for an owner. There is exactly
// one per document (or none if Workspace is nil). Tree is nullable and can be
// nil to represent an empty workspace.
type WorkspaceRecord struct {
	Owner     OwnerID     `json:"owner"`
	Revision  int64       `json:"revision"`
	Tree      *PaneNode   `json:"tree,omitempty"`
	ActiveKey *SessionRef `json:"active_key,omitempty"`
	// Name is the mutable, human-selected workspace name.
	Name string `json:"name,omitempty"`
}



// PaneNode is a concrete tagged-union node in a workspace layout tree.
// A node is either a leaf (Ref != nil) or a split (First != nil and
// Second != nil). The Type field carries the tag used by JSON and TypeScript.
type PaneNode struct {
	Type      string         `json:"type"`
	Ref       *SessionRef    `json:"ref,omitempty"`       // leaf
	ID        SplitID        `json:"id,omitempty"`        // split (optional)
	Direction SplitDirection `json:"direction,omitempty"` // split
	Ratio     Ratio          `json:"ratio,omitempty"`     // split
	First     *PaneNode      `json:"first,omitempty"`     // split
	Second    *PaneNode      `json:"second,omitempty"`    // split
}

// IsLeaf reports whether the node is a leaf.
func (n PaneNode) IsLeaf() bool { return n.Type == "leaf" }

// IsSplit reports whether the node is a split.
func (n PaneNode) IsSplit() bool { return n.Type == "split" }

// Leaf builds a leaf node.
func Leaf(ref SessionRef) PaneNode {
	return PaneNode{Type: "leaf", Ref: &ref}
}

// Split builds a split node.
func Split(dir SplitDirection, ratio Ratio, first, second PaneNode) PaneNode {
	return PaneNode{
		Type:      "split",
		Direction: dir,
		Ratio:     ratio,
		First:     &first,
		Second:    &second,
	}
}

// MarshalJSON encodes a PaneNode as a tagged object.
func (n PaneNode) MarshalJSON() ([]byte, error) {
	switch n.Type {
	case "leaf":
		if n.Ref == nil {
			return nil, fmt.Errorf("leaf pane has nil ref")
		}
		return json.Marshal(struct {
			Type string     `json:"type"`
			Ref  SessionRef `json:"ref"`
		}{Type: "leaf", Ref: *n.Ref})
	case "split":
		return json.Marshal(struct {
			Type      string         `json:"type"`
			ID        SplitID        `json:"id,omitempty"`
			Direction SplitDirection `json:"direction"`
			Ratio     Ratio          `json:"ratio"`
			First     *PaneNode      `json:"first"`
			Second    *PaneNode      `json:"second"`
		}{Type: "split", ID: n.ID, Direction: n.Direction, Ratio: n.Ratio, First: n.First, Second: n.Second})
	default:
		return nil, fmt.Errorf("unknown pane node type %q", n.Type)
	}
}

// UnmarshalJSON decodes a tagged object into a PaneNode.
func (n *PaneNode) UnmarshalJSON(data []byte) error {
	var tag struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &tag); err != nil {
		return err
	}
	switch tag.Type {
	case "leaf":
		var w struct {
			Ref SessionRef `json:"ref"`
		}
		if err := json.Unmarshal(data, &w); err != nil {
			return err
		}
		*n = Leaf(w.Ref)
		return nil
	case "split":
		var w struct {
			ID        SplitID        `json:"id,omitempty"`
			Direction SplitDirection `json:"direction"`
			Ratio     Ratio          `json:"ratio"`
			First     *PaneNode      `json:"first"`
			Second    *PaneNode      `json:"second"`
		}
		if err := json.Unmarshal(data, &w); err != nil {
			return err
		}
		*n = PaneNode{
			Type:      "split",
			ID:        w.ID,
			Direction: w.Direction,
			Ratio:     w.Ratio,
			First:     w.First,
			Second:    w.Second,
		}
		return nil
	default:
		return fmt.Errorf("unknown pane node type %q", tag.Type)
	}
}

// PendingCreateRecord tracks a local create intent that has been issued but is
// not yet reflected in the owner catalog.
type PendingCreateRecord struct {
	IntentID       CommandID  `json:"intent_id"`
	Ref            SessionRef `json:"ref"`
	Inserted       time.Time  `json:"inserted_at"`
	Shell          string     `json:"shell,omitempty"`
	Cwd            string     `json:"cwd,omitempty"`
	Cols           uint16     `json:"cols,omitempty"`
	Rows           uint16     `json:"rows,omitempty"`
	Retries        int        `json:"retries,omitempty"`
	NextAttempt    time.Time  `json:"next_attempt,omitempty"`
	DisplayName    string     `json:"display_name,omitempty"`
	WorktreeBranch string     `json:"worktree_branch,omitempty"`
	// ScheduleID, when non-empty, is the identity of the scheduler job that
	// fired this create. It is carried through to the materialized session so
	// a schedule can be correlated with the sessions it produced, and is used
	// by ValidateDocument to reject more than one in-flight create owning the
	// same schedule slot at once (see ErrInconsistentScheduleOwnership).
	ScheduleID string `json:"schedule_id,omitempty"`
	// AgentType mirrors CreateParams.AgentType; it is carried through to
	// LocalSessionRecord.AgentType when the create completes.
	AgentType string `json:"agent_type,omitempty"`
}

// PendingRemoteCreateRecord is the only persisted distributed saga. It tracks
// a remote create request from another node until the workspace owner has
// allocated the ref, placed the leaf in the singleton workspace, and the daemon
// is running.
type PendingRemoteCreateRecord struct {
	IntentID       CommandID  `json:"intent_id"`
	Owner          OwnerID    `json:"owner"`
	Requester      OwnerID    `json:"requester,omitempty"`
	Ref            SessionRef `json:"ref"`
	DisplayName    string     `json:"display_name,omitempty"`
	Shell          string     `json:"shell,omitempty"`
	Cwd            string     `json:"cwd,omitempty"`
	Cols           uint16     `json:"cols,omitempty"`
	Rows           uint16     `json:"rows,omitempty"`
	Status         string     `json:"status"`
	Inserted       time.Time  `json:"inserted_at"`
	UpdatedAt      time.Time  `json:"updated_at,omitempty"`
	Attempts       int        `json:"attempts,omitempty"`
	NextAttempt    time.Time  `json:"next_attempt,omitempty"`
	WorktreeBranch string     `json:"worktree_branch,omitempty"`
	// ScheduleID mirrors PendingCreateRecord.ScheduleID for the remote-create
	// saga (see RemoteCreateRequest.ScheduleID).
	ScheduleID string `json:"schedule_id,omitempty"`
	// AgentType mirrors RemoteCreateRequest.AgentType; it is carried through
	// to LocalSessionRecord.AgentType when the remote create completes.
	AgentType string `json:"agent_type,omitempty"`
}

// OwnerCatalogSnapshot is sent from an owner to the browser or to peers. It
// carries the canonical catalog state and the owner-scope persisted revision.
type OwnerCatalogSnapshot struct {
	Owner     OwnerID              `json:"owner"`
	Revision  int64                `json:"revision"`
	Sessions  []LocalSessionRecord `json:"sessions"`
	Workspace *WorkspaceRecord     `json:"workspace,omitempty"`
}

// BrowserWorkspaceSnapshot is the aggregate state exposed to one browser
// connection. Its revision is connection-local and includes command sequence
// numbers, but the underlying owner revisions are authoritative.
type BrowserWorkspaceSnapshot struct {
	Generation int                   `json:"transport_generation"`
	Revision   int64                 `json:"revision"`
	Owner      OwnerID               `json:"owner"`
	Sessions   []BrowserSession      `json:"sessions"`
	Workspace  *WorkspaceRecord      `json:"workspace,omitempty"`
	Pending    []PendingCreateRecord `json:"pending,omitempty"`
}

// BrowserSession is the read-only session projection shown to the browser.
type BrowserSession struct {
	Ref      SessionRef   `json:"ref"`
	Phase    SessionPhase `json:"phase"`
	Revision int64        `json:"revision"`

	// DisplayName/ProjectPath/PromptPreview/AgentType are mutable display
	// fields shown to the browser.
	DisplayName   string `json:"display_name,omitempty"`
	ProjectPath   string `json:"project_path,omitempty"`
	PromptPreview string `json:"prompt_preview,omitempty"`
	AgentType     string `json:"agent_type,omitempty"`
}

// CommandReceiptError is the durable, stable representation of a command
// that was rejected in a way that is safe to replay unchanged: the error is
// fully determined by the command's own input and does not depend on
// mutable catalog state that could legitimately produce a different answer
// on a later retry (e.g. a not-found error is NOT safe to cache this way,
// since the same command ID retried after the target is created should
// succeed, not keep replaying a stale not-found).
type CommandReceiptError struct {
	Code    ErrorCode `json:"code"`
	Field   string    `json:"field,omitempty"`
	Message string    `json:"message"`
}

// CommandReceipt is the durable record of one accepted command, keyed by
// CommandID. It is the single source of truth for idempotent replay: a
// retried request carrying the same CommandID must return the EXACT original
// outcome recorded here, never a value re-derived by scanning current catalog
// state (that scan is what let a replayed create return an arbitrary
// unrelated session once multiple commands were in flight -- see
// session_commands.go's executeCreate and remote_create.go's
// ExecuteRemoteCreate).
//
// Kind identifies the command family and action (e.g. "session:create",
// "workspace:split"). Target is a normalized identifier for what the command
// addressed (a SessionRef.MapKey() or similar), for observability only --
// replay never re-derives the result from Target.
// Status is "ok" or "error". Result is the exact JSON-encoded success payload
// (typically a CommandResult or RemoteCreateResult); Error, when Status is
// "error", is a previously observed terminal, input-derived failure that is
// safe to replay unchanged. Receipts are bounded by MaxCommandReceiptAge and
// MaxPendingCommands (see pruneReceipts in store.go), so idempotent replay is
// only guaranteed within that window, not forever.
type CommandReceipt struct {
	ID       CommandID            `json:"id"`
	IntentID CommandID            `json:"intent_id"`
	Seq      int64                `json:"seq"`
	Created  time.Time            `json:"created_at"`
	Kind     string               `json:"kind,omitempty"`
	Target   string               `json:"target,omitempty"`
	Status   string               `json:"status,omitempty"`
	Result   json.RawMessage      `json:"result,omitempty"`
	Error    *CommandReceiptError `json:"error,omitempty"`
}

// DecodeResult unmarshals the receipt's stored success payload into out. It
// is a no-op (out left unchanged) if the receipt carries no result.
func (r CommandReceipt) DecodeResult(out interface{}) error {
	if len(r.Result) == 0 {
		return nil
	}
	return json.Unmarshal(r.Result, out)
}

// AsError reconstructs the stored terminal failure, if any, as a StateError.
// Returns nil if the receipt does not carry an error.
func (r CommandReceipt) AsError() error {
	if r.Error == nil {
		return nil
	}
	return StateError{Code: r.Error.Code, Field: r.Error.Field, Detail: r.Error.Message}
}

// findCommandReceipt returns the receipt for id, if one exists in doc.
func findCommandReceipt(doc *AppDocument, id CommandID) (CommandReceipt, bool) {
	for _, r := range doc.Commands {
		if r.ID == id {
			return r, true
		}
	}
	return CommandReceipt{}, false
}

// newSuccessReceipt builds a CommandReceipt recording a successful command
// outcome. result is JSON-marshalled once and stored verbatim so replay never
// re-derives it.
func newSuccessReceipt(id CommandID, kind, target string, seq int64, now time.Time, result interface{}) (CommandReceipt, error) {
	b, err := json.Marshal(result)
	if err != nil {
		return CommandReceipt{}, err
	}
	return CommandReceipt{
		ID:       id,
		IntentID: id,
		Seq:      seq,
		Created:  now,
		Kind:     kind,
		Target:   target,
		Status:   "ok",
		Result:   b,
	}, nil
}

// SessionCommand is an envelope for a command targeting a session.
type SessionCommand struct {
	ID     CommandID       `json:"id"`
	Ref    SessionRef      `json:"ref"`
	Action string          `json:"action"`
	Params json.RawMessage `json:"params,omitempty"`
}

// WorkspaceCommand is an envelope for a command targeting the singleton workspace.
type WorkspaceCommand struct {
	ID     CommandID       `json:"id"`
	Action string          `json:"action"`
	Params json.RawMessage `json:"params,omitempty"`
}
