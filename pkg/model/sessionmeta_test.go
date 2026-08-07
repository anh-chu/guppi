package model

import "testing"

func TestNormalizeAgentType(t *testing.T) {
	tests := map[string]string{
		"claude":                      "claude",
		"codex":                       "codex",
		"pi":                          "pi",
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

func TestExtractPromptPreview(t *testing.T) {
	content := "output\n❯ first prompt\nok\n❯ second prompt\n"
	if got := ExtractPromptPreview(content); got != "second prompt" {
		t.Fatalf("ExtractPromptPreview() = %q, want %q", got, "second prompt")
	}
}
