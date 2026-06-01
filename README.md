# GUPPI

all your tmux sessions, all your agents, one interface

get notified when it matters

---

## What is GUPPI?

guppi gives you a real-time web interface for your tmux sessions. It renders full terminal output in the browser using xterm.js backed by PTY connections, so you get the exact same view as your local terminal — borders, splits, colors, and all.

It also tracks AI coding agents (Claude Code, Codex, Copilot, OpenCode) running inside your sessions, surfacing their status so you know when an agent needs input, hits an error, or finishes a task.

### Key features

- **Full terminal in the browser** — PTY-backed xterm.js rendering. Type, scroll, resize — it just works.
- **Real-time session discovery** — sessions, windows, and panes update live via tmux control mode.
- **AI agent monitoring** — see which agents are active, waiting for input, or errored across all sessions at a glance.
- **Push notifications** — get browser/desktop notifications when an agent needs attention, even with the tab backgrounded.
- **Quick switcher** — Ctrl+K to jump between sessions and windows instantly, hands never leave the keyboard.
- **Single binary** — Go backend with the React frontend embedded. No separate processes, no Node runtime needed in production.
- **Unix socket + HTTP** — local CLI notifications go through a Unix socket for zero-config, with HTTP as fallback.
- **Multi-machine peering** — connect any number of guppi nodes through the dashboard; sessions on every machine show up in one view.

### Non-goals

- **Multi-user** — guppi is a single-user tool. One person, one dashboard. There are no user accounts, roles, or shared access controls.
- **Agent orchestration** — guppi doesn't start, stop, or control your agents. It watches and reports. You run your agents however you want; guppi just tells you what they're doing.
- **tmux management** — guppi doesn't configure or manage your tmux setup. Your `.tmux.conf`, layouts, and workflows stay yours.

## Installation

### Using dist (recommended)

