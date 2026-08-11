package toolevents

import (
	"testing"
)

// TestIsWriteToolMatchesToolName verifies that IsWriteTool filters on actual
// tool names (e.g. "write", "edit", "str_replace_editor"), not agent types
// (e.g. "claude", "pi"), and is case-insensitive.
func TestIsWriteToolMatchesToolName(t *testing.T) {
	tests := []struct {
		toolName string
		want     bool
	}{
		// Write-list tools (lowercase)
		{"write", true},
		{"edit", true},
		{"multiedit", true},
		{"str_replace", true},
		{"str_replace_editor", true},
		{"apply_patch", true},
		{"notebook_edit", true},

		// Case-insensitive (Claude capitalizes Write/Edit)
		{"Write", true},
		{"Edit", true},
		{"WRITE", true},
		{"Str_Replace_Editor", true},

		// Not in allowlist
		{"read", false},
		{"grep", false},
		{"glob", false},
		{"bash", false},

		// Agent types (not tool names) should NOT match
		{"claude", false},
		{"pi", false},
		{"copilot", false},

		// Camel case without underscores (not in allowlist)
		{"StrReplace", false},

		// Empty
		{"", false},
	}

	for _, tc := range tests {
		t.Run(tc.toolName, func(t *testing.T) {
			got := IsWriteTool(tc.toolName)
			if got != tc.want {
				t.Errorf("IsWriteTool(%q) = %v, want %v", tc.toolName, got, tc.want)
			}
		})
	}
}

// TestEvictArtifactPersists verifies that EvictArtifact persists changes
// so they survive a load() round-trip.
func TestEvictArtifactPersists(t *testing.T) {
	path := tmpPath(t)

	// Create tracker, store artifact, then evict it
	tr := NewTracker()
	tr.path = path
	art := &FileArtifact{
		Path:   "/tmp/file.txt",
		Name:   "file.txt",
		Tool:   ToolPi,
		Source: "hook",
	}
	tr.storeArtifacts("", "main", []*FileArtifact{art})
	if got := tr.GetArtifacts("", "main"); len(got) != 1 {
		t.Fatalf("artifact not stored: got %v", got)
	}

	// Evict the artifact
	tr.EvictArtifact("", "main", "/tmp/file.txt")
	if got := tr.GetArtifacts("", "main"); len(got) != 0 {
		t.Fatalf("artifact not evicted: got %v", got)
	}

	// Simulate restart: new tracker loads the file
	tr2 := NewTracker()
	tr2.path = path
	tr2.load()

	// Verify eviction persisted
	got := tr2.GetArtifacts("", "main")
	if len(got) != 0 {
		t.Errorf("eviction not persisted: got %v, want empty", got)
	}
}

// TestClearArtifactsPersists verifies that ClearArtifacts persists changes.
func TestClearArtifactsPersists(t *testing.T) {
	path := tmpPath(t)

	// Create tracker, store artifacts, then clear them
	tr := NewTracker()
	tr.path = path
	art1 := &FileArtifact{
		Path:   "/tmp/file1.txt",
		Name:   "file1.txt",
		Tool:   ToolPi,
		Source: "hook",
	}
	art2 := &FileArtifact{
		Path:   "/tmp/file2.txt",
		Name:   "file2.txt",
		Tool:   ToolClaude,
		Source: "hook",
	}
	tr.storeArtifacts("", "main", []*FileArtifact{art1, art2})
	if got := tr.GetArtifacts("", "main"); len(got) != 2 {
		t.Fatalf("artifacts not stored: got %v", got)
	}

	// Clear all artifacts
	tr.ClearArtifacts("", "main")
	if got := tr.GetArtifacts("", "main"); len(got) != 0 {
		t.Fatalf("artifacts not cleared: got %v", got)
	}

	// Simulate restart: new tracker loads the file
	tr2 := NewTracker()
	tr2.path = path
	tr2.load()

	// Verify clear persisted
	got := tr2.GetArtifacts("", "main")
	if len(got) != 0 {
		t.Errorf("clear not persisted: got %v, want empty", got)
	}
}

// TestEvictArtifactHostScoped verifies that EvictArtifact only affects the
// artifacts for the specified host.
func TestEvictArtifactHostScoped(t *testing.T) {
	tr := NewTracker()

	// Store same path on different hosts
	art := &FileArtifact{
		Path:   "/shared/file.txt",
		Name:   "file.txt",
		Tool:   ToolPi,
		Source: "hook",
	}
	tr.storeArtifacts("hostA", "main", []*FileArtifact{art})
	tr.storeArtifacts("hostB", "main", []*FileArtifact{art})

	// Evict from hostA only
	tr.EvictArtifact("hostA", "main", "/shared/file.txt")

	// hostA should be empty, hostB should still have the artifact
	gotA := tr.GetArtifacts("hostA", "main")
	gotB := tr.GetArtifacts("hostB", "main")

	if len(gotA) != 0 {
		t.Errorf("hostA: got %v, want empty", gotA)
	}
	if len(gotB) != 1 {
		t.Errorf("hostB: got %v, want 1 artifact", gotB)
	}
}

// TestClearArtifactsHostScoped verifies that ClearArtifacts only affects the
// artifacts for the specified host.
func TestClearArtifactsHostScoped(t *testing.T) {
	tr := NewTracker()

	// Store artifacts on different hosts
	art := &FileArtifact{
		Path:   "/tmp/file.txt",
		Name:   "file.txt",
		Tool:   ToolPi,
		Source: "hook",
	}
	tr.storeArtifacts("hostA", "main", []*FileArtifact{art})
	tr.storeArtifacts("hostB", "main", []*FileArtifact{art})

	// Clear from hostA only
	tr.ClearArtifacts("hostA", "main")

	// hostA should be empty, hostB should still have the artifact
	gotA := tr.GetArtifacts("hostA", "main")
	gotB := tr.GetArtifacts("hostB", "main")

	if len(gotA) != 0 {
		t.Errorf("hostA: got %v, want empty", gotA)
	}
	if len(gotB) != 1 {
		t.Errorf("hostB: got %v, want 1 artifact", gotB)
	}
}

// tmpPath creates a temporary file path for testing.
func tmpPath(t *testing.T) string {
	return t.TempDir() + "/artifacts.json"
}
