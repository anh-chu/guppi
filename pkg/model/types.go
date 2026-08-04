package model

import "time"

// Session represents a terminal session
type Session struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	Host             string    `json:"host,omitempty"`        // peer fingerprint (empty = local)
	HostName         string    `json:"host_name,omitempty"`   // peer display name
	HostOnline       bool      `json:"host_online,omitempty"` // whether the host peer is connected
	Backend          string    `json:"backend,omitempty"`     // "daemon" for session-daemon sessions
	Windows          []*Window `json:"windows"`
	Created          time.Time `json:"created"`
	Attached         bool      `json:"attached"`
	LastActivity     time.Time `json:"last_activity"`
	ProjectPath      string    `json:"project_path,omitempty"`
	IsWorktree       bool      `json:"is_worktree,omitempty"`
	WorktreeParent   string    `json:"worktree_parent,omitempty"` // main worktree root path (linked worktrees only)
	AgentType        string    `json:"agent_type,omitempty"`
	ScheduleID       string    `json:"schedule_id,omitempty"` // owning schedule (set by scheduler)
	PromptPreview    string    `json:"prompt_preview,omitempty"`
	AgentSessionID   string    `json:"agent_session_id,omitempty"`
	UserPrompt       string    `json:"user_prompt,omitempty"`
	LastAgentMessage string    `json:"last_agent_message,omitempty"`
	DisplayName      string    `json:"display_name,omitempty"`  // AI-generated friendly label; frontend shows this || Name
	UserSetName      bool      `json:"user_set_name,omitempty"` // user manually set DisplayName; AI must not overwrite
}

// Window represents a terminal window
type Window struct {
	ID        string  `json:"id"`
	SessionID string  `json:"session_id"`
	Name      string  `json:"name"`
	Index     int     `json:"index"`
	Active    bool    `json:"active"`
	Layout    string  `json:"layout"`
	Panes     []*Pane `json:"panes"`
}

// PaneDetailed contains resolved session name and window index for a pane,
// avoiding extra queries. Used by the agent detector.
type PaneDetailed struct {
	ID      string `json:"id"`
	Session string `json:"session"` // session name (not ID)
	Window  int    `json:"window"`  // window index
	PID     int    `json:"pid"`
}

// Pane represents a terminal pane
type Pane struct {
	ID             string `json:"id"`
	WindowID       string `json:"window_id"`
	SessionID      string `json:"session_id"`
	Index          int    `json:"index"`
	Active         bool   `json:"active"`
	Width          int    `json:"width"`
	Height         int    `json:"height"`
	CurrentCommand string `json:"current_command"`
	CurrentPath    string `json:"current_path,omitempty"`
	PID            int    `json:"pid"`
}
