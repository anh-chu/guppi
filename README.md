<p align="center">
  <img src="web/public/icon-512.png" alt="guppi logo" width="128" />
</p>

<h1 align="center">guppi</h1>

<p align="center">
  <strong>The guppi of your terminal workflow.</strong>
</p>

<p align="center">
  A web dashboard for monitoring and interacting with tmux sessions and AI coding agents — all from your browser.
</p>

---

## What is guppi?

guppi gives you a real-time web interface for your tmux sessions. It renders full terminal output in the browser using xterm.js backed by native PTY connections, so you get the exact same view as your local terminal — borders, splits, colors, and all.

It also tracks AI coding agents (Claude Code, Codex, Copilot, OpenCode) running inside your sessions, surfacing their status so you know when an agent needs input, hits an error, or finishes a task.

### Key features

- **Full terminal in the browser** — PTY-backed xterm.js rendering, not a screen scrape. Type, scroll, resize — it just works.
- **Real-time session discovery** — sessions, windows, and panes update live via tmux control mode.
- **AI agent monitoring** — see which agents are active, waiting for input, or errored across all sessions at a glance.
- **Push notifications** — get browser/desktop notifications when an agent needs attention, even with the tab backgrounded.
- **Quick switcher** — Ctrl+K to jump between sessions and windows instantly.
- **Single binary** — Go backend with the React frontend embedded. No separate processes, no Node runtime needed in production.
- **Unix socket + HTTP** — local CLI notifications go through a Unix socket for zero-config, with HTTP as fallback.

## Quick start

### Prerequisites

- [Go](https://go.dev/) 1.25+
- [Node.js](https://nodejs.org/) 18+ (for building the frontend)
- [tmux](https://github.com/tmux/tmux) running with at least one session

### Build & run

```bash
# Build everything (frontend + Go binary)
make build

# Run the server
./dist/guppi server

# Open in your browser
open http://localhost:7654
```

### Development

```bash
# Frontend dev server (hot reload)
cd web && npm install && npm run dev

# Go server (watches for tmux changes)
go run . server
```

## Agent hooks

guppi can track AI agent activity inside your tmux sessions. The `agent-setup` command auto-detects installed agents and configures their hooks:

```bash
guppi agent-setup
```

This configures hooks for any detected agents:
- **Claude Code** — hooks in `~/.claude/settings.json`
- **Codex** — `notify` command in `~/.codex/config.toml`
- **GitHub Copilot CLI** — hooks in `~/.copilot/hooks/guppi.json`
- **OpenCode** — hook script in `~/.config/opencode/`

See [docs/agent-setup.md](docs/agent-setup.md) for detailed configuration and manual setup instructions.

You can also send notifications manually:

```bash
guppi notify -t claude -s waiting -m "Needs approval"
guppi notify -t codex -s active
guppi notify -t claude -s completed
```

The tmux session, window, and pane are auto-detected when run inside tmux.

## Architecture

```
Browser  <──WebSocket──>  Go Server  <──PTY──>  tmux attach-session
                              │
                              ├── Control mode (real-time state changes)
                              ├── Session discovery (polling fallback)
                              ├── Tool event tracker (agent status)
                              └── Unix socket (local CLI notifications)
```

Each browser tab gets its own PTY process running `tmux attach-session`. tmux handles all rendering natively — guppi just bridges the PTY output to xterm.js over a WebSocket. Window switching uses the tmux `select-window` command; tmux re-renders through the existing PTY connection.

State changes (new sessions, window renames, pane activity) are detected via tmux control mode and broadcast to all connected clients over a separate WebSocket.

## UI concepts

### Session status

Sessions in the sidebar and overview show as **active** or **idle**:

- **Active** — at least one pane in the session has a foreground process that isn't a shell. For example: `vim`, `claude`, `node`, `python`, `go build`, etc.
- **Idle** — every pane is sitting at a shell prompt (`bash`, `zsh`, `fish`, `sh`, `dash`, `ksh`, `csh`, `tcsh`, `tmux`, `login`).

This is driven by tmux's `pane_current_command`, which reports the foreground process of each pane. The server receives this via tmux control mode (or polling) and broadcasts it over WebSocket.

### Alerts

Alerts surface when an AI agent needs attention. They appear in the **alert banner** at the top of every page and in the **Pending Alerts** section on the overview.

- **Waiting** — the agent is waiting for user input (e.g., tool approval in Claude Code).
- **Error** — the agent hit an error.
- **Active** — the agent is running normally (shown as badges in the sidebar, not as alerts).

Alerts are live state from the server — they always reflect the current status and survive page refreshes. Dismissing an alert hides it from the UI but doesn't affect the agent.

Push alerts (via the Web Push API) work independently of the browser tab, including when logged out or when the tab is closed.

## Configuration

### Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `GUPPI_PORT` | `7654` | HTTP server port |
| `GUPPI_SOCKET` | auto | Unix socket path for local CLI |
| `GUPPI_DISCOVERY_INTERVAL` | `2` | Session polling interval (seconds) |
| `GUPPI_NO_CONTROL_MODE` | `false` | Disable tmux control mode |
| `GUPPI_URL` | `http://localhost:7654` | Server URL for notify/agent-setup |

### CLI flags

```
guppi server [flags]
  -p, --port int              HTTP server port (default 7654)
      --discovery-interval    Session discovery interval in seconds (default 2)
      --no-control-mode       Disable tmux control mode (use polling only)
      --socket string         Unix socket path (auto-detected if omitted)
```

## FAQ

### Copy/paste doesn't work with TUI apps (opencode, vim, etc.)

TUI applications capture mouse and keyboard input, which prevents normal text selection in the browser. There are two things to configure:

**1. Use Shift+click/drag to select text**

Holding Shift while clicking and dragging bypasses terminal mouse mode, letting you select text directly in xterm.js. Then use Cmd/Ctrl+C to copy.

**2. Enable tmux clipboard passthrough**

For clipboard integration to work through tmux (so that tmux copy mode and OSC 52-aware apps can write to your browser clipboard), add these to your `~/.tmux.conf`:

```tmux
set -g set-clipboard on
set -g allow-passthrough on
```

Then reload with `tmux source-file ~/.tmux.conf`.

**What these do:**

- `set-clipboard on` — lets tmux process OSC 52 clipboard escape sequences and store them in both the tmux paste buffer and the outer terminal. The default (`external`) only forwards to the outer terminal and skips the paste buffer.
- `allow-passthrough on` — lets applications inside tmux send escape sequences (including OSC 52 clipboard writes) through to the outer terminal, which in guppi's case is xterm.js in your browser.

**Security note:** `allow-passthrough` permits any application running inside tmux to send arbitrary escape sequences to the outer terminal. This is standard for modern terminal workflows (neovim, image protocols, clipboard sync all use it), but means you should trust the code running in your sessions. If you run untrusted code, leave passthrough off and use Shift+select as your copy method instead.

## Tech stack

- **Backend:** Go, chi v5, gorilla/websocket, creack/pty
- **Frontend:** React 19, TypeScript, Vite, Tailwind CSS v4, xterm.js
- **Build:** Single binary with `//go:embed`, GoReleaser for releases

## License

MIT
