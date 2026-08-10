package state

import (
	"testing"

	"github.com/anh-chu/termyard/pkg/model"
	"github.com/anh-chu/termyard/pkg/pty"
)

// fakeDaemonReg is a minimal DaemonRegistry stub for UpdateSessions tests.
type fakeDaemonReg struct {
	dead     map[string]bool
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
func (f *fakeDaemonReg) Capture(name string) (string, error)   { return "", nil }
func (f *fakeDaemonReg) CrashedSessions() []CrashedSessionInfo { return nil }
func (f *fakeDaemonReg) IsSessionDead(name string) bool        { return f.dead[name] }

// TestUpdateSessions_RemovesConfirmedDeadLastSession verifies that killing
// the last session (discovery goes empty) removes it from state instead of
// skipping the cycle, so it does not linger as "disconnected — reconnecting".
func TestUpdateSessions_RemovesConfirmedDeadLastSession(t *testing.T) {
	m := &Manager{
		sessions:  map[string]*model.Session{"solo": {Name: "solo"}},
		meta:      map[string]SessionMetadata{"solo": {}},
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
		sessions:  map[string]*model.Session{"solo": {Name: "solo"}},
		meta:      map[string]SessionMetadata{"solo": {}},
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

// TestUpdateSessions_MajorityRemovalGuardFiltersDeadSessions verifies that when
// >50% of sessions vanish and >2 exist, the majority-removal guard checks each
// removed session via IsSessionDead and only removes those confirmed dead.
// In this test: 2 of 3 sessions vanish and are confirmed dead -> removed.
func TestUpdateSessions_MajorityRemovalGuardFiltersDeadSessions(t *testing.T) {
	m := &Manager{
		sessions: map[string]*model.Session{
			"live":  {Name: "live"},
			"dead1": {Name: "dead1"},
			"dead2": {Name: "dead2"},
		},
		meta: map[string]SessionMetadata{
			"live":  {},
			"dead1": {},
			"dead2": {},
		},
		daemonReg: &fakeDaemonReg{
			dead: map[string]bool{
				"live":  false,
				"dead1": true,
				"dead2": true,
			},
		},
	}

	ch := m.Subscribe()
	defer m.Unsubscribe(ch)

	// Discovery now only has "live" (2 of 3 vanished = 67% removal > 50%)
	m.UpdateSessions([]*model.Session{{Name: "live"}})

	// Both dead sessions should be removed since they are confirmed dead
	if _, ok := m.sessions["dead1"]; ok {
		t.Fatalf("confirmed-dead session dead1 was not removed")
	}
	if _, ok := m.sessions["dead2"]; ok {
		t.Fatalf("confirmed-dead session dead2 was not removed")
	}
	if _, ok := m.sessions["live"]; !ok {
		t.Fatalf("live session was wrongly removed")
	}

	// Verify removal broadcasts were sent
	removalCount := 0
	for len(ch) > 0 {
		evt := <-ch
		if evt.Type == "session-removed" {
			removalCount++
		}
	}
	if removalCount != 2 {
		t.Errorf("expected 2 removal broadcasts, got %d", removalCount)
	}
}

// TestUpdateSessions_MajorityRemovalGuardBlocksUnconfirmedRemovals verifies
// that when >50% of sessions vanish and >2 exist, sessions not confirmed dead
// are kept (not removed) to protect against transient discovery failures.
// In this test: 2 of 3 sessions vanish but are NOT confirmed dead -> blocked.
func TestUpdateSessions_MajorityRemovalGuardBlocksUnconfirmedRemovals(t *testing.T) {
	m := &Manager{
		sessions: map[string]*model.Session{
			"live":       {Name: "live"},
			"unconfirm1": {Name: "unconfirm1"},
			"unconfirm2": {Name: "unconfirm2"},
		},
		meta: map[string]SessionMetadata{
			"live":       {},
			"unconfirm1": {},
			"unconfirm2": {},
		},
		daemonReg: &fakeDaemonReg{
			dead: map[string]bool{
				"live":       false,
				"unconfirm1": false,
				"unconfirm2": false,
			},
		},
	}

	ch := m.Subscribe()
	defer m.Unsubscribe(ch)

	// Discovery now only has "live" (2 of 3 vanished = 67% removal > 50%)
	// But neither unconfirm1 nor unconfirm2 is confirmed dead
	m.UpdateSessions([]*model.Session{{Name: "live"}})

	// Both unconfirmed sessions should be kept (not removed)
	if _, ok := m.sessions["unconfirm1"]; !ok {
		t.Fatalf("unconfirmed session unconfirm1 was wrongly removed")
	}
	if _, ok := m.sessions["unconfirm2"]; !ok {
		t.Fatalf("unconfirmed session unconfirm2 was wrongly removed")
	}
	if _, ok := m.sessions["live"]; !ok {
		t.Fatalf("live session was wrongly removed")
	}

	// Verify no removal broadcasts were sent
	removalCount := 0
	for len(ch) > 0 {
		evt := <-ch
		if evt.Type == "session-removed" {
			removalCount++
		}
	}
	if removalCount != 0 {
		t.Errorf("expected 0 removal broadcasts, got %d", removalCount)
	}
}

// TestUpdateSessions_MajorityRemovalGuardAllowsMixedRemovals verifies that when
// >50% of sessions vanish and >2 exist, the majority-removal guard removes only
// confirmed dead sessions and restores unconfirmed ones to state.
// In this test: 2 of 3 sessions vanish, 1 confirmed dead and 1 unconfirmed.
// Expected: dead removed, unconfirmed retained, exactly one "session-removed" broadcast.
func TestUpdateSessions_MajorityRemovalGuardAllowsMixedRemovals(t *testing.T) {
	m := &Manager{
		sessions: map[string]*model.Session{
			"live":      {Name: "live"},
			"dead":      {Name: "dead"},
			"unconfirm": {Name: "unconfirm"},
		},
		meta: map[string]SessionMetadata{
			"live":      {},
			"dead":      {},
			"unconfirm": {},
		},
		daemonReg: &fakeDaemonReg{
			dead: map[string]bool{
				"live":      false,
				"dead":      true,
				"unconfirm": false,
			},
		},
	}

	ch := m.Subscribe()
	defer m.Unsubscribe(ch)

	// Discovery now only has "live" (2 of 3 vanished = 67% removal > 50%)
	// "dead" is confirmed dead, "unconfirm" is not
	m.UpdateSessions([]*model.Session{{Name: "live"}})

	// Confirmed dead session should be removed
	if _, ok := m.sessions["dead"]; ok {
		t.Fatalf("confirmed-dead session was not removed")
	}
	// Unconfirmed session should be retained
	if _, ok := m.sessions["unconfirm"]; !ok {
		t.Fatalf("unconfirmed session was wrongly removed")
	}
	// Live session should be retained
	if _, ok := m.sessions["live"]; !ok {
		t.Fatalf("live session was wrongly removed")
	}

	// Verify exactly one removal broadcast (for "dead" only)
	removalCount := 0
	removalNames := []string{}
	for len(ch) > 0 {
		evt := <-ch
		if evt.Type == "session-removed" {
			removalCount++
			removalNames = append(removalNames, evt.Session)
		}
	}
	if removalCount != 1 {
		t.Errorf("expected 1 removal broadcast, got %d", removalCount)
	}
	if len(removalNames) > 0 && removalNames[0] != "dead" {
		t.Errorf("expected removal of 'dead', got %v", removalNames)
	}
}
