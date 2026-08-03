package pty_test

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anh-chu/termyard/pkg/pty"
)

// waitForSocketReady polls for a session socket to accept connections.
func waitForSocketReady(path string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("unix", path, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return false
}

// BenchmarkDaemonCreateToSocketReady measures local create acceptance-to-
// terminal-connect time: from spawning the session daemon to its socket
// accepting connections.  We use pty.RunDaemon directly because test binaries
// cannot re-exec the `session-daemon` subcommand that Registry.Create relies
// on; the resulting time is the server-side daemon cold-start baseline.
func BenchmarkDaemonCreateToSocketReady(b *testing.B) {
	if os.Getenv("RUN_BASELINE") == "" {
		b.Skip("set RUN_BASELINE=1 to collect create-to-connect baseline")
	}

	for _, n := range []int{1, 5} {
		b.Run(fmt.Sprintf("creates-%d", n), func(b *testing.B) {
			var total time.Duration
			var successes int

			for i := 0; i < b.N; i++ {
				socketDir := b.TempDir()
				stateDir := b.TempDir()
				b.Setenv("TERMYARD_SESSION_DIR", socketDir)

				store, err := pty.NewLifecycleStore(stateDir)
				if err != nil {
					b.Fatal(err)
				}

				start := time.Now()
				for j := 0; j < n; j++ {
					name := fmt.Sprintf("bench-create-%d-%d", i, j)
					cfg := pty.DaemonConfig{
						ID:        name,
						Shell:     "/bin/sh",
						Cols:      120,
						Rows:      40,
						Cwd:       "/tmp",
						SocketDir: socketDir,
						StateDir:  stateDir,
					}
					go func(c pty.DaemonConfig) {
						_ = pty.RunDaemon(c)
					}(cfg)
				}

				lastName := fmt.Sprintf("bench-create-%d-%d", i, n-1)
				socketPath := filepath.Join(socketDir, lastName+".sock")
				if waitForSocketReady(socketPath, 10*time.Second) {
					total += time.Since(start)
					successes++
				}

				reg := pty.NewRegistry(socketDir)
				reg.SetLifecycleStore(store)
				for j := 0; j < n; j++ {
					name := fmt.Sprintf("bench-create-%d-%d", i, j)
					_ = reg.Kill(name) // best-effort cleanup
				}
			}

			if successes == 0 {
				b.Fatal("no session reached a connectable socket")
			}
			avg := total / time.Duration(successes)
			b.ReportMetric(float64(avg.Nanoseconds())/1e6, "ms/op")
			b.Logf("create-to-connect (%d session batch): %v", n, avg)
		})
	}
}
