package pty_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anh-chu/termyard/pkg/pty"
)

// simpleReader wraps a pty.Session with a single persistent read goroutine.
type simpleReader struct {
	sess pty.Session
	ch   chan readResult
	done chan struct{}
}

func newSimpleReader(sess pty.Session) *simpleReader {
	r := &simpleReader{
		sess: sess,
		ch:   make(chan readResult, 256),
		done: make(chan struct{}),
	}
	go func() {
		defer close(r.ch)
		buf := make([]byte, 64*1024)
		for {
			select {
			case <-r.done:
				return
			default:
			}
			n, err := sess.Read(buf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				select {
				case r.ch <- readResult{data: data}:
				case <-r.done:
					return
				}
			}
			if err != nil {
				select {
				case r.ch <- readResult{err: err}:
				case <-r.done:
					return
				}
				return
			}
		}
	}()
	return r
}

// readFor reads all available data for the given duration, returning total bytes.
func (r *simpleReader) readFor(dur time.Duration) int64 {
	var total int64
	deadline := time.After(dur)
	for {
		select {
		case res, ok := <-r.ch:
			if !ok || res.err != nil {
				return total
			}
			total += int64(len(res.data))
		case <-deadline:
			return total
		}
	}
}

// readFirst reads until we get at least one byte, with timeout.
func (r *simpleReader) readFirst(timeout time.Duration) (time.Duration, bool) {
	start := time.Now()
	select {
	case res, ok := <-r.ch:
		if !ok || res.err != nil {
			return 0, false
		}
		return time.Since(start), true
	case <-time.After(timeout):
		return 0, false
	}
}

// drain reads and discards any buffered data.
func (r *simpleReader) drain(dur time.Duration) {
	deadline := time.After(dur)
	for {
		select {
		case _, ok := <-r.ch:
			if !ok {
				return
			}
		case <-deadline:
			return
		}
	}
}

func (r *simpleReader) stop() {
	close(r.done)
}

