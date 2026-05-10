# guppi — Overview

> **Navigation aid.** This article shows WHERE things live (routes, models, files). Read actual source files before implementing new features or making changes.

**guppi** is a go project built with chi.

## Scale

72 API routes · 13 UI components · 55 library files · 1 middleware layers · 6 environment variables

## Subsystems

- **[Auth](./auth.md)** — 10 routes — touches: auth, db, queue, ai
- **[Payments](./payments.md)** — 2 routes — touches: auth, db, queue, ai
- **[*](./section.md)** — 1 routes — touches: auth, db, queue, ai
- **[Activity](./activity.md)** — 2 routes — touches: auth, db, queue, ai
- **[Agent-status](./agent-status.md)** — 2 routes — touches: auth, db, queue, ai
- **[Cols](./cols.md)** — 1 routes — touches: auth, db, queue, ai
- **[Host](./host.md)** — 1 routes — touches: auth, db, queue, ai
- **[Hosts](./hosts.md)** — 2 routes — touches: auth, db, queue, ai
- **[Hub](./hub.md)** — 1 routes — touches: auth
- **[Name](./name.md)** — 1 routes — touches: auth, db, queue, ai
- **[Notify](./notify.md)** — 1 routes — touches: auth, ai
- **[Pair](./pair.md)** — 4 routes — touches: auth, db, queue, ai
- **[Preferences](./preferences.md)** — 4 routes — touches: auth, db, queue, ai
- **[Pty_relay](./pty_relay.md)** — 1 routes — touches: auth, db
- **[Push](./push.md)** — 4 routes — touches: auth, db, queue, ai
- **[Rows](./rows.md)** — 1 routes — touches: auth, db, queue, ai
- **[Session](./session.md)** — 10 routes — touches: auth, db, queue, ai
- **[Sessions](./sessions.md)** — 2 routes — touches: auth, db, queue, ai
- **[Stats](./stats.md)** — 2 routes — touches: auth, db, queue, ai
- **[Tls](./tls.md)** — 6 routes — touches: auth, db, queue, ai
- **[Tool-event](./tool-event.md)** — 4 routes — touches: auth, db, queue, ai
- **[Tool-events](./tool-events.md)** — 4 routes — touches: auth, db, queue, ai
- **[Version](./version.md)** — 2 routes — touches: auth, db, queue, ai
- **[Ws](./ws.md)** — 4 routes — touches: auth, db, queue, ai

**UI:** 13 components (react) — see [ui.md](./ui.md)

**Libraries:** 55 files — see [libraries.md](./libraries.md)

## High-Impact Files

Changes to these files have the widest blast radius across the codebase:

- `path/filepath` — imported by **17** files
- `encoding/json` — imported by **15** files
- `web/src/hooks/usePreferences.ts` — imported by **10** files
- `crypto/rand` — imported by **9** files
- `web/src/theme.ts` — imported by **9** files
- `net/http` — imported by **8** files

## Required Environment Variables

- `PATH` — `pkg/commands/install/install.go`
- `SHELL` — `pkg/tmux/client.go`
- `TMPDIR` — `pkg/socket/socket.go`
- `TMUX_PANE` — `pkg/commands/notify/notify.go`
- `XDG_DATA_HOME` — `pkg/webpush/vapid.go`
- `XDG_RUNTIME_DIR` — `pkg/socket/socket.go`

---
_Back to [index.md](./index.md) · Generated 2026-05-10_