[dist](https://github.com/ekristen/distillery) installs binaries from GitHub releases with checksum verification and multi-version support.

```bash
dist install ekristen/guppi
```

### Quick install

```bash
curl -sSL https://get.guppi.sh | sh
```

This detects your platform, downloads the latest release, and puts `guppi` in `/usr/local/bin` (or `~/.local/bin` if not writable). You can pin a version with `VERSION=v0.1.0 curl -sSL ... | sh`.

### From source

Requires [Go](https://go.dev/) 1.25+ and [Node.js](https://nodejs.org/) 18+.

```bash
git clone https://github.com/ekristen/guppi.git
cd guppi
make build
# Binary is at ./dist/guppi
```

## User guide

### 1. Start the server

Make sure [tmux](https://github.com/tmux/tmux) is running with at least one session, then:

```bash
guppi server
```

Open `http://localhost:7654` in your browser. On first launch you'll set a password, then guppi will guide you through agent setup.

guppi serves plain HTTP. For remote access put it behind a reverse proxy
(Caddy, nginx) or use an overlay network like [Tailscale](https://tailscale.com/)
/ WireGuard — those layers also give you encryption.

### 2. Configure agent hooks

guppi tracks AI agents running in your tmux sessions, but agents need hooks configured so they can report their status. Run:

```bash
guppi agent-setup
```

This auto-detects which agents you have installed and configures their hooks:

- **Claude Code** — hooks in `~/.claude/settings.json`
- **Codex** — `notify` command in `~/.codex/config.toml`
- **GitHub Copilot CLI** — hooks in `~/.copilot/hooks/guppi.json`
- **OpenCode** — plugin in `~/.config/opencode/plugins/guppi.js`

Run this on each machine where you use agents.

You can check hook status any time in the web UI under **Settings → Agents**, or by visiting `/setup`.

See [docs/agent-setup.md](docs/agent-setup.md) for manual setup instructions.

### 3. Day-to-day

Once hooks are configured, agent status shows up automatically:

- The **Overview** page shows all sessions and any agents that need attention.
- The **sidebar** badges sessions with active/waiting/errored agents.
- **Quick switcher** (`Ctrl+K` / `Cmd+K`) jumps between sessions and windows.
- **Push notifications** alert you when an agent needs input, even with the tab closed (enable in Settings → Notifications).
- **Splits** — drag a session onto another to tile them side-by-side; drop on an edge to choose orientation. Press `Ctrl+Shift+\` to split the active pane into a new session.

### 4. Connect another machine (optional)

guppi nodes are fully symmetric — every machine runs the same `guppi server` and any node can connect to any other.

On each machine you want to mesh, run `guppi server`, open its dashboard, and set its password. Then on machine **A**'s dashboard:

1. Go to **Settings → Machines → Connect to another machine**.
2. Enter machine **B**'s address (e.g. `devvm-b.local:7654`) and **B**'s dashboard password.
3. Check **Auto-reconnect** (default on) and click **Connect**.

That's it. A and B now share sessions both ways. You only run Connect once per pair, from whichever side can reach the other — reverse direction is automatic.

If a link drops it auto-recovers within 30s with exponential backoff. Toggle **Auto-reconnect** off to stop dialing without forgetting the peer; click **Forget** to remove the peer entirely.

There's no transitivity: pairing A↔B and A↔C does not let B see C through A. Pair B↔C directly if you want that.

See [docs/multi-host.md](docs/multi-host.md) for reachability tips (NAT, Tailscale, reverse proxy).

### Keyboard shortcuts

Press `Ctrl+/` (or `Cmd+/` on macOS) to see all shortcuts, or click the `?` in the status bar.

| Shortcut | Action                                                   |
| -------- | -------------------------------------------------------- |
| `Ctrl+K` | Quick switcher — jump between sessions and windows       |
| `Ctrl+J` | Jump to next alert (cycles through waiting/error agents) |
| `Ctrl+H` | Overview                                                 |
| `Ctrl+,` | Settings                                                 |
| `Ctrl+\` | Toggle sidebar                                           |
| `Ctrl+L` | Lock / sign out                                          |
| `Ctrl+/` | Keyboard shortcuts help                                  |

### Manual notifications

You can also send status updates from scripts or the command line:

```bash
guppi notify -t claude -s waiting -m "Needs approval"
guppi notify -t codex -s active
guppi notify -t claude -s completed
```

The tmux session, window, and pane are auto-detected when run inside tmux.

### Lock & sign out

Click the lock icon in the status bar (or `Ctrl+L`) to sign out. Auto-lock
after idle is configurable under **Settings → Security**, including a
shorter timeout when the tab is backgrounded.

### Development

```bash
# Frontend dev server (hot reload)
cd web && npm install && npm run dev

# Go server (watches for tmux changes)
go run . server
```

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

| Variable                   | Default                 | Description                        |
| -------------------------- | ----------------------- | ---------------------------------- |
| `GUPPI_PORT`               | `7654`                  | HTTP server port                   |
| `GUPPI_SOCKET`             | auto                    | Unix socket path for local CLI     |
| `GUPPI_DISCOVERY_INTERVAL` | `2`                     | Session polling interval (seconds) |
| `GUPPI_NO_CONTROL_MODE`    | `false`                 | Disable tmux control mode          |
| `GUPPI_URL`                | `http://localhost:7654` | Server URL for notify/agent-setup  |
| `GUPPI_NO_AUTH`            | `false`                 | Disable authentication             |

### CLI flags

```
guppi server [flags]
  -p, --port int                  HTTP server port (default 7654)
      --discovery-interval int    Session discovery interval in seconds (default 2)
      --no-control-mode           Disable tmux control mode (use polling only)
      --socket string             Unix socket path (auto-detected if omitted)
      --no-auth                   Disable authentication (not recommended for remote access)
```

Multi-host peering is configured through the dashboard (**Settings →
Machines**). Peer records live in `~/.config/guppi/peers.json` and are
managed entirely by the UI — there are no `--hub` / `--tls-*` flags and no
`guppi pair` / `guppi peers` commands. See
[`docs/multi-host.md`](docs/multi-host.md) for details.

## FAQ

### How do I get HTTPS / remote access?

guppi serves plain HTTP and the peer link runs over plain WebSocket. For
encryption or cross-network reachability put one of these in front of
guppi:

- **Tailscale / WireGuard** — stable hostnames, end-to-end encryption, no
  proxy config. Recommended.
- **Reverse proxy** — Caddy or nginx terminating TLS in front of
  `localhost:7654`.

There is no built-in TLS termination or certificate generation.

### Can I run guppi without a password?

Yes, with `--no-auth`. Only safe on a trusted local network or behind a
reverse proxy that handles auth itself. Peer bootstrap requires a
password, so multi-host peering is disabled when `--no-auth` is set.

### What happens if a peer goes offline?

Local sessions stay fully functional. The peer's sessions are kept
visible (as offline) for 5 minutes, then pruned. Auto-reconnect retries
in the background; when the peer comes back the link recovers within
backoff (max 30s).

### How do I forget a paired machine?

Open **Settings → Machines**, find the machine, click **Forget**. The
forget message propagates over the live link so both sides clean up.
If the link is already down, the remote will eventually hit "unknown
peer" on its next dial and keep retrying — you can forget it on that
side too.

### How do I copy text from the terminal?

The terminal captures mouse events, so normal click-and-drag selects text inside tmux rather than copying to your clipboard. Hold a modifier key while selecting to override this and copy to the system clipboard:

| Platform         | Select to copy                                                                                                                |
| ---------------- | ----------------------------------------------------------------------------------------------------------------------------- |
| **macOS**        | Hold `Option` and drag to select, then `Cmd+C` to copy                                                                        |
| **Linux**        | Hold `Shift` and drag to select, then `Ctrl+Shift+C` to copy                                                                  |
| **iOS (Safari)** | Touch-select doesn't work in the terminal. Connect a mouse or trackpad and use `Option`+drag, then copy from the context menu |

This is standard xterm.js behavior — the modifier key tells the browser to handle the selection instead of sending the mouse events to tmux.

## Tech stack

- **Backend:** Go, chi v5, gorilla/websocket, creack/pty
- **Frontend:** React 19, TypeScript, Vite, Tailwind CSS v4, xterm.js
- **Build:** Single binary with `//go:embed`, GoReleaser for releases

## License

MIT