func TestDaemonPTYComparison(t *testing.T) {
	if os.Getenv("RUN_PTY_BENCH") == "" {
		t.Skip("skipping PTY benchmark comparison; set RUN_PTY_BENCH=1 to run")
	}

	const throughputSeconds = 3
	const latencyIters = 20

	// ============================================================
	// Test 1: Direct PTY
	// ============================================================
	t.Log("=== DIRECT PTY ===")
	dSess, err := pty.NewDirectPTYSession("/bin/sh", 120, 40, "")
	if err != nil {
		t.Fatal(err)
	}
	dr := newSimpleReader(dSess)

	// Warmup
	dSess.Write([]byte("\n"))
	dr.drain(500 * time.Millisecond)

	dSess.Write([]byte("seq 1 1000000\n"))
	time.Sleep(30 * time.Millisecond)
	dBytes := dr.readFor(time.Duration(throughputSeconds) * time.Second)
	dMBps := float64(dBytes) / float64(throughputSeconds) / (1024 * 1024)
	t.Logf("  Throughput: %d bytes in %ds = %.2f MB/s", dBytes, throughputSeconds, dMBps)

	// Latency
	dSess.Write([]byte{0x03}) // Ctrl+C
	dr.drain(500 * time.Millisecond)
	var dLats []time.Duration
	for i := 0; i < latencyIters; i++ {
		dr.drain(50 * time.Millisecond)
		dSess.Write([]byte("echo x\n"))
		if lat, ok := dr.readFirst(5 * time.Second); ok {
			dLats = append(dLats, lat)
		}
		dr.drain(50 * time.Millisecond)
	}
	dr.stop()
	dSess.Close()
	dAvgLat := trimmedAvg(dLats)

	// ============================================================
	// Test 2: Daemon PTY
	// ============================================================
	t.Log("=== DAEMON PTY ===")
	daemonID := fmt.Sprintf("bench-daemon-%d", os.Getpid())
	socketDir := filepath.Join(os.TempDir(), fmt.Sprintf("termyard-bench-%d", os.Getpid()))
	defer os.RemoveAll(socketDir)

	daemonCfg := pty.DaemonConfig{
		ID:        daemonID,
		Shell:     "/bin/sh",
		Cols:      120,
		Rows:      40,
		SocketDir: socketDir,
		StateDir:  t.TempDir(),
	}

	// Start daemon in background.
	daemonErrCh := make(chan error, 1)
	go func() {
		daemonErrCh <- pty.RunDaemon(daemonCfg)
	}()

	// Wait for socket to appear.
	socketPath := filepath.Join(socketDir, daemonID+".sock")
	if !waitForSocket(socketPath, 5*time.Second) {
		t.Fatal("daemon socket did not appear")
	}

	// Connect to daemon.
	daSess, err := pty.NewDaemonSession(socketPath)
	if err != nil {
		t.Fatalf("connect to daemon: %v", err)
	}
	dar := newSimpleReader(daSess)

	// Warmup
	daSess.Write([]byte("\n"))
	dar.drain(1 * time.Second)

	daSess.Write([]byte("seq 1 1000000\n"))
	time.Sleep(30 * time.Millisecond)
	daBytes := dar.readFor(time.Duration(throughputSeconds) * time.Second)
	daMBps := float64(daBytes) / float64(throughputSeconds) / (1024 * 1024)
	t.Logf("  Throughput: %d bytes in %ds = %.2f MB/s", daBytes, throughputSeconds, daMBps)

	// Latency
	daSess.Write([]byte{0x03}) // Ctrl+C
	dar.drain(500 * time.Millisecond)
	var daLats []time.Duration
	for i := 0; i < latencyIters; i++ {
		dar.drain(50 * time.Millisecond)
		daSess.Write([]byte("echo x\n"))
		if lat, ok := dar.readFirst(5 * time.Second); ok {
			daLats = append(daLats, lat)
		}
		dar.drain(50 * time.Millisecond)
	}
	dar.stop()
	daSess.Kill()
	daAvgLat := trimmedAvg(daLats)

	// Wait for daemon to exit.
	select {
	case err := <-daemonErrCh:
		if err != nil {
			t.Logf("  daemon exited with: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("  daemon did not exit within timeout")
	}

	// ============================================================
	// Summary
	// ============================================================
	t.Log("")
	t.Log("╔══════════════════════════════════════════════════════════════════════╗")
	t.Log("║                PTY PERFORMANCE COMPARISON (2 modes)                 ║")
	t.Log("╠══════════════════════════════════════════════════════════════════════╣")
	if dMBps > 0.001 && daMBps > 0.001 {
		t.Logf("║  Throughput:  Direct=%-8.2f  Daemon=%-8.2f MB/s                ║", dMBps, daMBps)
		t.Logf("║                Direct is %.1fx faster than Daemon                    ║", dMBps/daMBps)
	}
	if dAvgLat > 0 && daAvgLat > 0 {
		t.Logf("║  Latency:     Direct=%-10v  Daemon=%-10v                      ║", dAvgLat, daAvgLat)
	}
	t.Log("╚══════════════════════════════════════════════════════════════════════╝")
}

// waitForSocket polls for a Unix socket file to appear.
func waitForSocket(path string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if info, err := os.Stat(path); err == nil && info.Mode()&os.ModeSocket != 0 {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// trimmedAvg computes trimmed mean (drop min and max if >= 5 samples).
func trimmedAvg(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	if len(ds) < 5 {
		var total time.Duration
		for _, d := range ds {
			total += d
		}
		return total / time.Duration(len(ds))
	}
	minI, maxI := 0, 0
	for i, d := range ds {
		if d < ds[minI] {
			minI = i
		}
		if d > ds[maxI] {
			maxI = i
		}
	}
	var total time.Duration
	count := 0
	for i, d := range ds {
		if i == minI || i == maxI {
			continue
		}
		total += d
		count++
	}
	return total / time.Duration(count)
}


