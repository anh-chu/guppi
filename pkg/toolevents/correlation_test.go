package toolevents

import "testing"

// TestCorrelationSurvivesRename proves an already-recorded (in-flight) event
// stays correlated by its durable SessionID when the session's display name
// changes. The name field is a mutable alias; the stable id must be the
// correlation identity when present.
func TestCorrelationSurvivesRename(t *testing.T) {
	tr := NewTracker()

	const sid = "abc123def456"
	tr.Record(&Event{
		Tool:      ToolClaude,
		Status:    StatusWaiting,
		Session:   "v1-name",
		SessionID: sid,
		Window:    0,
		Pane:      "%1",
		Message:   "needs approval",
	})

	// Correlation by the stable id resolves the in-flight event regardless of
	// the display name recorded with it.
	got := tr.GetForSession(sid)
	if len(got) != 1 {
		t.Fatalf("GetForSession(%q) = %d events, want 1 (in-flight event lost after keying)", sid, len(got))
	}
	if got[0].SessionID != sid || got[0].Session != "v1-name" {
		t.Fatalf("correlated event = %+v, want SessionID %q / Session %q", got[0], sid, "v1-name")
	}

	// Rename: the session's display name changes but the durable id does not.
	// A renewal of the same session carries the stable id under the new name
	// and must replace the retained event, not duplicate it into a fresh
	// name-keyed slot.
	tr.Record(&Event{
		Tool:      ToolClaude,
		Status:    StatusWaiting,
		Session:   "renamed-name",
		SessionID: sid,
		Window:    0,
		Pane:      "%1",
		Message:   "still needs approval",
	})

	got = tr.GetForSession(sid)
	if len(got) != 1 {
		t.Fatalf("GetForSession(%q) after rename = %d events, want 1 (rename split correlation)", sid, len(got))
	}
	if got[0].Session != "renamed-name" || got[0].Message != "still needs approval" {
		t.Fatalf("post-rename event = %+v, want updated name/message", got[0])
	}

	// The old display name no longer resolves it (it was only ever an alias),
	// but the stable id still does — correlation follows the id, not the name.
	if old := tr.GetForSession("v1-name"); len(old) != 0 {
		t.Fatalf("GetForSession(%q) = %d events, want 0 after rename", "v1-name", len(old))
	}

	// Clear by the durable id removes the retained event even though its stored
	// display name was the renamed one.
	tr.Clear("", sid, 0, "%1")
	if got := tr.GetForSession(sid); len(got) != 0 {
		t.Fatalf("Clear by SessionID left %d events, want 0", len(got))
	}
}

// TestCorrelationFallsBackToName proves legacy events without a SessionID keep
// correlating by session name exactly as before.
func TestCorrelationFallsBackToName(t *testing.T) {
	tr := NewTracker()

	tr.Record(&Event{
		Tool:    ToolCodex,
		Status:  StatusWaiting,
		Session: "legacy-session",
		Window:  0,
		Pane:    "%2",
	})

	got := tr.GetForSession("legacy-session")
	if len(got) != 1 {
		t.Fatalf("GetForSession(%q) = %d events, want 1", "legacy-session", len(got))
	}
	if got[0].SessionID != "" || got[0].Session != "legacy-session" {
		t.Fatalf("legacy event = %+v, want no SessionID and name preserved", got[0])
	}

	tr.Clear("", "legacy-session", 0, "%2")
	if got := tr.GetForSession("legacy-session"); len(got) != 0 {
		t.Fatalf("Clear by name left %d events, want 0", len(got))
	}
}
