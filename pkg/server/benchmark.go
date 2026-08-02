package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/anh-chu/termyard/pkg/pty"
)

const (
	// benchmarkTimeout bounds each individual benchmark run.
	benchmarkTimeout = 30 * time.Second
	// benchmarkRuns is the number of samples per benchmark.
	benchmarkRuns = 3
)

type benchmarkResult struct {
	ThroughputMbps     float64 `json:"throughput_mbps"`
	FirstByteLatencyUs int64   `json:"first_byte_latency_us"`
	TotalBytes         int64   `json:"total_bytes"`
	ElapsedMs          int64   `json:"elapsed_ms"`
}

type latencySample struct {
	latency time.Duration
	err     error
}

type benchmarkResponse struct {
	Direct benchmarkResult `json:"direct"`
}

func handlePTYBenchmark(w http.ResponseWriter, r *http.Request, opts *Options) {
	var directRes benchmarkResult

	// ---- Direct PTY benchmark ----
	for i := 0; i < benchmarkRuns; i++ {
		res, err := runDirectBenchmark()
		if err != nil {
			logrus.WithError(err).Warn("direct benchmark failed")
			http.Error(w, fmt.Sprintf("direct benchmark failed: %v", err), http.StatusInternalServerError)
			return
		}
		directRes.ThroughputMbps += res.ThroughputMbps
		directRes.FirstByteLatencyUs += res.FirstByteLatencyUs
		directRes.TotalBytes += res.TotalBytes
		directRes.ElapsedMs += res.ElapsedMs
	}
	avg(&directRes, benchmarkRuns)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(benchmarkResponse{
		Direct: directRes,
	})
}

func avg(r *benchmarkResult, n int) {
	if n <= 1 {
		return
	}
	r.ThroughputMbps /= float64(n)
	r.FirstByteLatencyUs /= int64(n)
	r.TotalBytes /= int64(n)
	r.ElapsedMs /= int64(n)
}

// runDirectBenchmark measures throughput and first-byte latency on a direct PTY.
func runDirectBenchmark() (benchmarkResult, error) {
	sess, err := pty.NewDirectPTYSession("", 120, 40, "")
	if err != nil {
		return benchmarkResult{}, fmt.Errorf("create direct PTY: %w", err)
	}
	defer sess.Close()

	return runThroughputBenchmark(sess)
}

// runThroughputBenchmark sends a throughput command and measures timing.
func runThroughputBenchmark(sess pty.Session) (benchmarkResult, error) {
	// Warm up: send a simple echo to prime the PTY
	_ = writeAll(sess, "echo warmup\n")
	time.Sleep(200 * time.Millisecond)
	drainBuffer(sess) // drain warmup output

	// First-byte latency: send "echo x" and measure time to first output byte
	latencies := make([]latencySample, 0, 5)
	for i := 0; i < 5; i++ {
		drainBuffer(sess)
		_ = writeAll(sess, "echo x\n")
		start := time.Now()
		buf := make([]byte, 1)
		_, readErr := readWithDeadline(sess, buf, benchmarkTimeout)
		elapsed := time.Since(start)
		latencies = append(latencies, latencySample{latency: elapsed, err: readErr})
	}
	drainBuffer(sess)

	// Average first-byte latency, ignoring fastest and slowest
	firstByteUs := avgLatency(latencies)
	if firstByteUs < 0 {
		// Fall back to simple average
		var total time.Duration
		var count int
		for _, s := range latencies {
			if s.err == nil {
				total += s.latency
				count++
			}
		}
		if count > 0 {
			firstByteUs = int64(total) / int64(count) / 1000
		}
	}

	// Throughput: send seq 1 50000 and measure bytes/time until EOF
	drainBuffer(sess)
	_ = writeAll(sess, "stty -echo 2>/dev/null; seq 1 50000; echo 'BENCHMARK_DONE'\n")

	result := readTimed(sess, benchmarkTimeout)

	return benchmarkResult{
		ThroughputMbps:     result.throughputMbps,
		FirstByteLatencyUs: firstByteUs,
		TotalBytes:         result.totalBytes,
		ElapsedMs:          result.elapsed.Milliseconds(),
	}, nil
}

