package tmux

import "testing"

func TestNormalizeAgentType(t *testing.T) {
	tests := map[string]string{
		"claude":                      "claude",
		"codex":                       "codex",
		"node /usr/lib/codex.js":      "codex",
		`"claude --dangerously-skip"`: "claude",
		"gemini":                      "gemini",
		"bash":                        "",
	}
	for input, want := range tests {
		if got := NormalizeAgentType(input); got != want {
			t.Fatalf("NormalizeAgentType(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestResolveProjectPathPrefersActivePane(t *testing.T) {
	windows := []*Window{
		{
			Active: true,
			Panes: []*Pane{
				{Active: false, CurrentPath: "/tmp/other"},
				{Active: true, CurrentPath: "/tmp/project"},
			},
		},
	}
	if got := ResolveProjectPath(windows, ""); got != "/tmp/project" {
		t.Fatalf("ResolveProjectPath() = %q, want %q", got, "/tmp/project")
	}
}

func TestExtractPromptPreview(t *testing.T) {
	content := "output\n❯ first prompt\nok\n❯ second prompt\n"
	if got := ExtractPromptPreview(content); got != "second prompt" {
		t.Fatalf("ExtractPromptPreview() = %q, want %q", got, "second prompt")
	}
}
