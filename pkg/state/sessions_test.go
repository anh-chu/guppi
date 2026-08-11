package state

import (
	"testing"
	"time"

	"github.com/anh-chu/termyard/pkg/model"
)

// TestSessionsEqual_IncludesAllFields verifies sessionsEqual checks all required fields.
func TestSessionsEqual_IncludesAllFields(t *testing.T) {
	now := time.Now()

	// Create two identical sessions
	base := &model.Session{
		ID:               "session-1",
		Name:             "test-session",
		Host:             "host-1",
		HostName:         "localhost",
		HostOnline:       true,
		Backend:          "daemon",
		Created:          now,
		ProjectPath:      "/home/user/project",
		IsWorktree:       false,
		WorktreeParent:   "",
		AgentType:        "developer",
		ScheduleID:       "schedule-1",
		PromptPreview:    "user@host",
		AgentSessionID:   "agent-1",
		UserPrompt:       "test",
		LastAgentMessage: "hello",
		DisplayName:      "My Session",
		UserSetName:      true,
		Unreachable:      false,
		Windows: []*model.Window{
			{
				ID:        "window-1",
				SessionID: "session-1",
				Name:      "main",
				Index:     0,
				Active:    true,
				Layout:    "even-horizontal",
				Panes: []*model.Pane{
					{
						ID:             "pane-1",
						WindowID:       "window-1",
						SessionID:      "session-1",
						Index:          0,
						Active:         true,
						CurrentCommand: "bash",
						CurrentPath:    "/home/user",
						PID:            12345,
					},
				},
			},
		},
	}

	// Test equality
	copy := &model.Session{
		ID:               base.ID,
		Name:             base.Name,
		Host:             base.Host,
		HostName:         base.HostName,
		HostOnline:       base.HostOnline,
		Backend:          base.Backend,
		Created:          base.Created,
		ProjectPath:      base.ProjectPath,
		IsWorktree:       base.IsWorktree,
		WorktreeParent:   base.WorktreeParent,
		AgentType:        base.AgentType,
		ScheduleID:       base.ScheduleID,
		PromptPreview:    base.PromptPreview,
		AgentSessionID:   base.AgentSessionID,
		UserPrompt:       base.UserPrompt,
		LastAgentMessage: base.LastAgentMessage,
		DisplayName:      base.DisplayName,
		UserSetName:      base.UserSetName,
		Unreachable:      base.Unreachable,
		Windows: []*model.Window{
			{
				ID:        "window-1",
				SessionID: "session-1",
				Name:      "main",
				Index:     0,
				Active:    true,
				Layout:    "even-horizontal",
				Panes: []*model.Pane{
					{
						ID:             "pane-1",
						WindowID:       "window-1",
						SessionID:      "session-1",
						Index:          0,
						Active:         true,
						CurrentCommand: "bash",
						CurrentPath:    "/home/user",
						PID:            12345,
					},
				},
			},
		},
	}

	if !sessionsEqual(base, copy) {
		t.Error("sessionsEqual should return true for identical sessions")
	}

	// Test difference in Session.ID
	copy.ID = "different-id"
	if sessionsEqual(base, copy) {
		t.Error("sessionsEqual should return false when Session.ID differs")
	}
	copy.ID = base.ID

	// Test difference in Window.SessionID
	copy.Windows[0].SessionID = "different-session"
	if sessionsEqual(base, copy) {
		t.Error("sessionsEqual should return false when Window.SessionID differs")
	}
	copy.Windows[0].SessionID = base.Windows[0].SessionID

	// Test difference in Window.Layout
	copy.Windows[0].Layout = "tiled"
	if sessionsEqual(base, copy) {
		t.Error("sessionsEqual should return false when Window.Layout differs")
	}
	copy.Windows[0].Layout = base.Windows[0].Layout

	// Test difference in Pane.WindowID
	copy.Windows[0].Panes[0].WindowID = "different-window"
	if sessionsEqual(base, copy) {
		t.Error("sessionsEqual should return false when Pane.WindowID differs")
	}
	copy.Windows[0].Panes[0].WindowID = base.Windows[0].Panes[0].WindowID

	// Test difference in Pane.SessionID
	copy.Windows[0].Panes[0].SessionID = "different-session"
	if sessionsEqual(base, copy) {
		t.Error("sessionsEqual should return false when Pane.SessionID differs")
	}
	copy.Windows[0].Panes[0].SessionID = base.Windows[0].Panes[0].SessionID

	// Test difference in Pane.Index
	copy.Windows[0].Panes[0].Index = 99
	if sessionsEqual(base, copy) {
		t.Error("sessionsEqual should return false when Pane.Index differs")
	}
	copy.Windows[0].Panes[0].Index = base.Windows[0].Panes[0].Index

	// Verify equality is restored
	if !sessionsEqual(base, copy) {
		t.Error("sessionsEqual should return true after restoring all fields")
	}
}

// TestSessionsEqual_NilHandling verifies nil handling.
func TestSessionsEqual_NilHandling(t *testing.T) {
	session := &model.Session{Name: "test"}

	// nil == nil should be true
	if !sessionsEqual(nil, nil) {
		t.Error("sessionsEqual(nil, nil) should return true")
	}

	// nil != session should be false
	if sessionsEqual(nil, session) {
		t.Error("sessionsEqual(nil, session) should return false")
	}
	if sessionsEqual(session, nil) {
		t.Error("sessionsEqual(session, nil) should return false")
	}
}
