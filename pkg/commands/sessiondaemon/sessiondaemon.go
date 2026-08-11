package sessiondaemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v3"

	"github.com/anh-chu/termyard/pkg/common"
	"github.com/anh-chu/termyard/pkg/pty"
	"github.com/anh-chu/termyard/pkg/socket"
)

// defaultSessionDir returns the per-user socket directory for session daemons.
// Matches the server's resolution to ensure CLI and server use the same directory.
func defaultSessionDir() string {
	if dir := os.Getenv("TERMYARD_SESSION_DIR"); dir != "" {
		return dir
	}
	uid := fmt.Sprintf("%d", os.Getuid())
	return fmt.Sprintf("/tmp/termyard-sessions-%s", uid)
}

// executeSessionDaemon is the action for the hidden "session-daemon" command.
func executeSessionDaemon(ctx context.Context, c *cli.Command) error {
	cfg := pty.DaemonConfig{
		ID:          c.String("id"),
		Shell:       c.String("shell"),
		Cwd:         c.String("cwd"),
		SocketDir:   c.String("socket-dir"),
		StateDir:    c.String("state-dir"),
		SystemdUnit: c.String("systemd-unit"),
		BufferSize:  int(c.Int("buffer-size")),
		Nonce:       c.String("nonce"),
	}

	// Parse terminal size.
	cols, _ := strconv.ParseUint(c.String("cols"), 10, 16)
	rows, _ := strconv.ParseUint(c.String("rows"), 10, 16)
	if cols > 0 {
		cfg.Cols = uint16(cols)
	}
	if rows > 0 {
		cfg.Rows = uint16(rows)
	}

	return pty.RunDaemon(cfg)
}

// executeSessionCreate implements "termyard session create".
// First tries the local server API (unix socket, then HTTP).
// Falls back to direct spawn only when server is confirmed absent AND lock is free.
// If lock is held (server booting), retries API path.
func executeSessionCreate(ctx context.Context, c *cli.Command) error {
	name := c.String("name")
	if name == "" {
		return fmt.Errorf("--name is required")
	}

	shell := c.String("shell")
	cwd := c.String("cwd")

	cols, _ := strconv.ParseUint(c.String("cols"), 10, 16)
	rows, _ := strconv.ParseUint(c.String("rows"), 10, 16)

	// Derive socket dir if not set.
	socketDir := c.String("socket-dir")
	if socketDir == "" {
		socketDir = defaultSessionDir()
	}

	// Try server API first (unix socket, then HTTP)
	ctx = context.WithValue(ctx, "sessionName", name)
	ctx = context.WithValue(ctx, "shell", shell)
	ctx = context.WithValue(ctx, "cwd", cwd)
	ctx = context.WithValue(ctx, "cols", uint16(cols))
	ctx = context.WithValue(ctx, "rows", uint16(rows))

	if err := attemptServerCreate(ctx); err == nil {
		fmt.Printf("Session %q created via server API.\n", name)
		return nil
	} else if !isServerConfirmedAbsent(err) {
		// Server reachable but returned error; surface to user, never spawn
		return fmt.Errorf("server error: %w", err)
	}

	// Server confirmed absent (socket missing + http connection refused).
	// Atomically try to acquire adoption lock: if held (server booting), retry API with backoff.
	// Only spawn while HOLDING the lock.
	logrus.Debug("server confirmed absent; attempting atomic adoption lock")
	reg := pty.NewRegistry(socketDir)

	for retries := 0; retries < 5; retries++ {
		// Atomically try to acquire lock without blocking
		release, acquired := reg.TryAcquireAdoptionLock()

		if acquired {
			// Lock acquired; we have exclusive right to spawn
			defer release()

			_, err := reg.Create(name, shell, cwd, uint16(cols), uint16(rows))
			if err != nil {
				return fmt.Errorf("failed to create session: %w", err)
			}
			fmt.Printf("Session %q created directly (server will adopt after next start).\n", name)
			return nil
		}

		// Lock held; server is booting. Wait and retry API path.
		if retries < 4 {
			time.Sleep(time.Duration((retries+1)*250) * time.Millisecond)
			if err := attemptServerCreate(ctx); err == nil {
				fmt.Printf("Session %q created via server API (after boot).\n", name)
				return nil
			}
		}
	}

	return fmt.Errorf("failed to acquire adoption lock (server may be stuck booting)")
}

