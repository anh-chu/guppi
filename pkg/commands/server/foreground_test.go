package server

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/anh-chu/termyard/pkg/model"
)

func TestNewForegroundProvider(t *testing.T) {
	p := newForegroundProvider()
	if p == nil {
		t.Fatal("newForegroundProvider returned nil")
	}
}

func TestForegroundProviderFindsChild(t *testing.T) {
	p := newForegroundProvider()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Spawn a shell whose child is a long-running sleep. The shell PID is what
	// we pass to the provider; it should discover "sleep" as the foreground
	// command. Background the sleep and wait so the provider sees a stable
	// direct child even on systems where a bare `bash -c sleep` re-execs.
	cmd := exec.CommandContext(ctx, "bash", "-c", "sleep 60 & wait")
	if err := cmd.Start(); err != nil {
		t.Skipf("bash not available: %v", err)
	}
	defer cmd.Process.Kill()

	shellPid := cmd.Process.Pid
	var cmdName string
	var found bool
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		cmdName, found = p.Foreground(shellPid)
		if found && cmdName == "sleep" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("expected foreground 'sleep' for shell %d, got %q, found=%v", shellPid, cmdName, found)
}

func TestForegroundProviderIdleShell(t *testing.T) {
	p := newForegroundProvider()
	cmd := exec.Command("sleep", "1")
	if err := cmd.Start(); err != nil {
		t.Skipf("sleep not available: %v", err)
	}
	defer cmd.Process.Kill()

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, ok := p.Foreground(cmd.Process.Pid); ok {
			t.Fatalf("expected no foreground process for childless pid %d", cmd.Process.Pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSessionRefParsesCurrentIdentifiers(t *testing.T) {
	ref, err := model.ParseSessionRef("session-a:0.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ref.Session != "session-a" || ref.Window != 0 || ref.Pane != 0 {
		t.Fatalf("unexpected ref: %+v", ref)
	}
}
