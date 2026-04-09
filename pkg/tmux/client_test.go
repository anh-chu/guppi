package tmux

import (
	"os"
	"path/filepath"
	"reflect"
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

func TestWrapSessionCommand(t *testing.T) {
	t.Setenv("SHELL", "/bin/zsh")

	got := wrapSessionCommand("codex --dangerously-bypass-approvals")
	want := []string{"/bin/zsh", "-lc", "codex --dangerously-bypass-approvals; exec /bin/zsh -i"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wrapSessionCommand() = %#v, want %#v", got, want)
	}
}
