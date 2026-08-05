package state

import (
	"encoding/json"
	"fmt"
	"time"
)

// SchemaVersion is the only supported lean v2 document schema.
const SchemaVersion = 2

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
// catalog and workspace layouts for exactly one owner.
//
// Identity fields are IDs; mutable display labels live in compatibility records.
type AppDocument struct {
	Schema               int                         `json:"schema"`
	Owner                OwnerID                     `json:"owner"`
	Revision             int64                       `json:"revision"`
	Sessions             []LocalSessionRecord        `json:"sessions"`
	Workspaces           []WorkspaceRecord           `json:"workspaces,omitempty"`
	Layouts              []LayoutRecord              `json:"layouts,omitempty"`
	Commands             []CommandReceipt            `json:"commands,omitempty"`
	PendingCreates       []PendingCreateRecord       `json:"pending_creates,omitempty"`
	PendingRemoteCreates []PendingRemoteCreateRecord `json:"pending_remote_creates,omitempty"`
	Compat               CompatAppDocument           `json:"_compat,omitempty"`
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
	Compat   CompatLocalSession  `json:"_compat,omitempty"`
}

// WorkspaceRecord is the active workspace layout for an owner.
type WorkspaceRecord struct {
	ID        LayoutID        `json:"id"`
	Owner     OwnerID         `json:"owner"`
	Revision  int64           `json:"revision"`
	Tree      PaneNode        `json:"tree"`
	ActiveKey *SessionRef     `json:"active_key,omitempty"`
	Compat    CompatWorkspace `json:"_compat,omitempty"`
}

// LayoutRecord is a named, persisted layout in the owner's catalog.
type LayoutRecord struct {
	ID       LayoutID     `json:"id"`
	Owner    OwnerID      `json:"owner"`
	Order    int64        `json:"order"`
	Revision int64        `json:"revision"`
	Tree     PaneNode     `json:"tree"`
	Compat   CompatLayout `json:"_compat,omitempty"`
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

// PresentationRecord maps a session reference to its on-screen presentation
// state for a particular browser connection.
type PresentationRecord struct {
	Ref      SessionRef `json:"ref"`
	Selected bool       `json:"selected"`
	ZIndex   int        `json:"z_index,omitempty"`
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
}

// PendingRemoteCreateRecord is the only persisted distributed saga. It tracks
// a remote create request from another node until the workspace owner has
// allocated the ref, placed the leaf, and the daemon is running.
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
	LayoutID       LayoutID   `json:"layout_id,omitempty"`
	Status         string     `json:"status"`
	Inserted       time.Time  `json:"inserted_at"`
	UpdatedAt      time.Time  `json:"updated_at,omitempty"`
	Attempts       int        `json:"attempts,omitempty"`
	NextAttempt    time.Time  `json:"next_attempt,omitempty"`
	WorktreeBranch string     `json:"worktree_branch,omitempty"`
}

// OwnerCatalogSnapshot is sent from an owner to the browser or to peers. It
// carries the canonical catalog state and the owner-scope persisted revision.
type OwnerCatalogSnapshot struct {
	Owner    OwnerID              `json:"owner"`
	Revision int64                `json:"revision"`
	Sessions []LocalSessionRecord `json:"sessions"`
	Layouts  []LayoutRecord       `json:"layouts,omitempty"`
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
	Ref      SessionRef           `json:"ref"`
	Phase    SessionPhase         `json:"phase"`
	Revision int64                `json:"revision"`
	Compat   CompatBrowserSession `json:"_compat,omitempty"`
}

// CreateIntent captures a request to create or modify state. It carries enough
// information to be idempotently retried by a worker.
//
// KNOWN-DORMANT SIBLING BUG: like the (fixed) RemoteCreateRequest.Target
// before it, Target below is a non-pointer SessionRef with an `omitempty`
// tag that encoding/json never honors for struct types, so a zero-value
// Target would always marshal as SessionRef's custom ":0.0" representation
// instead of being omitted. This is intentionally left unfixed here: as of
// this note, CreateIntent is never constructed anywhere in non-test
// production code (verified via a repo-wide reference search), so there is
// no live path where the bug can currently manifest. If CreateIntent is
// ever wired into a real construction path, apply the same fix used for
// RemoteCreateRequest.Target (pointer + explicit nil check) before doing so.
type CreateIntent struct {
	ID       CommandID       `json:"id"`
	Owner    OwnerID         `json:"owner"`
	Kind     string          `json:"kind"`
	Target   SessionRef      `json:"target,omitempty"`
	Params   json.RawMessage `json:"params,omitempty"`
	Inserted time.Time       `json:"inserted_at"`
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
// addressed (a SessionRef.MapKey(), a LayoutID, or similar), for
// observability only -- replay never re-derives the result from Target.
// Status is "ok" or "error". Result is the exact JSON-encoded success payload
// (typically a CommandResult or RemoteCreateResult); Error, when Status is
// "error", is a previously observed terminal, input-derived failure that is
// safe to replay unchanged. Receipts are bounded by MaxCommandReceiptAge and
// MaxPendingCommands (see pruneReceipts in store.go), so idempotent replay is
// only guaranteed within that window, not forever.
type CommandReceipt struct {
	ID       CommandID             `json:"id"`
	IntentID CommandID             `json:"intent_id"`
	Seq      int64                 `json:"seq"`
	Created  time.Time             `json:"created_at"`
	Kind     string                `json:"kind,omitempty"`
	Target   string                `json:"target,omitempty"`
	Status   string                `json:"status,omitempty"`
	Result   json.RawMessage       `json:"result,omitempty"`
	Error    *CommandReceiptError  `json:"error,omitempty"`
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

// WorkspaceCommand is an envelope for a command targeting a workspace layout.
type WorkspaceCommand struct {
	ID     CommandID       `json:"id"`
	Layout LayoutID        `json:"layout"`
	Action string          `json:"action"`
	Params json.RawMessage `json:"params,omitempty"`
}

// CompatAppDocument holds legacy fields needed during the v2 transition.
type CompatAppDocument struct {
	TmuxCatalogRevision int64 `json:"tmux_catalog_revision,omitempty"`
}

// CompatLocalSession holds mutable display and runtime details that are not
// part of canonical identity.
type CompatLocalSession struct {
	Name        string `json:"name,omitempty"`
	Shell       string `json:"shell,omitempty"`
	Cwd         string `json:"cwd,omitempty"`
	Cols        uint16 `json:"cols,omitempty"`
	Rows        uint16 `json:"rows,omitempty"`
	DaemonPID   int    `json:"daemon_pid,omitempty"`
	SystemdUnit string `json:"systemd_unit,omitempty"`
	Generation  string `json:"generation,omitempty"`
}

// CompatWorkspace holds legacy workspace fields.
type CompatWorkspace struct {
	Name string `json:"name,omitempty"`
}

// CompatLayout holds legacy layout fields, including human-selected names.
type CompatLayout struct {
	Name string `json:"name,omitempty"`
}

// CompatBrowserSession holds legacy display fields for the browser.
type CompatBrowserSession struct {
	DisplayName   string `json:"display_name,omitempty"`
	ProjectPath   string `json:"project_path,omitempty"`
	PromptPreview string `json:"prompt_preview,omitempty"`
	AgentType     string `json:"agent_type,omitempty"`
}