type readTimedResult struct {
	totalBytes     int64
	elapsed        time.Duration
	throughputMbps float64
}

func readTimed(sess pty.Session, timeout time.Duration) readTimedResult {
	var totalBytes int64
	start := time.Now()
	deadline := start.Add(timeout)
	doneMarker := []byte("BENCHMARK_DONE")
	buf := make([]byte, 64*1024)
	// Track last bytes seen for done-marker detection
	tail := make([]byte, 0, len(doneMarker)+64*1024)

	for time.Now().Before(deadline) {
		sessEnd := time.Now().Add(500 * time.Millisecond)
		for time.Now().Before(sessEnd) {
			n, err := readNonblocking(sess, buf)
			if n > 0 {
				totalBytes += int64(n)
				// Check for done marker in the last chunk
				tail = append(tail, buf[:n]...)
				if len(tail) > len(doneMarker)*2 {
					tail = tail[len(tail)-len(doneMarker)*2:]
				}
			}
			if err != nil {
				// EOF or error — we're done
				goto done
			}
			if containsBytes(tail, doneMarker) {
				goto done
			}
		}
	}

done:
	elapsed := time.Since(start)
	var throughputMbps float64
	if elapsed > 0 && totalBytes > 0 {
		throughputMbps = float64(totalBytes) / elapsed.Seconds() / (1024 * 1024) * 8
	}
	return readTimedResult{
		totalBytes:     totalBytes,
		elapsed:        elapsed,
		throughputMbps: throughputMbps,
	}
}

func avgLatency(samples []latencySample) int64 {
	if len(samples) < 3 {
		return -1
	}
	// Drop fastest and slowest, average the rest
	var total time.Duration
	var count int
	fastest := samples[0].latency
	slowest := samples[0].latency
	for _, s := range samples {
		if s.err != nil {
			continue
		}
		if s.latency < fastest {
			fastest = s.latency
		}
		if s.latency > slowest {
			slowest = s.latency
		}
	}
	for _, s := range samples {
		if s.err != nil {
			continue
		}
		if s.latency != fastest && s.latency != slowest {
			total += s.latency
			count++
		}
	}
	if count == 0 {
		// Fall back to simple average
		for _, s := range samples {
			if s.err == nil {
				total += s.latency
				count++
			}
		}
	}
	if count == 0 {
		return -1
	}
	return int64(total) / int64(count) / 1000 // microseconds
}

func writeAll(sess pty.Session, data string) error {
	for len(data) > 0 {
		n, err := sess.Write([]byte(data))
		if err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

func drainBuffer(sess pty.Session) {
	buf := make([]byte, 32*1024)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	for {
		readDone := make(chan struct{})
		go func() {
			for {
				_, err := sess.Read(buf)
				if err != nil {
					break
				}
			}
			close(readDone)
		}()
		select {
		case <-readDone:
			return
		case <-ctx.Done():
			return
		}
	}
}

func readWithDeadline(sess pty.Session, buf []byte, timeout time.Duration) (int, error) {
	type result struct {
		n   int
		err error
	}
	ch := make(chan result, 1)
	go func() {
		n, err := sess.Read(buf)
		ch <- result{n, err}
	}()
	select {
	case r := <-ch:
		return r.n, r.err
	case <-time.After(timeout):
		return 0, fmt.Errorf("read deadline exceeded")
	}
}

func readNonblocking(sess pty.Session, buf []byte) (int, error) {
	type result struct {
		n   int
		err error
	}
	ch := make(chan result, 1)
	go func() {
		n, err := sess.Read(buf)
		ch <- result{n, err}
	}()
	select {
	case r := <-ch:
		return r.n, r.err
	case <-time.After(100 * time.Millisecond):
		return 0, nil
	}
}

func containsBytes(data, sub []byte) bool {
	if len(sub) == 0 {
		return false
	}
	for i := 0; i <= len(data)-len(sub); i++ {
		if string(data[i:i+len(sub)]) == string(sub) {
			return true
		}
	}
	return false
}
