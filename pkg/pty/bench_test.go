package pty_test

import (
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/anh-chu/termyard/pkg/pty"
)

// Optional tmux comparison benchmark (skipped if tmux not on PATH).
func tmuxPath() string {
	p, _ := exec.LookPath("tmux")
	return p
}

// ptyReader wraps a pty.Session with a single persistent read goroutine to
// avoid leaking blocked goroutines on the PTY fd.
type ptyReader struct {
	sess pty.Session
	ch   chan readResult
	wg   sync.WaitGroup
}

type readResult struct {
	data []byte
	err  error
}

func newPTYReader(sess pty.Session) *ptyReader {
	r := &ptyReader{
		sess: sess,
		ch:   make(chan readResult, 64),
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		buf := make([]byte, 64*1024)
		for {
			n, err := sess.Read(buf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				r.ch <- readResult{data: data}
			}
			if err != nil {
				r.ch <- readResult{err: err}
				return
			}
		}
	}()
	return r
}

// readFor reads all available data for the given duration, returning total bytes.
func (r *ptyReader) readFor(dur time.Duration) int64 {
	var total int64
	deadline := time.After(dur)
	for {
		select {
		case res := <-r.ch:
			if res.err != nil {
				return total
			}
			total += int64(len(res.data))
		case <-deadline:
			return total
		}
	}
}

// readFirst reads until we get at least one byte, with timeout.
func (r *ptyReader) readFirst(timeout time.Duration) (time.Duration, bool) {
	start := time.Now()
	select {
	case res := <-r.ch:
		if res.err != nil {
			return 0, false
		}
		return time.Since(start), true
	case <-time.After(timeout):
		return 0, false
	}
}

// drain reads and discards any buffered data.
func (r *ptyReader) drain(dur time.Duration) {
	deadline := time.After(dur)
	for {
		select {
		case <-r.ch:
		case <-deadline:
			return
		}
	}
}

func TestPTYComparison(t *testing.T) {
	const throughputDuration = 3 * time.Second
	const latencyIterations = 20

	// --- Direct PTY throughput ---
	t.Log("=== DIRECT PTY ===")

	dSess, err := pty.NewDirectPTYSession("/bin/sh", 120, 40, "")
	if err != nil {
		t.Fatal(err)
	}
	dr := newPTYReader(dSess)

	// Warmup
	dSess.Write([]byte("\n"))
	dr.drain(500 * time.Millisecond)

	// Throughput: send seq, measure bytes received in fixed window
	dSess.Write([]byte("seq 1 1000000\n"))
	time.Sleep(20 * time.Millisecond)
	dBytes := dr.readFor(throughputDuration)
	dMbps := float64(dBytes) / throughputDuration.Seconds() / (1024 * 1024)
	t.Logf("  Throughput: %d bytes in %v = %.2f MB/s", dBytes, throughputDuration, dMbps)

	// Latency: echo x, measure first byte
	dr.drain(200 * time.Millisecond)
	var dLatencies []time.Duration
	for i := 0; i < latencyIterations; i++ {
		dr.drain(50 * time.Millisecond)
		dSess.Write([]byte("echo x\n"))
		if lat, ok := dr.readFirst(5 * time.Second); ok {
			dLatencies = append(dLatencies, lat)
		}
		dr.drain(50 * time.Millisecond)
	}
	dSess.Close()

	dAvgLat := avgDuration(dLatencies)
	t.Logf("  Latency (avg of %d): %v", len(dLatencies), dAvgLat)
	t.Logf("  Latency samples: %v", dLatencies)

	// --- Summary ---
	t.Log("")
	t.Log("╔══════════════════════════════════════════════════════╗")
	t.Log("║              DIRECT PTY PERFORMANCE                 ║")
	t.Log("╠══════════════════════════════════════════════════════╣")
	t.Logf("║  Throughput:  %.2f MB/s                             ", dMbps)
	if dAvgLat > 0 {
		t.Logf("║  Latency:     %v                                    ", dAvgLat)
	}
	t.Log("╚══════════════════════════════════════════════════════╝")
}

func avgDuration(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	if len(ds) <= 4 {
		var total time.Duration
		for _, d := range ds {
			total += d
		}
		return total / time.Duration(len(ds))
	}
	// Trimmed mean: drop fastest and slowest
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

// BenchmarkStableEchoLatency measures PTY echo latency through a stable-attached
// daemon connection (re-keyed by SessionID+generation). One daemon is spawned
// and multiple clients connect to it to measure echo latency in isolation from
// cold-start overhead. Baseline: 195.754 µs (direct PTY).
func BenchmarkStableEchoLatency(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping latency benchmark in short mode")
	}

	const latencyIterations = 20

	// Spawn a stable daemon once per benchmark run
	socketDir := b.TempDir()
	stateDir := b.TempDir()
	daemonKey := "bench-echo-stable"

	daemonCfg := pty.DaemonConfig{
		ID:        daemonKey,
		Shell:     "/bin/sh",
		Cols:      120,
		Rows:      40,
		Cwd:       "/tmp",
		SocketDir: socketDir,
		StateDir:  stateDir,
	}

	// Start daemon in background
	go func() {
		_ = pty.RunDaemon(daemonCfg)
	}()

	// Wait for socket to be ready
	socketPath := socketDir + "/" + daemonKey + ".sock"
	if !waitForSocketReady(socketPath, 10*time.Second) {
		b.Fatal("daemon did not become ready")
	}

	// Measure echo latency through the daemon-attached connection
	var latencies []time.Duration

	// Create a daemon session (client connection)
	daemonSess, err := pty.NewDaemonSession(socketPath)
	if err != nil {
		b.Fatal(err)
	}
	defer daemonSess.Close()

	dr := newPTYReader(daemonSess)

	// Warmup
	daemonSess.Write([]byte("\n"))
	dr.drain(500 * time.Millisecond)

	// Measure echo latency
	for i := 0; i < latencyIterations; i++ {
		dr.drain(50 * time.Millisecond)
		daemonSess.Write([]byte("echo x\n"))
		if lat, ok := dr.readFirst(5 * time.Second); ok {
			latencies = append(latencies, lat)
		}
		dr.drain(50 * time.Millisecond)
	}

	avgLat := avgDuration(latencies)
	b.Logf("Stable-attach echo latency (avg of %d): %v", len(latencies), avgLat)
	b.Logf("Samples: %v", latencies)

	// Report as metric (microseconds)
	if avgLat > 0 {
		b.ReportMetric(float64(avgLat.Microseconds()), "µs/op")
	}
}
