package wikilite

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
)

// Status mirrors the HTTP /api/wiki/status contract.
type Status struct {
	Installed   bool   `json:"installed"`
	Installing  bool   `json:"installing"`
	Running     bool   `json:"running"`
	Version     string `json:"version"`
	Error       string `json:"error"`
	DefaultRoot string `json:"default_root"`
}

// Supervisor manages a wiki-viewer-lite child process. It is safe for
// concurrent use; all exported methods lock an internal mutex.
type Supervisor struct {
	mu         sync.Mutex
	cond       *sync.Cond
	port       int    // guarded by mu; 0 when not running
	procErr    string // last exit error, guarded by mu
	installing bool   // guarded by mu
	loopCtx    context.Context
	loopCancel context.CancelFunc
	logger     *logrus.Entry

	// cmd is the live child, guarded by mu. Stop() needs it to signal the
	// process group: the loop spends nearly all its time blocked in
	// cmd.Wait(), so cancelling the context alone never reaches it.
	cmd *exec.Cmd

	// loopDone is closed by the loop when it returns, so Stop() can wait for
	// a real teardown instead of assuming one.
	loopDone chan struct{}

	// paused blocks the loop from spawning while an install swaps the very
	// directory the child runs from. Guarded by mu.
	paused bool

	// parked is true while the loop sits in cond.Wait(). pause() needs it to
	// tell "no child right now" from "the loop is between the pause gate and
	// publishing a child it just spawned", which look identical through s.cmd
	// alone.
	parked bool

	// Fields set by the supervision loop goroutine.
	started  atomic.Bool
	stopping atomic.Bool
}

// NewSupervisor creates a supervisor. Call Start() to begin the supervision
// loop; Stop() tears it down.
func NewSupervisor() *Supervisor {
	s := &Supervisor{
		logger:   logrus.WithField("component", "wikilite"),
		loopDone: make(chan struct{}),
	}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// Status returns the current state for the HTTP endpoint.
func (s *Supervisor) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()

	v := InstalledVersion()
	running := s.port != 0
	defaultRoot := ""
	if home, err := homeDir(); err == nil {
		defaultRoot = home
	}
	return Status{
		Installed:   Installed(),
		Installing:  s.installing,
		Running:     running,
		Version:     v,
		Error:       s.procErr,
		DefaultRoot: defaultRoot,
	}
}

// Port returns the current listen port and whether the child is running.
func (s *Supervisor) Port() (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.port == 0 {
		return 0, false
	}
	return s.port, true
}

// StartInstall reserves the installing state synchronously and then runs the
// install in the background, so an HTTP handler can answer at once. The work
// deliberately does not use the request context: a roughly 40MB fetch plus
// extract must not be cancelled part-way through a directory swap because a
// client timed out.
func (s *Supervisor) StartInstall() error {
	if missing := firstMissingTool(); missing != "" {
		return fmt.Errorf("%s not found on PATH", missing)
	}

	s.mu.Lock()
	if s.installing {
		s.mu.Unlock()
		return fmt.Errorf("already installing")
	}
	s.installing = true
	s.mu.Unlock()

	go func() {
		if err := s.runInstall(context.Background()); err != nil {
			s.logger.WithError(err).Warn("wiki-viewer-lite install failed")
		}
	}()
	return nil
}

// runInstall performs the install. The caller must already have reserved the
// installing flag.
func (s *Supervisor) runInstall(ctx context.Context) error {
	// Download and extract while the child keeps serving. Only the rename
	// needs the child stopped, which keeps the outage to milliseconds instead
	// of the length of a 40MB fetch.
	staging, err := Stage(ctx)
	if err == nil {
		s.pause()
		err = Commit(staging)
		s.resume()
	}

	s.mu.Lock()
	s.installing = false
	if err != nil {
		s.procErr = err.Error()
		s.mu.Unlock()
		return err
	}
	// Auto-start: wake the supervision loop so it picks up the fresh install.
	s.cond.Broadcast()
	s.mu.Unlock()
	return nil
}

// pause stops the child and keeps the loop from respawning it, so an install
// can replace the directory the child was executing from.
//
// It must not sample s.cmd once and act on it: the loop can be past the pause
// gate but not yet publishing the child it is spawning, so a nil read there
// would let Commit() swap the directory out from under a live child. Poll
// until the loop is genuinely parked with no command.
func (s *Supervisor) pause() {
	s.mu.Lock()
	s.paused = true
	s.mu.Unlock()

	var signalled *exec.Cmd
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		c := s.cmd
		parked := s.parked
		s.mu.Unlock()

		if parked && c == nil {
			return
		}
		// Signal each distinct child once. A child spawned in the race window
		// is caught on a later pass.
		if c != nil && c != signalled {
			signalGroup(c, syscall.SIGTERM)
			signalled = c
		}
		time.Sleep(50 * time.Millisecond)
	}

	s.mu.Lock()
	c := s.cmd
	s.mu.Unlock()
	if c != nil {
		s.logger.Warn("wiki-viewer-lite did not stop for the install swap, sending SIGKILL")
		signalGroup(c, syscall.SIGKILL)
	}
}

