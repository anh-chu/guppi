package state

import (
	"testing"

	"github.com/anh-chu/termyard/pkg/model"
	"github.com/anh-chu/termyard/pkg/pty"
)

// fakeDaemonReg is a minimal DaemonRegistry stub for UpdateSessions tests.
type fakeDaemonReg struct {
	dead map[string]bool
	sessions map[string]pty.SessionInfo
}

func (f *fakeDaemonReg) List() []pty.SessionInfo {
	if f.sessions == nil {
		return nil
	}
	var out []pty.SessionInfo
	for _, info := range f.sessions {
		out = append(out, info)
	}
	return out
}
func (f *fakeDaemonReg) Capture(name string) (string, error)    { return "", nil }
func (f *fakeDaemonReg) CrashedSessions() []CrashedSessionInfo { return nil }
func (f *fakeDaemonReg) IsSessionDead(name string) bool         { return f.dead[name] }

// TestUpdateSessions_RemovesConfirmedDeadLastSession verifies that killing
// the last session (discovery goes empty) removes it from state instead of
// skipping the cycle, so it does not linger as "disconnected — reconnecting".
func TestUpdateSessions_RemovesConfirmedDeadLastSession(t *testing.T) {
	m := &Manager{
		sessions: map[string]*model.Session{"solo": {Name: "solo"}},
		meta:     map[string]SessionMetadata{"solo": {}},
		daemonReg: &fakeDaemonReg{dead: map[string]bool{"solo": true}},
	}

	ch := m.Subscribe()
	defer m.Unsubscribe(ch)

	m.UpdateSessions(nil) // discovery returns empty

	if _, ok := m.sessions["solo"]; ok {
		t.Fatalf("confirmed-dead last session was not removed")
	}
}

// TestUpdateSessions_SkipsTransientEmptyDiscovery verifies that an empty
// discovery is still treated as transient (not a real kill) when the tracked
// session is NOT confirmed dead, preserving the mass-removal safety guard.
func TestUpdateSessions_SkipsTransientEmptyDiscovery(t *testing.T) {
	m := &Manager{
		sessions: map[string]*model.Session{"solo": {Name: "solo"}},
		meta:     map[string]SessionMetadata{"solo": {}},
		daemonReg: &fakeDaemonReg{dead: map[string]bool{"solo": false}},
	}

	m.UpdateSessions(nil)

	if _, ok := m.sessions["solo"]; !ok {
		t.Fatalf("live session was wrongly removed on transient empty discovery")
	}
}

// TestLoadDaemonSessionDetails_SetsCurrentCommand verifies that the synthetic
// pane's CurrentCommand is wired from SessionInfo.Shell (when matched) or empty
// (when not matched).
func TestLoadDaemonSessionDetails_SetsCurrentCommand(t *testing.T) {
	tests := []struct {
		name            string
		sessions        map[string]pty.SessionInfo
		expectedCommand string
	}{
		{
			name:            "shell found",
			sessions:        map[string]pty.SessionInfo{"test": {ID: "test", Shell: "/bin/bash"}},
			expectedCommand: "/bin/bash",
		},
		{
			name:            "shell not found",
			sessions:        map[string]pty.SessionInfo{},
			expectedCommand: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session := &model.Session{Name: "test"}
			m := &Manager{
				daemonReg: &fakeDaemonReg{sessions: tt.sessions},
			}
			m.loadDaemonSessionDetails(session)

			if len(session.Windows) == 0 || len(session.Windows[0].Panes) == 0 {
				t.Fatalf("expected synthetic window/pane, got none")
			}
			pane := session.Windows[0].Panes[0]
			if pane.CurrentCommand != tt.expectedCommand {
				t.Errorf("got CurrentCommand %q, want %q", pane.CurrentCommand, tt.expectedCommand)
			}
		})
	}
}

