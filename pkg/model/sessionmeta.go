package model

import (
	"regexp"
	"strings"
)

var promptPrefixes = []string{"❯ ", "> ", "$ ", "# ", "% "}

var questionLine = regexp.MustCompile(`^[A-Z0-9].{0,180}\?$`)

func NormalizeAgentType(command string) string {
	cmd := strings.Trim(strings.ToLower(command), `"' `)
	switch {
	case strings.Contains(cmd, "claude"):
		return "claude"
	case strings.Contains(cmd, "codex"):
		return "codex"
	case strings.Contains(cmd, "copilot"):
		return "copilot"
	case strings.Contains(cmd, "opencode"):
		return "opencode"
	case strings.Contains(cmd, "gemini"):
		return "gemini"
	case cmd == "pi":
		return "pi"
	default:
		return ""
	}
}

func ExtractPromptPreview(content string) string {
	lines := strings.Split(content, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		for _, prefix := range promptPrefixes {
			if strings.HasPrefix(line, prefix) {
				return trimPreview(strings.TrimSpace(strings.TrimPrefix(line, prefix)))
			}
		}
		if questionLine.MatchString(line) {
			return trimPreview(line)
		}
	}
	return ""
}

func trimPreview(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) <= 180 {
		return text
	}
	return strings.TrimSpace(text[:177]) + "..."
}