// attemptServerCreate tries to create a session via the server API.
// Returns nil on success, or an error marking whether the server is confirmed absent.
func attemptServerCreate(ctx context.Context) error {
	val := ctx.Value("sessionName")
	if val == nil {
		return &serverAbsentError{inner: fmt.Errorf("session context missing")}
	}

	name := val.(string)
	shell := ctx.Value("shell").(string)
	cwd := ctx.Value("cwd").(string)
	cols := ctx.Value("cols").(uint16)
	rows := ctx.Value("rows").(uint16)

	req := map[string]interface{}{
		"name":    name,
		"path":    cwd,
		"command": shell,
		"cols":    int(cols),
		"rows":    int(rows),
	}
	body, _ := json.Marshal(req)

	// Try Unix socket first
	socketPath := socket.DefaultPath()
	if resp, err := postViaUnixSocket(socketPath, "/api/session/new", body); err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
			return nil
		}
		if resp.StatusCode >= 400 {
			return &serverError{statusCode: resp.StatusCode, msg: "server error"}
		}
	}

	// Fall back to HTTP using TERMYARD_URL or default
	serverURL := os.Getenv("TERMYARD_URL")
	if serverURL == "" {
		serverURL = "http://localhost:7654"
	}

	req2, _ := http.NewRequestWithContext(ctx, "POST", serverURL+"/api/session/new", bytes.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req2)
	if err != nil {
		// Only ECONNREFUSED = server absent; timeouts/DNS/other = error
		if isConnectionRefused(err) {
			return &serverAbsentError{inner: err}
		}
		// Other error (timeout, DNS, etc) = print and exit
		return &serverError{statusCode: 0, msg: fmt.Sprintf("connection error: %v", err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
		return nil
	}
	if resp.StatusCode >= 400 {
		return &serverError{statusCode: resp.StatusCode, msg: "server error"}
	}

	return &serverAbsentError{inner: fmt.Errorf("unexpected response code: %d", resp.StatusCode)}
}

// serverError marks a reachable server returning an error.
type serverError struct {
	statusCode int
	msg        string
}

func (e *serverError) Error() string {
	return fmt.Sprintf("%s: %d", e.msg, e.statusCode)
}

// serverAbsentError marks a confirmed server absence (socket missing, connection refused).
type serverAbsentError struct {
	inner error
}

func (e *serverAbsentError) Error() string {
	return e.inner.Error()
}

// isServerConfirmedAbsent checks the error type to distinguish absence from reachable server error.
func isServerConfirmedAbsent(err error) bool {
	_, ok := err.(*serverAbsentError)
	return ok
}

// isConnectionRefused checks if an error is ECONNREFUSED (connection refused).
func isConnectionRefused(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, syscall.ECONNREFUSED)
}

// postViaUnixSocket sends a POST request via Unix socket and returns the response.
// Returns error if the socket is missing or unreachable.
func postViaUnixSocket(socketPath, endpoint string, body []byte) (*http.Response, error) {
	// Check socket exists first
	if _, err := os.Stat(socketPath); err != nil {
		return nil, &serverAbsentError{inner: fmt.Errorf("socket not found: %w", err)}
	}

	client := &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				var dialer net.Dialer
				return dialer.DialContext(ctx, "unix", socketPath)
			},
		},
	}
	req, _ := http.NewRequest("POST", "http://localhost"+endpoint, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		// Only ECONNREFUSED = server absent; other dial/timeout errors are real errors
		if isConnectionRefused(err) {
			return nil, &serverAbsentError{inner: err}
		}
		// Timeout or other network error = server present but unreachable
		return nil, &serverError{statusCode: 0, msg: fmt.Sprintf("socket connection error: %v", err)}
	}
	return resp, nil
}