// resume lets the loop spawn a child again.
func (s *Supervisor) resume() {
	s.mu.Lock()
	s.paused = false
	s.cond.Broadcast()
	s.mu.Unlock()
}

// Start launches the supervision loop in a background goroutine.
func (s *Supervisor) Start(ctx context.Context) {
	// A crash between the two renames of a previous swap leaves no live
	// directory. Restore it before anything reads Installed().
	if err := RecoverInstall(); err != nil {
		s.logger.WithError(err).Warn("could not recover previous wiki-viewer-lite install")
	}
	s.loopCtx, s.loopCancel = context.WithCancel(ctx)
	go s.loop()
}

// Stop terminates the child process group and waits for the supervision loop
// to exit.
//
// Signalling is not optional. Pdeathsig is set on Linux, but it fires when the
// creating THREAD dies, and the Go runtime moves goroutines between threads
// freely, so it cannot be relied on to reap the child when termyard exits.
// Darwin has no Pdeathsig. Without an explicit kill here a Node process
// survives shutdown holding its port.
func (s *Supervisor) Stop() {
	s.stopping.Store(true)
	if s.loopCancel != nil {
		s.loopCancel()
	}

	s.mu.Lock()
	c := s.cmd
	// Wake the loop if it is parked in cond.Wait() with nothing installed.
	s.cond.Broadcast()
	s.mu.Unlock()

	if c == nil {
		// No child was ever spawned. Still wait for the loop to notice the
		// cancellation, but do not block shutdown on it.
		s.waitForLoop(5 * time.Second)
		return
	}

	signalGroup(c, syscall.SIGTERM)
	if s.waitForLoop(5 * time.Second) {
		return
	}
	s.logger.Warn("wiki-viewer-lite did not exit on SIGTERM, sending SIGKILL")
	signalGroup(c, syscall.SIGKILL)
	s.waitForLoop(5 * time.Second)
}

// waitForLoop reports whether the supervision loop exited within d.
func (s *Supervisor) waitForLoop(d time.Duration) bool {
	if !s.started.Load() {
		return true
	}
	select {
	case <-s.loopDone:
		return true
	case <-time.After(d):
		return false
	}
}

