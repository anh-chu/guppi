package server

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/anh-chu/termyard/pkg/peer"
	"github.com/anh-chu/termyard/pkg/pty"
	"github.com/anh-chu/termyard/pkg/state"
)

// fakeRegistry records List calls and returns a stable snapshot.
type fakeRegistry struct {
	pty.Registry
	listCalls int
	sessions  []pty.SessionInfo
}

func (f *fakeRegistry) List() []pty.SessionInfo {
	f.listCalls++
	return f.sessions
}

func TestRuntimeReadyWithoutPolling(t *testing.T) {
	// The runtime must become ready immediately; any 10 ms hub-polling loop
	// would introduce visible latency here.
	t.Setenv("TERMYARD_SESSION_DIR", t.TempDir())
	rt, err := newRuntime(&cli.Command{})
	if err != nil {
		t.Fatalf("newRuntime: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if err := rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer rt.Stop()

	select {
	case <-rt.Ready():
	case <-ctx.Done():
		t.Fatal("runtime did not become ready before context timeout")
	}
}

func TestRuntimeCancellationStops(t *testing.T) {
	t.Setenv("TERMYARD_SESSION_DIR", t.TempDir())
	rt, err := newRuntime(&cli.Command{})
	if err != nil {
		t.Fatalf("newRuntime: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	if err := rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	<-rt.Ready()
	cancel()

	select {
	case <-rt.ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("runtime context was not cancelled after Stop/cancel")
	}
}

func TestDaemonAdapterSharesSnapshot(t *testing.T) {
	sessions := []pty.SessionInfo{
		{ID: "alpha", Cwd: "/tmp/alpha"},
		{ID: "beta", Cwd: "/tmp/beta"},
	}
	reg := &fakeRegistry{sessions: sessions}
	adapter := &daemonAdapter{reg: reg}

	// Both interface shapes can be assigned without a second conversion.
	var _ state.DaemonRegistry = adapter
	var _ peer.DaemonRegistry = adapter

	// refresh now takes SessionInfo slice directly (updated from registry by runtime)
	adapter.refresh(sessions)

	// List returns the cached snapshot.
	list := adapter.List()
	if len(list) != 2 {
		t.Fatalf("List returned %d sessions, want 2", len(list))
	}

	// Verify snapshot is correct
	if list[0].ID != "alpha" || list[1].ID != "beta" {
		t.Fatalf("snapshot has wrong sessions: got %v", list)
	}

	// Mutating the returned slice must not affect later callers.
	list[0].ID = "mutated"
	if got := adapter.List()[0].ID; got != "alpha" {
		t.Fatalf("shared slice was mutated through List copy: got %q", got)
	}
}

func TestExecuteReturnsAssemblyError(t *testing.T) {
	// Lock the config directory so identity loading fails and we exercise the
	// error path without starting a server.
	dir := t.TempDir()
	badFile := fmt.Sprintf("%s/.config", dir)
	if err := os.WriteFile(badFile, []byte("x"), 0o600); err == nil {
		t.Setenv("HOME", dir)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := Execute(ctx, &cli.Command{}); err == nil {
		t.Fatal("expected Execute to return an assembly error")
	}
}
