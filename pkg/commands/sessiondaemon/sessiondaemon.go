// Package sessiondaemon implements the standalone `termyard session
// create/list/kill/capture` CLI and the hidden `session-daemon` process it
// spawns. This is a low-level debug tool that talks directly to the PTY
// daemon registry (pkg/pty); it does NOT go through the canonical
// state.Catalog/SessionCommandService. Sessions created this way have no
// stable owner/session identity and will NOT appear in the browser UI,
// will NOT sync across peers, and are not part of the supported
// session-creation path (use the browser UI or /api/state/session-commands
// for that). This is intentional, not a bug.
package sessiondaemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v3"

	"github.com/anh-chu/termyard/pkg/common"
	"github.com/anh-chu/termyard/pkg/pty"
)

// defaultSessionDir returns the per-user socket directory for session daemons.
// Honors TERMYARD_SESSION_DIR to stay in parity with the server's own
// defaultSessionDir (see pkg/commands/server/runtime.go).
func defaultSessionDir() string {
	if dir := os.Getenv("TERMYARD_SESSION_DIR"); dir != "" {
		return dir
	}
	uid := fmt.Sprintf("%d", os.Getuid())
	return filepath.Join("/tmp", "termyard-sessions-"+uid)
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
		Owner:       c.String("owner"),
		SessionID:   c.String("session-id"),
		Generation:  c.String("generation"),
		CommandID:   c.String("command-id"),
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

	reg := pty.NewRegistry(socketDir)
	if err := reg.Create(name, shell, cwd, uint16(cols), uint16(rows)); err != nil {
		return err
	}

	fmt.Printf("Session %q created.\n", name)
	return nil
}

// executeSessionList implements "termyard session list".
func executeSessionList(ctx context.Context, c *cli.Command) error {
	socketDir := c.String("socket-dir")
	if socketDir == "" {
		socketDir = defaultSessionDir()
	}

	reg := pty.NewRegistry(socketDir)
	sessions := reg.List()

	if c.Bool("json") {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(sessions)
	}

	if len(sessions) == 0 {
		fmt.Println("No active sessions.")
		return nil
	}

	fmt.Printf("%-32s %-8s %-20s %s\n", "ID", "PID", "CREATED", "SHELL")
	fmt.Println("----------------------------------------------------------------------")
	for _, s := range sessions {
		fmt.Printf("%-32s %-8d %-20s %s\n", s.ID, s.Pid, s.Created, s.Shell)
	}
	return nil
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
			&cli.IntFlag{
				Name:  "buffer-size",
				Usage: "ring buffer size in bytes (default 1MB)",
				Value: 1 << 20,
			},
			&cli.StringFlag{
				Name:  "daemon-key",
				Usage: "stable daemon binding key",
			},
			&cli.StringFlag{
				Name:  "owner",
				Usage: "v2 owner identity for this daemon's stable binding",
			},
			&cli.StringFlag{
				Name:  "session-id",
				Usage: "v2 session identifier for this daemon's stable binding",
			},
			&cli.StringFlag{
				Name:  "generation",
				Usage: "v2 generation for this daemon's stable binding",
			},
			&cli.StringFlag{
				Name:  "command-id",
				Usage: "optional command id associated with the spawn request",
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
