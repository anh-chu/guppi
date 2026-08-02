# Termyard architecture

Termyard is a single-user dashboard for coding agent sessions. The Go server runs a local web dashboard, manages detached session daemons, tracks agent status, and can peer with other Termyard nodes.

## Runtime overview

```
Browser  <──WebSocket──>  Go Server  <──Unix socket──>  Session Daemon (PTY)
                              │
                              ├── Session discovery (daemon registry scan)
                              ├── Tool event tracker (agent status)
                              ├── WebSocket hub (state broadcasts)
                              └── Peer manager (symmetric multi-host links)
```

Each session runs as an independent daemon process with its own PTY. Daemons are fully detached and survive server crashes and restarts. The server discovers daemons by scanning Unix sockets in the session directory and connects on demand.

## Backend ownership

- `pkg/commands/server/` — `termyard server` command flags, composition root, monitor wiring
- `pkg/server/` — chi HTTP server, route registration, and embedded frontend
- `pkg/pty/` — daemon registry, lifecycle store, crash/recovery, PTY I/O
- `pkg/state/` — central session tree, previews, and diff-based broadcasting
- `pkg/peer/` — peer wire protocol, manager, and PTY relay
- `pkg/auth/` — password auth and session management
- `pkg/toolevents/` — agent detection, event tracking, silence monitor, prompt parser
- `pkg/commands/notify/` — `termyard notify` CLI used by agent hooks
- `pkg/commands/agent-setup/` — per-agent hook installers and embedded plugin/extension sources
- `pkg/ws/` — WebSocket hub and terminal bridge

## Frontend ownership

- `web/src/App.tsx` — composition shell and global shortcuts
- `web/src/components/` — feature components (Sidebar, Terminal, Overview, Settings, etc.)
- `web/src/hooks/` — transport and lifecycle hooks
- `web/src/lib/` — pure utilities (pane tree, terminal pool, session state)
- Build output: `web/dist/` is embedded from `pkg/server/dist` via `//go:embed`

## Multi-host peering

Nodes are symmetric. Each server can connect to any other reachable server from the dashboard (**Settings → Machines → Connect to another machine**). Pairing is not transitive; link machines directly. Peer records live in `~/.config/termyard/peers.json`.

## Agent events

Agents report state by invoking `termyard notify`. The CLI prefers the local Unix socket and falls back to HTTP only when the socket is unreachable. Tool events carry `Host` and `HostName` so the frontend can build session keys (`host/session`) in multi-host setups.

See `docs/agent-detection.md` and `docs/agent-setup.md` for the per-agent hook details.
