package pty

import (
	"encoding/binary"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// findModuleRoot walks up from the current working directory to locate the
// go.mod file, so the test can build the real termyard binary regardless of
// which package directory `go test` is invoked from.
func findModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate module root (go.mod not found)")
		}
		dir = parent
	}
}

// buildTermyardBinary compiles the real termyard CLI binary once for the
// test and returns its path. This is the real entry point used in
// production: pkg/pty.Registry.Start spawns exactly this binary (via
// os.Executable) with the "session-daemon" subcommand and its CLI flags.
// go test binaries cannot re-exec themselves as "session-daemon" (see
// create_bench_test.go), so a real subprocess-spawn regression test must
// build and invoke the actual compiled binary.
func buildTermyardBinary(t *testing.T) string {
	t.Helper()
	root := findModuleRoot(t)
	binPath := filepath.Join(t.TempDir(), "termyard-under-test")
	cmd := exec.Command("go", "build", "-o", binPath, ".")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build failed: %v\n%s", err, out)
	}
	return binPath
}

// TestSessionDaemonSpawn_AcceptsStableIdentityFlags is a regression test for the
// production defect where pkg/pty/registry_stable.go's Start() passes
// --daemon-key/--owner/--session-id/--generation/--command-id to the
// "session-daemon" subcommand, but pkg/commands/sessiondaemon/sessiondaemon.go
// never defined those flags. urfave/cli fataled with "flag provided but not
// defined" and the child process exited immediately, silently, because the
// parent redirects child stdio to /dev/null in production. This test spawns
// the REAL compiled binary with the REAL flag set Start() uses (mirroring
// pkg/pty/registry_stable.go's args construction) and captures stdio
// directly (unlike production) so a regression is loud, not silent.
func TestSessionDaemonSpawn_AcceptsStableIdentityFlags(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}

	binPath := buildTermyardBinary(t)

	socketDir := t.TempDir()
	stateDir := t.TempDir()

	const (
		wantOwner      = "owner-under-test"
		wantSessionID  = "session-under-test"
		wantGeneration = "gen-under-test"
		wantCommandID  = "cmd-under-test"
		daemonKey      = "spawn-test-key"
	)

	args := []string{
		"session-daemon",
		"--id", daemonKey,
		"--daemon-key", daemonKey,
		"--owner", wantOwner,
		"--session-id", wantSessionID,
		"--generation", wantGeneration,
		"--command-id", wantCommandID,
		"--shell", "/bin/sh",
		"--cols", "80",
		"--rows", "24",
		"--cwd", stateDir,
		"--socket-dir", socketDir,
		"--state-dir", stateDir,
	}

	cmd := exec.Command(binPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	var stdout, stderr strings.Builder
	var mu sync.Mutex
	cmd.Stdout = &lockedWriter{mu: &mu, w: &stdout}
	cmd.Stderr = &lockedWriter{mu: &mu, w: &stderr}

	if err := cmd.Start(); err != nil {
		t.Fatalf("start session-daemon subprocess: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_, _ = cmd.Process.Wait()
		}
	})

	socketPath := filepath.Join(socketDir, daemonKey+".sock")
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		if conn, err := net.DialTimeout("unix", socketPath, 200*time.Millisecond); err == nil {
			conn.Close()
			lastErr = nil
			break
		} else {
			lastErr = err
		}
		time.Sleep(25 * time.Millisecond)
	}

	mu.Lock()
	combined := stdout.String() + stderr.String()
	mu.Unlock()

	if strings.Contains(combined, "flag provided but not defined") {
		t.Fatalf("session-daemon rejected stable identity flags as undefined: %s", combined)
	}
	if lastErr != nil {
		t.Fatalf("session-daemon socket never became ready (last dial error: %v); output:\n%s", lastErr, combined)
	}

	// Verify the daemon actually bound using the passed owner/session-id/
	// generation values (not just that flag parsing succeeded) by querying
	// its identity handshake over the real wire protocol.
	info, err := queryIdentityAt(socketPath)
	if err != nil {
		t.Fatalf("identity query failed: %v; output:\n%s", err, combined)
	}
	if info.Owner != wantOwner {
		t.Errorf("identity Owner = %q, want %q", info.Owner, wantOwner)
	}
	if info.SessionID != wantSessionID {
		t.Errorf("identity SessionID = %q, want %q", info.SessionID, wantSessionID)
	}
	if info.Generation != wantGeneration {
		t.Errorf("identity Generation = %q, want %q", info.Generation, wantGeneration)
	}
}

// queryIdentityAt performs the FrameQueryIdentity handshake against an
// arbitrary socket path (mirrors Registry.queryIdentity, which is keyed off
// the registry's own SocketPath rather than an explicit path).
func queryIdentityAt(socketPath string) (ReadyInfo, error) {
	conn, err := net.DialTimeout("unix", socketPath, identityDialTimeout)
	if err != nil {
		return ReadyInfo{}, err
	}
	defer conn.Close()

	if err := conn.SetReadDeadline(time.Now().Add(identityReadTimeout)); err != nil {
		return ReadyInfo{}, err
	}
	if _, err := conn.Write(encodeFrame(FrameQueryIdentity, nil)); err != nil {
		return ReadyInfo{}, err
	}

	// A connection may receive an unsolicited FrameReplay first if the ring
	// buffer already has content (e.g. the shell printed a prompt before we
	// connected). Skip any non-identity frames until the identity response
	// arrives or the read deadline trips.
	header := make([]byte, 5)
	for {
		if _, err := io.ReadFull(conn, header); err != nil {
			return ReadyInfo{}, err
		}
		ftype := header[0]
		plen := binary.BigEndian.Uint32(header[1:5])
		payload := make([]byte, plen)
		if plen > 0 {
			if _, err := io.ReadFull(conn, payload); err != nil {
				return ReadyInfo{}, err
			}
		}
		if ftype != FrameIdentity {
			continue
		}
		return UnmarshalReadyInfo(payload)
	}
}

// lockedWriter serializes concurrent writes from a subprocess's stdout and
// stderr pipes into a shared strings.Builder.
type lockedWriter struct {
	mu *sync.Mutex
	w  *strings.Builder
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
