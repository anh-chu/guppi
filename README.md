# Termyard

Every coding agent session, in one browser tab.

Know the moment an agent needs you.

---

You run coding agents. One session has Claude churning through a refactor, another has Codex waiting on a tool approval you forgot about, a third finished ten minutes ago and you never noticed. The work is spread across sessions and sometimes machines, and you find out what each agent is doing by switching to it and looking.

Termyard parks all of that in one place. It renders your live sessions in the browser and watches the agents running inside them, so a glance tells you which ones are working, which are stuck waiting, and which are done. When one needs your attention, it tells you, even if the tab is in the background.

<!-- TODO: drop a dashboard screenshot or GIF here once captured, e.g. ![Termyard dashboard](docs/brand/dashboard.png) -->

## Highlights

- **Agent status at a glance.** See which agents are active, waiting for input, or errored across every session, without switching to each one.
- **Overview board and peek.** Group every session into a kanban board by state (needs you, working, idle), hover any session for a read-only snapshot of its terminal, and click to dock a live, resizable terminal beside the board.
- **Push notifications.** Get a browser or desktop alert when an agent needs you, including when the tab is closed or you are signed out.
- **Multi-machine.** Connect any number of Termyard nodes through the dashboard. Sessions on every machine show up in one view.
- **The real terminal, in the browser.** PTY-backed xterm.js rendering means you get the exact terminal: borders, splits, colors, scrollback. Type, scroll, and resize like you are local.
- **Images in the terminal.** Full kitty graphics protocol support, so image tools render inline in the browser terminal: `icat`, `timg`, `chafa`, `viu`, neovim image plugins, and terminal browsers. Works over local and remote sessions. Most browser terminals cannot do this at all.
- **Live session discovery.** Sessions update in real time through daemon process scanning.
- **Quick switcher.** `Ctrl+K` to jump between sessions and windows. Your hands never leave the keyboard.
- **One binary.** Go backend with the React frontend embedded. No separate processes, no Node runtime in production.

## Install

### Quick install

```bash
curl -sSL https://raw.githubusercontent.com/anh-chu/termyard/master/scripts/install.sh | sh
```

Detects your platform, downloads the latest release, and drops `termyard` in `/usr/local/bin` (or `~/.local/bin` if that is not writable). Pin a version with `VERSION=v0.12.0`, or set `BIN_DIR` to choose where it lands.

### With dist

