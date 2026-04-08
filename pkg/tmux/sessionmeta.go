package tmux

import (
	"regexp"
	"strings"
)

var shellCommands = map[string]bool{
	"":      true,
	"bash":  true,
	"csh":   true,
	"dash":  true,
	"fish":  true,
	"ksh":   true,
	"login": true,
	"sh":    true,
	"tcsh":  true,
	"tmux":  true,
	"zsh":   true,
}

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
	default:
		return ""
	}
}

func IsShellCommand(command string) bool {
	return shellCommands[strings.TrimSpace(strings.ToLower(command))]
}

func PrimaryPane(windows []*Window) *Pane {
	for _, win := range windows {
		if !win.Active {
			continue
		}
		for _, pane := range win.Panes {
			if pane.Active {
				return pane
			}
		}
		if len(win.Panes) > 0 {
			return win.Panes[0]
		}
	}
	for _, win := range windows {
		for _, pane := range win.Panes {
			if pane.Active {
				return pane
			}
		}
		if len(win.Panes) > 0 {
			return win.Panes[0]
		}
	}
	return nil
}

func InferAgentType(windows []*Window, fallback string) string {
	if pane := PrimaryPane(windows); pane != nil {
		if inferred := NormalizeAgentType(pane.CurrentCommand); inferred != "" {
			return inferred
		}
	}
	for _, win := range windows {
		for _, pane := range win.Panes {
			if inferred := NormalizeAgentType(pane.CurrentCommand); inferred != "" {
				return inferred
			}
		}
	}
	return NormalizeAgentType(fallback)
}

func ResolveProjectPath(windows []*Window, fallback string) string {
	if pane := PrimaryPane(windows); pane != nil && strings.TrimSpace(pane.CurrentPath) != "" {
		return strings.TrimSpace(pane.CurrentPath)
	}
	for _, win := range windows {
		for _, pane := range win.Panes {
			if strings.TrimSpace(pane.CurrentPath) != "" {
				return strings.TrimSpace(pane.CurrentPath)
			}
		}
	}
	return strings.TrimSpace(fallback)
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
