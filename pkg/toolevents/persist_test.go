package toolevents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestPersistRoundTrip verifies retained waiting events and session metadata
// survive a "restart": a fresh tracker loading the same file sees them again.
func TestPersistRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "toolevents.json")

	tr := NewTracker()
	tr.path = path
	tr.Record(&Event{
		Tool:       ToolClaude,
		Status:     StatusWaiting,
		Session:    "work",
		Window:     0,
		Pane:       "%1",
		Message:    "needs approval",
		UserPrompt: "fix the bug",
	})

	// Simulate restart: new tracker, same file.
	tr2 := NewTracker()
	tr2.path = path
	tr2.load()

	got := tr2.RetainedWaitingForPane("%1")
	if got == nil {
		t.Fatal("waiting event not restored after reload")
	}
	if got.Message != "needs approval" {
		t.Fatalf("message = %q, want %q", got.Message, "needs approval")
	}

	meta, ok := tr2.sessionMeta["\x00work"]
	if !ok || meta.UserPrompt != "fix the bug" {
		t.Fatalf("session meta not restored: %+v (ok=%v)", meta, ok)
	}
}

// TestLoadDropsLegacyArtifactKeys verifies that load() skips artifact entries
// lacking the "host\x00session" separator (legacy bare-session-name keys from
// before cross-host scoping was implemented). New-format keys load correctly.
func TestLoadDropsLegacyArtifactKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "toolevents.json")

	// Hand-craft a persisted-state JSON with one legacy bare-key artifact
	// and one new-format artifact.
	legacyArt := &FileArtifact{
		Path:   "/legacy/output.txt",
		Name:   "output.txt",
		Tool:   ToolPi,
		Source: "regex",
	}
	newFormatArt := &FileArtifact{
		Path:   "/hostA/result.log",
		Name:   "result.log",
		Tool:   ToolClaude,
		Source: "hook",
	}

	st := persistedState{
		Artifacts: map[string][]*FileArtifact{
			"main":          {legacyArt},    // legacy: bare session name, no host\x00
			"hostA\x00main": {newFormatArt}, // new: scoped by host
		},
	}
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	// Load into a fresh tracker.
	tr := NewTracker()
	tr.path = path
	tr.load()

	// New-format entry should load correctly.
	gotNew := tr.GetArtifacts("hostA", "main")
	if len(gotNew) != 1 || gotNew[0].Path != newFormatArt.Path {
		t.Errorf("new-format artifact not loaded: got %v, want %v", gotNew, newFormatArt)
	}

	// Legacy bare-key should NOT be accessible (because load() skipped it).
	// GetArtifacts("", "main") would look for key "\x00main", not "main".
	gotLegacy := tr.GetArtifacts("", "main")
	if len(gotLegacy) != 0 {
		t.Errorf("legacy bare-key artifact unexpectedly loaded: got %v, want empty", gotLegacy)
	}

	// Direct check: the internal artifacts map should not have the bare "main" key.
	_, ok := tr.artifacts["main"]
	if ok {
		t.Errorf("legacy bare key still in artifacts map: tr.artifacts[\"main\"] exists, want dropped")
	}
}