func executeSessionList(ctx context.Context, c *cli.Command) error {
	socketDir := c.String("socket-dir")
	if socketDir == "" {
		socketDir = defaultSessionDir()
	}

	// Try server API via socket first
	socketPath := socket.DefaultPath()
	if resp, err := getViaUnixSocket(socketPath, "/api/sessions"); err == nil {
		var sessions []*pty.SessionInfo
		if json.NewDecoder(resp.Body).Decode(&sessions) == nil {
			resp.Body.Close()
			returnSessionList(c, sessions)
			return nil
		}
		resp.Body.Close()
	}

	// Fallback to HTTP
	serverURL := os.Getenv("TERMYARD_URL")
	if serverURL == "" {
		serverURL = "http://localhost:7654"
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(serverURL + "/api/sessions")
	if err == nil && resp.StatusCode == 200 {
		var sessions []*pty.SessionInfo
		if json.NewDecoder(resp.Body).Decode(&sessions) == nil {
			resp.Body.Close()
			returnSessionList(c, sessions)
			return nil
		}
		resp.Body.Close()
	}
	if resp != nil {
		resp.Body.Close()
	}

	// Server not running: use fallback (read-only scan)
	logrus.Debug("server not running, using fallback socket dir scan")
	reg := pty.NewRegistry(socketDir)
	sessions := make([]*pty.SessionInfo, 0)
	for _, s := range reg.Scan() {
		s := s
		sessions = append(sessions, &s)
	}
	returnSessionList(c, sessions)
	return nil
}

// getViaUnixSocket sends a GET request via Unix socket.
func getViaUnixSocket(socketPath, endpoint string) (*http.Response, error) {
	// Check socket exists first
	if _, err := os.Stat(socketPath); err != nil {
		return nil, err
	}

	client := &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				var dialer net.Dialer
				return dialer.DialContext(ctx, "unix", socketPath)
			},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://localhost"+endpoint, nil)
	return client.Do(req)
}

// returnSessionList displays sessions in JSON or text format.
func returnSessionList(c *cli.Command, sessions []*pty.SessionInfo) {
	if c.Bool("json") {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(sessions)
		return
	}

	if len(sessions) == 0 {
		fmt.Println("No active sessions.")
		return
	}

	fmt.Printf("%-32s %-8s %-20s %s\n", "ID", "PID", "CREATED", "SHELL")
	fmt.Println("----------------------------------------------------------------------")
	for _, s := range sessions {
		if s != nil {
			fmt.Printf("%-32s %-8d %-20s %s\n", s.ID, s.Pid, s.Created, s.Shell)
		}
	}
}

// executeSessionKill implements "termyard session kill".
func executeSessionKill(ctx context.Context, c *cli.Command) error {
	if c.NArg() < 1 {
		return fmt.Errorf("session name is required")
	}
	name := c.Args().First()
	if name == "" {
		return fmt.Errorf("session name is required")
	}

	socketDir := c.String("socket-dir")
	if socketDir == "" {
		socketDir = defaultSessionDir()
	}

	reg := pty.NewRegistry(socketDir)
	if err := reg.Kill(name); err != nil {
		return err
	}

	fmt.Printf("Session %q killed.\n", name)
	return nil
}

func executeSessionCapture(ctx context.Context, c *cli.Command) error {
	if c.NArg() < 1 {
		return fmt.Errorf("session name is required")
	}
	name := c.Args().First()

	socketDir := c.String("socket-dir")
	if socketDir == "" {
		socketDir = defaultSessionDir()
	}

	reg := pty.NewRegistry(socketDir)
	text, err := reg.Capture(name)
	if err != nil {
		return err
	}

	lines := int(c.Int("lines"))
	if lines > 0 {
		parts := splitLastLines(text, lines)
		text = parts
	}

	fmt.Print(text)
	return nil
}

