package model

import (
	"fmt"
	"strings"
)

// SessionForeground describes the foreground command of a session's active pane.
type SessionForeground struct {
	Session string
	Command string
	PID     int
}

// ValidateSessionName returns an error if name contains characters that have
// special meaning and would prevent reliable targeting and would prevent reliable targeting.
func ValidateSessionName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("session name cannot be empty")
	}
	// These characters are reserved:
	//   ~  last marked pane  (causes "no marked target" error)
	//   !  last active session
	//   :  window separator  (foo:1 targets window 1 of session foo)
	const reserved = "~!:"
	for _, r := range reserved {
		if strings.ContainsRune(name, r) {
			return fmt.Errorf("session name cannot contain %q (reserved character)", r)
		}
	}
	return nil
}

// LastLines returns the last n lines of text (trailing newline ignored). n<=0
// or empty text returns text unchanged.
func LastLines(text string, n int) string {
	if n <= 0 || text == "" {
		return text
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
