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
type CreateIntent struct {
	ID       CommandID       `json:"id"`
	Owner    OwnerID         `json:"owner"`
	Kind     string          `json:"kind"`
	Target   SessionRef      `json:"target,omitempty"`
	Params   json.RawMessage `json:"params,omitempty"`
	Inserted time.Time       `json:"inserted_at"`
}

// CommandReceipt acknowledges that a command was accepted and optionally gives
// the worker the next sequence number to observe.
type CommandReceipt struct {
	ID       CommandID `json:"id"`
	IntentID CommandID `json:"intent_id"`
	Seq      int64     `json:"seq"`
	Created  time.Time `json:"created_at"`
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