// splitLastLines returns the last n lines of text.
func splitLastLines(text string, n int) string {
	var lines []string
	start := 0
	for i, ch := range text {
		if ch == '\n' {
			lines = append(lines, text[start:i+1])
			start = i + 1
		}
	}
	if start < len(text) {
		lines = append(lines, text[start:])
	}
	if len(lines) <= n {
		return text
	}
	result := ""
	for _, l := range lines[len(lines)-n:] {
		result += l
	}
	return result
}

func init() {
	logrus.Debug("registering sessiondaemon commands")

	// Hidden internal command used by the Registry to spawn daemon processes.
	sessionDaemonCmd := &cli.Command{
		Name:   "session-daemon",
		Usage:  "internal: spawn a session daemon process",
		Hidden: true,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "id",
				Usage:    "unique session identifier",
				Required: true,
			},
			&cli.StringFlag{
				Name:  "shell",
				Usage: "shell to spawn",
			},
			&cli.StringFlag{
				Name:  "cwd",
				Usage: "working directory",
			},
			&cli.StringFlag{
				Name:  "cols",
				Usage: "initial terminal columns",
				Value: "120",
			},
			&cli.StringFlag{
				Name:  "rows",
				Usage: "initial terminal rows",
				Value: "40",
			},
			&cli.StringFlag{
				Name:     "socket-dir",
				Usage:    "socket directory",
				Required: true,
			},
			&cli.StringFlag{
				Name:  "state-dir",
				Usage: "lifecycle state directory",
			},
			&cli.StringFlag{
				Name:  "systemd-unit",
				Usage: "systemd scope unit name (for cleanup on exit)",
			},
			&cli.StringFlag{
				Name:  "nonce",
				Usage: "8-byte hex nonce for instance identity (generated by server)",
			},
			&cli.IntFlag{
				Name:  "buffer-size",
				Usage: "ring buffer size in bytes (default 1MB)",
				Value: 1 << 20,
			},
		},
		Action: executeSessionDaemon,
	}
	common.RegisterCommand(sessionDaemonCmd)

	// User-facing "session" command with subcommands.
	sessionCmd := &cli.Command{
		Name:  "session",
		Usage: "manage termyard-yarded session daemons",
		Commands: []*cli.Command{
			{
				Name:  "create",
				Usage: "create a new session daemon",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:     "name",
						Aliases:  []string{"n"},
						Usage:    "session name",
						Required: true,
					},
					&cli.StringFlag{
						Name:  "shell",
						Usage: "shell to spawn (default: $SHELL or /bin/bash)",
					},
					&cli.StringFlag{
						Name:  "cwd",
						Usage: "working directory (default: current)",
					},
					&cli.StringFlag{
						Name:  "cols",
						Usage: "terminal columns (default: 120)",
						Value: "120",
					},
					&cli.StringFlag{
						Name:  "rows",
						Usage: "terminal rows (default: 40)",
						Value: "40",
					},
					&cli.StringFlag{
						Name:  "socket-dir",
						Usage: "socket directory (default: /tmp/termyard-sessions-{uid})",
					},
				},
				Action: executeSessionCreate,
			},
			{
				Name:  "list",
				Usage: "list active session daemons",
				Flags: []cli.Flag{
					&cli.BoolFlag{
						Name:  "json",
						Usage: "output as JSON",
					},
					&cli.StringFlag{
						Name:  "socket-dir",
						Usage: "socket directory",
					},
				},
				Action: executeSessionList,
			},
			{
				Name:      "kill",
				Usage:     "kill a session daemon",
				ArgsUsage: "NAME",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:  "socket-dir",
						Usage: "socket directory",
					},
				},
				Action: executeSessionKill,
			},
			{
				Name:      "capture",
				Usage:     "capture a session daemon's terminal content",
				ArgsUsage: "NAME",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:  "socket-dir",
						Usage: "socket directory",
					},
					&cli.IntFlag{
						Name:  "lines",
						Usage: "number of lines to return (0 = all)",
						Value: 40,
					},
				},
				Action: executeSessionCapture,
			},
		},
	}

	common.RegisterCommand(sessionCmd)
}