// loop is the supervision loop that keeps the child process alive.
func (s *Supervisor) loop() {
	s.started.Store(true)
	defer close(s.loopDone)

	var backoff time.Duration
	uptimeStart := time.Time{}

	for {
		select {
		case <-s.loopCtx.Done():
			s.logger.Debug("supervision loop exiting")
			return
		default:
		}

		// Park while an install swaps the directory underneath us.
		s.mu.Lock()
		for s.paused && s.loopCtx.Err() == nil {
			s.parked = true
			s.cond.Wait()
		}
		s.parked = false
		s.mu.Unlock()
		if s.loopCtx.Err() != nil {
			s.logger.Debug("supervision loop exiting after pause")
			return
		}

		if !Installed() {
			s.setError("not installed")
			// Wait for an install to complete.
			s.mu.Lock()
			s.parked = true
			s.cond.Wait()
			s.parked = false
			s.mu.Unlock()
			continue
		}

		// Spawn the child.
		bp, err := BinPath()
		if err != nil {
			s.setError(err.Error())
			backoff = nextBackoff(backoff)
			s.sleepOrCancel(backoff)
			continue
		}

		cmd := exec.Command("node", bp)
		cmd.Env = append(cmd.Environ(), "WIKI_LITE_PREFIX=/wiki")
		cmd.SysProcAttr = childSysProcAttr()

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			s.setError(err.Error())
			backoff = nextBackoff(backoff)
			s.sleepOrCancel(backoff)
			continue
		}

		s.logger.Debug("starting wiki-viewer-lite")
		if err := cmd.Start(); err != nil {
			s.setError(err.Error())
			backoff = nextBackoff(backoff)
			s.sleepOrCancel(backoff)
			continue
		}

		// Publish the live child so Stop() can signal its group.
		s.mu.Lock()
		s.cmd = cmd
		s.mu.Unlock()

		// Scan for the port sentinel with a 30s deadline.
		portCh := make(chan int, 1)
		errCh := make(chan error, 1)

		go func() {
			scanner := bufio.NewScanner(stdout)
			for scanner.Scan() {
				line := scanner.Text()
				s.logger.Debug(line)
				if port, ok := parsePortLine(line); ok {
					select {
					case portCh <- port:
					default:
					}
					// Keep draining for the process lifetime.
					continue
				}
			}
			// Scanner exited (stdout closed). Drain the pipe fully so
			// Wait() can return; the scanner already consumed everything.
			if err := scanner.Err(); err != nil {
				errCh <- err
			} else {
				errCh <- nil
			}
		}()

		select {
		case port := <-portCh:
			s.mu.Lock()
			s.port = port
			s.procErr = ""
			s.mu.Unlock()
			s.logger.WithField("port", port).Info("wiki-viewer-lite listening")
		case err := <-errCh:
			// Process exited before we got a port.
			s.setError(fmt.Sprintf("process exited early: %v", err))
			_ = cmd.Wait()
			s.clearCmd()
			backoff = nextBackoff(backoff)
			s.sleepOrCancel(backoff)
			continue
		case <-time.After(30 * time.Second):
			s.setError("timed out waiting for start sentinel")
			s.terminate(cmd)
			s.clearCmd()
			backoff = nextBackoff(backoff)
			s.sleepOrCancel(backoff)
			continue
		case <-s.loopCtx.Done():
			s.terminate(cmd)
			s.clearCmd()
			s.logger.Debug("supervision loop exiting during start")
			return
		}

		// Child is running. Monitor it.
		uptimeStart = time.Now()

		// Wait for the process to exit. The drain goroutine is still
		// consuming stdout; once it finishes, cmd.Wait() will return.
		waitErr := cmd.Wait()

		// Clear port and record error.
		s.mu.Lock()
		s.port = 0
		s.cmd = nil
		if waitErr != nil {
			s.procErr = waitErr.Error()
		}
		if s.loopCtx.Err() != nil {
			// Stopping intentionally; do not restart.
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()

		// If the process had decent uptime, reset backoff.
		if time.Since(uptimeStart) > 60*time.Second {
			backoff = 0
		}

		// A stop we asked for so an install could swap the directory is not a
		// crash. Skip the restart backoff, or pause() sits through a delay
		// that protects nothing.
		s.mu.Lock()
		paused := s.paused
		s.mu.Unlock()
		if paused {
			continue
		}

		backoff = nextBackoff(backoff)
		s.logger.WithFields(logrus.Fields{
			"error":   waitErr,
			"backoff": backoff,
		}).Warn("wiki-viewer-lite exited, restarting")
		s.sleepOrCancel(backoff)
	}
}

func (s *Supervisor) setError(err string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.procErr = err
}

// clearCmd drops the tracked child once it has been reaped.
func (s *Supervisor) clearCmd() {
	s.mu.Lock()
	s.cmd = nil
	s.mu.Unlock()
}

// signalGroup sends sig to the child's whole process group. It never calls
// Wait: the supervision loop owns the single Wait for any given command, and a
// second Wait would race it.
func signalGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, sig)
}

// terminate stops a child that the loop has not yet begun waiting on, and
// reaps it. Only start-phase paths may call this, because it owns the Wait.
func (s *Supervisor) terminate(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	signalGroup(cmd, syscall.SIGTERM)

	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		signalGroup(cmd, syscall.SIGKILL)
		<-done
	}
}

func (s *Supervisor) sleepOrCancel(d time.Duration) {
	if d <= 0 {
		return
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-s.loopCtx.Done():
	}
}

// parsePortLine accepts "WIKI_LITE_PORT=<port>" lines and returns the port
// number. Any other format, junk, or out-of-range values are rejected.
func parsePortLine(line string) (int, bool) {
	const prefix = "WIKI_LITE_PORT="
	if !strings.HasPrefix(line, prefix) {
		return 0, false
	}
	val := strings.TrimSpace(line[len(prefix):])
	port, err := strconv.Atoi(val)
	if err != nil || port < 1 || port > 65535 {
		return 0, false
	}
	return port, true
}

// nextBackoff produces the sequence 1s, 2s, 4s, 8s, 16s capped at 30s.
func nextBackoff(prev time.Duration) time.Duration {
	if prev <= 0 {
		return time.Second
	}
	next := prev * 2
	if next > 30*time.Second {
		return 30 * time.Second
	}
	return next
}

// SetTestPort sets the supervisor's internal port without starting a child
// process. Exported for tests only; do not call in production.
func (s *Supervisor) SetTestPort(p int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.port = p
	s.procErr = ""
}

func homeDir() (string, error) {
	if h := os.Getenv("HOME"); h != "" {
		return h, nil
	}
	return os.UserHomeDir()
}
