package state

import (
	"testing"

	"github.com/anh-chu/termyard/pkg/model"
	"github.com/anh-chu/termyard/pkg/pty"
)

// fakeDaemonReg is a minimal DaemonRegistry stub for state manager tests.
type fakeDaemonReg struct {
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
func (f *fakeDaemonReg) Capture(name string) (string, error) { return "", nil }

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
