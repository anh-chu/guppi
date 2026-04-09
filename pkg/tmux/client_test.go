package tmux

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandSessionPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir() failed: %v", err)
	}

	tests := map[string]string{
		"":               "",
		"/tmp/project":   "/tmp/project",
		"~/project":      filepath.Join(home, "project"),
		"~":              home,
		"~other/project": "~other/project",
	}

	for input, want := range tests {
		if got := expandSessionPath(input); got != want {
			t.Fatalf("expandSessionPath(%q) = %q, want %q", input, got, want)
		}
	}
}
