# Scheduled Command Sessions — Implementation Plan

Feature: schedule any command to run on a cron schedule; each fire spawns a new
tmux session. Sessions spawned by the same schedule are grouped/stacked in the
sidebar.

## Decisions (locked)

- **Cron expressions** for schedule spec (standard 5-field).
- **Always spawn** a new uniquely-named session per fire.
- **Persist** schedules to `~/.config/termyard/schedules.json`.
- Spawned sessions tagged with owning **ScheduleID** so frontend can group them.

## Dependency

Add `github.com/robfig/cron/v3`. Use **only its parser**, not its built-in
scheduler:

- `cron.ParseStandard(spec)` → validate + obtain `Schedule`.
- `schedule.Next(time.Now())` → compute `NextRun`.
- Keep our own ticker loop for ctx-cancel, hot-reload of edited jobs, and
  persistence — do not hand lifecycle to cron's internal goroutine.

## Backend

### `pkg/scheduler/store.go` — JSON-persisted registry

Mirror `preferences.Store` (mutex + `~/.config/termyard/` + load/save).

```go
type Job struct {
    ID                string
    Name              string
    CronSpec          string // standard 5-field cron
    Command           string
    Path              string
    AgentType         string
    Host              string // "" = local; else peer
    SessionNamePrefix string
    WorktreeBranch    string
    Enabled           bool
    LastRun           time.Time
    NextRun           time.Time
    RunCount          int
    CreatedAt         time.Time
}
```

Methods: `Add`, `Update`, `Remove`, `List`, `Get`, `MarkRan(id, lastRun, nextRun)`.
Validate `CronSpec` via parser on Add/Update; reject bad specs.

### `pkg/scheduler/runner.go`

`Runner{store, client, stateMgr, peerMgr, createFn, log}`:

- `Run(ctx)`: `time.NewTicker(1s)`; each tick iterate enabled jobs where
  `now >= NextRun`, spawn, then `MarkRan` with `NextRun = schedule.Next(now)`.
  Return on `ctx.Done()`.
- On startup, recompute `NextRun` for all enabled jobs (persisted NextRun stale).
- Unique session name per fire: `<prefix|name>-<unixts>`.
- Peer host offline at fire time → log + skip (no retry-storm).

### Refactor — shared spawn

Current `POST /api/session/new` handler (`pkg/server/server.go:554`) has inline
worktree + peer-forward + NewSession + agent-type + state-refresh logic. Extract
into `CreateSession(opts, CreateSessionReq) error`:

- `CreateSessionReq.ScheduleID string` — runner sets it; HTTP leaves empty.
- Reused by HTTP handler and scheduler runner (via injected `createFn`).

### Session → schedule tagging

- Persist `ScheduleID` in `sessionattrs.Store` (already keyed `<host>/<name>`):
  add `ScheduleID` field → flows to frontend via existing attr broadcast. No new
  transport.

### REST in `pkg/server/server.go`

Add `Scheduler`/`SchedulerStore` to `Options` (struct ~line 51). Mirror the
portforward route block (~line 1174):

- `GET /api/schedules` — list
- `POST /api/schedules` — create (validate cron)
- `PUT /api/schedules/{id}` — edit / enable-disable
- `DELETE /api/schedules/{id}`
- `POST /api/schedules/{id}/run` — run-now (fire immediately, does not disturb NextRun)

### Wire `pkg/commands/server/server.go`

Build store + runner, add to `Options`, `go runner.Run(ctx)` near line 82.
No shutdown cleanup needed (store auto-persists; no child procs to kill).

## Frontend

### Hooks / components

- `web/src/hooks/useSchedules.ts` — mirror `usePortForwards.ts`
  (fetch/create/update/delete/run-now).
- `web/src/components/ScheduleModal.tsx` — mirror `PortForwardModal.tsx`; reuse
  `NewSessionModal` fields (command, path, agent, host, worktree) + cron input
  with presets (hourly/daily/weekly/custom) + enable toggle. List shows name,
  cron, next-run (relative), run-count, edit/delete/run-now.
- Open from `TopBar` or `Settings`.

### Sidebar grouping

- `useSessions` already carries attrs; group sessions by `attr.scheduleID`.
  Ungrouped sessions render flat (today's behavior).
- Group header pulls live cron/next/count from `useSchedules`; children pull
  status from `useSessions`.

### Collapse behavior (IMPORTANT)

- **Schedule groups are COLLAPSED BY DEFAULT, showing only the latest run.**
  - Rationale: frequent crons (e.g. hourly = 24/day) would flood the sidebar;
    the latest run is the primary signal; header already shows next + run count.
  - Collapsed view: header (`▸`) + single latest child session row.
  - If latest is `done`/`exited` and nothing running, still show it (last result
    matters).
- Click `▸` to expand full stack (`▾`). Newest-first, `+N more` overflow.
- Collapse state persists in prefs (mirror existing sidebar collapse pattern).
- States: `⏱` active · `⏸` paused (Enabled=false) · `⚠` peer offline.
- Optional: auto-prune killed child sessions older than latest so the stack does
  not grow unbounded.

### Sidebar mockup (collapsed default)

```
╭─ SESSIONS ─────────────────────────────╮
│  ● my-dev-session              claude   │
│  ● scratch                     shell    │
│                                         │
│  ⏱ nightly-build                   ▸   │  ← collapsed (default)
│     0 2 * * *  ·  next 4h  ·  12 runs   │
│  │  ● build-1718    running    2m       │  ← latest only
│                                         │
│  ⏱ hourly-sync                     ▾   │  ← expanded
│     0 * * * *  ·  next 22m  ·  97 runs  │
│  │  ● sync-9842    running    1m        │
│  │  ● sync-9841    done       1h        │
│  │  ● sync-9840    done       2h        │
│  ╰  + 94 more                           │
│                                         │
│  ⏸ paused-job                          │
│     */5 * * * *  ·  paused  ·  3 runs   │
╰─────────────────────────────────────────╯
```

## Tests

- `store_test.go`: persist→reload roundtrip, CRUD, cron validation rejects garbage.
- `runner_test.go`: injectable clock + fake `createFn`/tmux client; assert fire
  eligibility, `NextRun` advancement, disabled jobs skipped, startup recompute.

## Risks

- Cron min granularity = 1 min (5-field). Sub-minute later → `cron.WithSeconds()`
  6-field; flag as future.
- Always-spawn + frequent cron = session pileup; mitigate with optional
  auto-kill-after / max-concurrent (future) + the latest-only collapse default.
- Peer-host jobs depend on peer being connected at fire time.

## Suggested order

1. `pkg/scheduler` (store + runner) + tests.
2. Refactor shared `CreateSession` + ScheduleID attr tagging.
3. REST endpoints + wiring.
4. Frontend hook + modal.
5. Sidebar grouping + collapse-by-default.