[dist](https://github.com/ekristen/distillery) installs from GitHub releases with checksum verification and multi-version support.

```bash
dist install anh-chu/termyard
```

### From source

Requires [Go](https://go.dev/) 1.25+ and [Node.js](https://nodejs.org/) 24+ / npm 11+.

```bash
git clone https://github.com/anh-chu/termyard.git
cd termyard
make build
# Binary lands at ./dist/termyard
```

## Quickstart

### 1. Start the server

```bash
termyard server
```

Open `http://localhost:7654`. On first launch you set a password, then Termyard walks you through agent setup.

By default Termyard serves plain HTTP. For LAN or trusted local use you can enable built-in TLS with `--tls` (self-signed) or `--tls-cert` / `--tls-key` (your own certificate). For production remote access, put it behind a reverse proxy (Caddy, nginx) or an overlay network like [Tailscale](https://tailscale.com/) or WireGuard. See the [FAQ](#faq) below.

### 2. Wire up your agents

Agents report their status through hooks, so they need configuring once per machine:

```bash
termyard agent-setup
```

This detects which agents you have installed and configures each one:

- **Claude Code**: hooks in `~/.claude/settings.json`
- **Codex**: lifecycle hooks in `~/.codex/hooks.json`, enabled by `hooks = true` in `~/.codex/config.toml`
- **OpenCode**: plugin at `~/.config/opencode/plugins/termyard.js`
- **Pi**: extension at `~/.pi/agent/extensions/termyard.ts`

Check hook status any time under **Settings → Agents**, or at `/setup`. For manual setup, see [docs/agent-setup.md](docs/agent-setup.md).

Useful flags: `--dry-run` previews changes without writing, `--config-dir agent=path` targets a non-default config location, `--block` lets hook failures surface instead of being ignored.

### 3. Day to day

Once hooks are in place, status shows up on its own:

- The **Overview** lists every session and surfaces any agent that needs attention. Switch it to a **board** to sort sessions into columns by state, hover a session to glance at its terminal, and click one to open a docked terminal you can drag to resize.
- The **sidebar** shows each session with its task and the agent's latest message, plus status badges for active, waiting, and errored agents.
- **Push notifications** alert you when an agent needs input, even with the tab closed. Enable them under **Settings → Notifications**.
- **Splits**: drag a session onto another to tile them side by side, or drop on an edge to pick orientation. `Ctrl+Shift+\` splits the active pane into a new session.
- **Quick switcher** (`Ctrl+K`) jumps between sessions and windows.

## Multiple machines

Termyard nodes are symmetric. Every machine runs the same `termyard server`, and any node can connect to any other.

On each machine you want in the mesh, run `termyard server`, open its dashboard, and set a password. Then from machine **A**:

1. Go to **Settings → Machines → Connect to another machine**.
2. Enter machine **B**'s address (for example `devvm-b.local:7654`) and **B**'s dashboard password.
3. Leave **Auto-reconnect** on and click **Connect**.

A and B now share sessions both ways. You run Connect once per pair, from whichever side can reach the other. The reverse direction is automatic.

A dropped link auto-recovers within 30s using exponential backoff. Toggle **Auto-reconnect** off to stop dialing without forgetting the peer, or click **Forget** to remove it entirely. Pairing is not transitive: linking A to B and A to C does not let B see C. Pair B and C directly for that.

See [docs/multi-host.md](docs/multi-host.md) for reachability tips (NAT, Tailscale, reverse proxy).

## How it works

```
Browser  <──WebSocket──>  Go Server  <──Unix socket──>  Session Daemon (PTY)
                              │
                              ├── Session discovery (daemon registry scan)
                              ├── Tool event tracker (agent status)
                              └── Unix socket (local CLI notifications)
```

Each session runs as an independent daemon process with its own PTY. Daemon processes are fully detached (`Setsid`) and survive server crashes, restarts, and OOM events. The server discovers sessions by scanning Unix sockets and connects to them on demand. Multiple browser tabs can attach to the same session simultaneously, with output replayed from a 1MB ring buffer on reconnect.

### Session status

Sessions show as **active** or **idle**, driven by the foreground process:

- **Active**: at least one pane has a foreground process that is not a shell, for example `vim`, `claude`, `node`, `python`, or `go build`.
- **Idle**: the session sits at a shell prompt (`bash`, `zsh`, `fish`, `sh`, `dash`, `ksh`, `csh`, `tcsh`, `login`).

### Alerts

Alerts surface when an agent needs attention. They appear in the banner at the top of every page and in **Pending Alerts** on the overview.

- **Waiting**: the agent is waiting on user input, such as a tool approval in Claude Code.
- **Error**: the agent hit an error.
- **Active**: the agent is running normally, shown as a sidebar badge rather than an alert.

Alerts are live server state. They always reflect the current status and survive refreshes. Dismissing one hides it from the UI without touching the agent. Push alerts run independently of the tab, including when it is closed or you are logged out.

## Reference

### Environment variables

| Variable                      | Default                 | Description                        |
| ----------------------------- | ----------------------- | ---------------------------------- |
| `TERMYARD_PORT`               | `7654`                  | HTTP server port                   |
| `TERMYARD_SOCKET`             | auto                    | Unix socket path for local CLI     |
| `TERMYARD_URL`                | `http://localhost:7654` | Server URL for notify/agent-setup  |
| `TERMYARD_NO_AUTH`            | `false`                 | Disable authentication             |
| `TERMYARD_TLS`                | `false`                 | Serve HTTPS with a self-signed cert |
| `TERMYARD_TLS_CERT`           |                         | Path to a TLS certificate (PEM)    |
| `TERMYARD_TLS_KEY`            |                         | Path to a TLS private key (PEM)    |

Session discovery polls the daemon registry every 2 seconds; this interval is not currently configurable.

### CLI flags

```
termyard server [flags]
  -p, --port int                  HTTP server port (default 7654)
      --socket string             Unix socket path for local notify CLI (auto-detected if omitted)
      --no-auth                   Disable authentication (not recommended for remote access)
      --tls                       Serve HTTPS with a self-signed cert
      --tls-cert string           Path to a TLS certificate file (PEM)
      --tls-key string            Path to a TLS private key file (PEM)
```

Multi-host peering is configured in the dashboard (**Settings → Machines**). Peer records live in `~/.config/termyard/peers.json` and are managed entirely by the UI. There are no `--hub` or peering CLI commands. See [docs/multi-host.md](docs/multi-host.md).

### Run as a service

Install Termyard as a user service so it starts on login (systemd on Linux, launchd on macOS):

```bash
termyard install     # install and start the service
termyard uninstall   # remove it
```

### Updating

```bash
termyard update                 # update to the latest release
termyard update --check         # check without installing
termyard update --version v0.2.0
```

Stable releases are published by the `v*` tag-triggered GoReleaser workflow. To cut a release, update `pkg/common/version.go` and `web/package.json`, run `./scripts/release.sh` to validate the files, commit, tag `vX.Y.Z`, and push the tag.

### Keyboard shortcuts

Press `Ctrl+/` (or `Cmd+/` on macOS) for the full list, or click the `?` in the status bar.

| Shortcut | Action                                                   |
| -------- | -------------------------------------------------------- |
| `Ctrl+K` | Quick switcher, jump between sessions and windows        |
| `Ctrl+J` | Jump to next alert (cycles through waiting/error agents) |
| `Ctrl+H` | Overview                                                 |
| `Ctrl+,` | Settings                                                 |
| `Ctrl+\` | Toggle sidebar                                           |
| `Ctrl+L` | Lock / sign out                                          |
| `Ctrl+/` | Keyboard shortcuts help                                  |

### Manual notifications

Send status updates from scripts or the command line. Session and pane are auto-detected from `TERMYARD_SESSION`/`TERMYARD_PANE` environment variables:

```bash
termyard notify -t claude -s waiting -m "Needs approval"
termyard notify -t codex -s active
termyard notify -t claude -s completed
```

`termyard notify` prefers the local Unix socket and falls back to HTTP only when the socket is unreachable. More flags carry session context for hooks: `--user-prompt` (the user's first message, set once, becomes the task label), `--agent-message` (the agent's latest reply), `--stdin` and `--event-data` (read a hook payload as JSON), `--agent-session-id`, plus `--session` / `--window` / `--pane` to override auto-detection.

### Copying text from the terminal

The terminal captures mouse events, so a normal click and drag selects text inside the terminal instead of copying. Hold a modifier while selecting to copy to the system clipboard:

| Platform         | Select to copy                                                                                                              |
| ---------------- | --------------------------------------------------------------------------------------------------------------------------- |
| **macOS**        | Hold `Option` and drag to select, then `Cmd+C` to copy                                                                      |
| **Linux**        | Hold `Shift` and drag to select, then `Ctrl+Shift+C` to copy                                                                |
| **iOS (Safari)** | Touch-select does not work in the terminal. Connect a mouse or trackpad, use `Option`+drag, then copy from the context menu |

This is standard xterm.js behavior. The modifier tells the browser to handle the selection instead of sending mouse events to the terminal.

### Images in the terminal

Termyard's browser terminal implements the kitty graphics protocol, so programs that draw raster images render them inline, in the browser, over a real (possibly remote) PTY. That covers image viewers (`icat`, `timg`, `chafa -f kitty`, `viu`), neovim image plugins (image.nvim, hologram, molten), and terminal browsers. This is unusual: xterm.js has no built-in kitty graphics support, and most browser terminals manage Sixel at best.

It works whether the session is local or on a remote paired machine. Programs often hand the terminal a file, temp file, or shared-memory handle instead of the pixels; the browser cannot read those, so the session daemon (which runs where your shell runs) reads the data and streams it to the browser for you. Direct, zlib-compressed, RGB/RGBA/PNG, chunked transfers, cursor placement, and Unicode placeholders are all handled, and Sixel still works alongside it.

**Limitations.** A few kitty features are not implemented. They degrade quietly, nothing crashes:

- No animation or video (for example `mpv`, animated frames).
- No cropping to a sub-region of an image, relative placements, or drawing text on top of an image (negative z-index).
- Very long scrollback can eventually misalign an image that has scrolled far back into history.

### Lock and sign out

Click the lock in the status bar (or `Ctrl+L`) to sign out. Auto-lock after idle is configurable under **Settings → Security**, including a shorter timeout when the tab is backgrounded.

## FAQ

### How do I get HTTPS or remote access?

For LAN or trusted local use, start the server with `--tls` to use a self-signed certificate, or `--tls-cert` / `--tls-key` to use your own. For production remote access, put one of these in front of it:

- **Tailscale / WireGuard**: stable hostnames, end-to-end encryption, no proxy config. Recommended.
- **Reverse proxy**: Caddy or nginx terminating TLS in front of `localhost:7654`.

### Can I run Termyard without a password?

Yes, with `--no-auth`. Only safe on a trusted local network or behind a reverse proxy that handles auth. Peer bootstrap requires a password, so multi-host peering is disabled when `--no-auth` is set.

### What happens if a peer goes offline?

Local sessions keep working. The peer's sessions stay visible (marked offline) for 5 minutes, then get pruned. Auto-reconnect retries in the background, and the link recovers within backoff (max 30s) when the peer returns.

### How do I forget a paired machine?

Open **Settings → Machines**, find the machine, and click **Forget**. The forget message propagates over the live link so both sides clean up. If the link is already down, the remote eventually hits "unknown peer" on its next dial. Forget it on that side too.

## Non-goals

- **Multi-user.** Termyard is single-user. One person, one dashboard. No accounts, roles, or shared access controls.
- **Agent orchestration.** Termyard does not start, stop, or control your agents. It watches and reports. You run agents however you like.
- **Session management.** Termyard manages session daemon processes. You control what runs inside them.

## Development

```bash
# Frontend dev server (hot reload)
cd web && npm ci && npm run dev

# Go server
go run . server
```

## Tech stack

- **Backend:** Go, chi v5, gorilla/websocket, creack/pty
- **Frontend:** React 19, TypeScript, Vite, Tailwind CSS v4, xterm.js
- **Build:** Single binary with `//go:embed`, GoReleaser for releases

## Credits

Termyard grew out of [ekristen/guppi](https://github.com/ekristen/guppi) by Erik Kristensen. Huge thanks to that project, which is the foundation and the heavy inspiration this one is built on.

## License

MIT
