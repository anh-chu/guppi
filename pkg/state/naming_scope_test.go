package state

import (
	"testing"

	"github.com/anh-chu/termyard/pkg/model"
	"github.com/anh-chu/termyard/pkg/toolevents"
)

// newMetaManager builds a bare Manager suitable for exercising
// UpdateSessionMetadataFromEvent. namer is nil so the async naming goroutine
// no-ops and only the synchronous metadata bookkeeping is under test.
func newMetaManager() *Manager {
	return &Manager{
		sessions: map[string]*model.Session{"s": {Name: "s"}},
		meta:     map[string]SessionMetadata{},
	}
}

func TestNamingScope_CountsDistinctUserPrompts(t *testing.T) {
	m := newMetaManager()

	m.UpdateSessionMetadataFromEvent(&toolevents.Event{
		Session: "s", AgentSessionID: "a1", UserPrompt: "first",
	})
	m.UpdateSessionMetadataFromEvent(&toolevents.Event{
		Session: "s", AgentSessionID: "a1", UserPrompt: "second",
	})
	// A duplicate prompt must not bump the count.
	m.UpdateSessionMetadataFromEvent(&toolevents.Event{
		Session: "s", AgentSessionID: "a1", UserPrompt: "second",
	})

	m.mu.RLock()
	meta := m.meta["s"]
	m.mu.RUnlock()

	if meta.UserPromptCount != 2 {
		t.Fatalf("expected UserPromptCount 2, got %d", meta.UserPromptCount)
	}
	if meta.UserPrompt != "first" {
		t.Fatalf("expected first prompt preserved, got %q", meta.UserPrompt)
	}
}

func TestNamingScope_NewAgentSessionResetsNamingContext(t *testing.T) {
	m := newMetaManager()

	m.UpdateSessionMetadataFromEvent(&toolevents.Event{
		Session: "s", AgentSessionID: "a1", UserPrompt: "first",
	})
	m.UpdateSessionMetadataFromEvent(&toolevents.Event{
		Session: "s", AgentSessionID: "a1", UserPrompt: "second",
	})
	// Simulate an AI name having been applied for the first agent session.
	m.mu.Lock()
	meta := m.meta["s"]
	meta.DisplayName = "old-name"
	meta.NameAssigned = true
	m.meta["s"] = meta
	m.mu.Unlock()

	// A second agent session starts in the same shell session.
	m.UpdateSessionMetadataFromEvent(&toolevents.Event{
		Session: "s", AgentSessionID: "a2", UserPrompt: "fresh work",
	})

	m.mu.RLock()
	got := m.meta["s"]
	m.mu.RUnlock()

	if got.AgentSessionID != "a2" {
		t.Fatalf("expected AgentSessionID a2, got %q", got.AgentSessionID)
	}
	if got.DisplayName != "" {
		t.Fatalf("expected DisplayName cleared for new agent session, got %q", got.DisplayName)
	}
	if got.NameAssigned {
		t.Fatal("expected NameAssigned reset for new agent session")
	}
	if got.UserPrompt != "fresh work" {
		t.Fatalf("expected UserPrompt to be the new agent's first prompt, got %q", got.UserPrompt)
	}
	if got.UserPromptCount != 1 {
		t.Fatalf("expected UserPromptCount reset to 1, got %d", got.UserPromptCount)
	}
}

func TestNamingScope_UserSetNameSurvivesAgentSessionChange(t *testing.T) {
	m := newMetaManager()

	m.UpdateSessionMetadataFromEvent(&toolevents.Event{
		Session: "s", AgentSessionID: "a1", UserPrompt: "first",
	})
	m.mu.Lock()
	meta := m.meta["s"]
	meta.DisplayName = "my-label"
	meta.UserSetName = true
	m.meta["s"] = meta
	m.mu.Unlock()

	m.UpdateSessionMetadataFromEvent(&toolevents.Event{
		Session: "s", AgentSessionID: "a2", UserPrompt: "fresh work",
	})

	m.mu.RLock()
	got := m.meta["s"]
	m.mu.RUnlock()

	if got.DisplayName != "my-label" || !got.UserSetName {
		t.Fatalf("expected manual name preserved, got %q userSet=%v", got.DisplayName, got.UserSetName)
	}
}
