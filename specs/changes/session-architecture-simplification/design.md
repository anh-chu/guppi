# Design — Session Architecture Simplification (Option B, rev 3)

**Status:** approved (rev 3)
**Parent:** `proposal.md`, `requirements.md` (rev 3)

## Overview

Event-driven single authority, hardened against transport ambiguity:

- In-memory map in the runtime is the single authority for local session
  membership; the state manager mirrors it via explicit `AddSession` /
  `RemoveSession`.
- Add: only via launch (`onLaunched(info)`) or one-time boot adoption.
- Remove: only when the daemon PID is confirmed dead (watch EOF + PID check)
  or via user kill.
- Transport failure with a live PID → "unreachable" + backoff reconnect.
  Never destructive.
- Delete: 2s `runDaemonRefresh` scan-and-diff, `UpdateSessions` guards,
  `pkg/pty/lifecycle.go`, crash detection/recovery, frontend polling and
  prune debounce.

## Instance identity

```go
// pkg/pty
type Instance struct {
    Name          string
    Pid           int       // daemon PID from sidecar JSON / Create
    Nonce         string    // 8-byte hex random written by daemon at spawn
    ProcStartTime int64     // fallback for legacy daemons (no nonce): /proc/<pid>/stat field 22
    SystemdUnit   string    // systemd scope name, captured immutably at spawn/adoption
}

// Identity match: nonce == "" ? (Pid+ProcStartTime) : (Pid+Nonce)
```

New daemons write a random nonce (8 bytes hex) to the sidecar at spawn.
Legacy daemons (pre-upgrade, no nonce) use PID + process start time from
/proc as fallback identity — this guards PID reuse across crashes. Every
watcher, exit callback, and cleanup action carries an `Instance`. Destructive
actions (RemoveSession, stale-file removal, `stopSystemdUnit`) execute only if
the runtime's currently tracked instance for that name matches the callback's
instance by identity. This kills the ABA/name-reuse class: a delayed callback
from killed session "A" (pid 100, nonce ABC) cannot touch the new "A" (pid
200, nonce XYZ). Reconnect after transport loss re-reads the sidecar JSON and
verifies identity match before resuming the watch; a sidecar mismatch with
live PID means the daemon was replaced outside the server — the session
becomes unreachable and retries indefinitely until the PID dies (then removed)
or user kills the session (removal on user action). The new daemon is NOT
silently adopted; it appears at next boot (server-mediated creation is the
supported path, R14).

## Watch mechanism

### Protocol: idle client on the existing protocol, no new verb

Unchanged from rev 1 (verified against `pkg/pty/daemon.go:504-575,614-659`):
a plain client connection that discards `FrameReplay`/`FrameOutput` frames;
daemon shutdown/SIGKILL closes it. Every shipped daemon supports this, so
legacy fallback is unnecessary (R10). Output flood is discarded reads on a
local unix socket; the daemon's non-blocking broadcast may drop frames to a
slow watcher — irrelevant, frames are discarded anyway, and the connection is
never closed for slowness.

### Socket directory lock coordination

Server holds an flock on the socket directory during adoption (boot). CLI
`termyard session create` offline-fallback first attempts the local server's
launch API. If connection is refused or server socket is absent (confirmed
server absence), then check the lock: if held, server is booting, wait/retry
API path. If free, acquire it, then spawn directly (daemon will be adopted at
next server boot). A reachable server returning 5xx or timeout is a server
error; do not spawn directly — surface the error to the user. This prevents
the race: direct CLI spawn during adoption window completing before Ready closes.

### Registry API (`pkg/pty/registry.go`)

```go
// Create spawns the daemon, waits (bounded ~2s) for the sidecar JSON
// (including nonce write), and returns the new instance's info. Error if the
// daemon never binds.
func (r *Registry) Create(name, shell, cwd string, cols, rows uint16) (SessionInfo, error)

// Watch dials inst's socket and holds it open, discarding frames. Captured
// immutable systemd unit from inst.SystemdUnit at spawn/adoption.
// On read error/EOF it consults verdict:
//   - PID dead (processAlive==false, matched via inst.Pid and, for legacy,
//     inst.ProcStartTime from /proc/<pid>/stat field 22) -> onExit(inst),
//     once.
//   - PID alive -> onUnreachable(inst, true); reconnect with backoff
//     250ms,500ms,1s,2s,5s,5s,... forever; on success verify sidecar still
//     matches inst (nonce or legacy fallback), call onUnreachable(inst,
//     false), resume. Sidecar mismatch with live PID -> keep retrying
//     indefinitely (session unreachable); only remove when PID confirmed dead.
// Initial dial retries for up to 2s (daemon startup tolerance); if it never
// succeeds AND the PID is dead -> onExit; if PID alive -> unreachable loop.
// stop() suppresses all callbacks and returns after the goroutine has exited
// (synchronous stop-and-wait). Callback suppression covers callbacks already
// in flight.
func (r *Registry) Watch(inst Instance, onExit func(Instance),
    onUnreachable func(Instance, bool)) (stop func(), err error)

// Scan performs one read-only pass over the socket dir, returning
// SessionInfo for every sidecar/socket pair whose daemon PID is alive.
// Used by boot adoption and the CLI list fallback. Never deletes files.
func (r *Registry) Scan() []SessionInfo

// Adopt = Scan + stale cleanup: PID-dead leftovers get their .sock/.json
// removed after re-verifying sidecar identity (nonce or legacy start-time)
// still matches. Sockets whose PID is alive are always adopted, even if a
// probe dial fails (Watch's unreachable loop handles it) — resolves the
// rev-1 adopt/remove contradiction: liveness is decided by PID, not by dial.
func (r *Registry) Adopt() []SessionInfo
```

`Create` signature change ripples to `sessionlaunch.DaemonRegistry`
(`pkg/sessionlaunch/service.go:55-58`) and the `daemonAdapter` /
`registryView` in `runtime.go` and CLI (`sessiondaemon.go:69`).

Kept: `SocketPath`, `Kill` (minus lifecycle `Transition`), `Capture`,
`CaptureTail`, `readDaemonPID`, `processAlive`, systemd helpers,
`validSessionID`. Changed: `stopSystemdScope(name)` -> `stopSystemdUnit(unit)`
called with immutable unit from Instance (no re-reading mutable sidecar).
Deleted: scanning `List()` + failCount/grace, `IsSessionDead`, lifecycle
store wiring, all crash APIs (`CrashedSessions`,
`DetectAndCleanupCrashes`, `RecoverSession`, `DismissSession/All`,
`CleanupCrashedIfDead`). Deleted file: `pkg/pty/lifecycle.go`; daemon stops
writing lifecycle records (`daemon.go` step 3b, `lifecycleStore` field,
shutdown `Transition`).

## State manager (`pkg/state`)

- Delete `UpdateSessions` + guards (`sessions.go:18-140`).
- Add `AddSession(*model.Session)` — insert, broadcast `session-added` +
  `sessions-changed`.
- Change `RemoveSession` (`sessions.go:497`) to broadcast **only when the
  session was actually present** (exactly-once events, R15). Returns bool.
- Add `UpdateSessionFields(name, fn)` for the enrichment tick; broadcasts
  `sessions-changed` only on change.
- Add `Unreachable(name, bool)` — sets a field on the session model,
  broadcast `sessions-changed` (frontend shows the existing
  disconnected-style indicator; session stays in list).
- `loadDaemonSessionDetails` (`sessions.go:166-230`) becomes enrichment-only:
  input is the tracked session's own info; keeps synthetic window/pane, live
  cwd, preview, worktree detection (`sessions.go:150-157`), `applyMetadata`,
  and stale-agent cleanup (`sessions.go:233-293`) — full parity per R12.
- `DaemonRegistry` interface (`manager.go:88-100`): drop `CrashedSessions`,
  `IsSessionDead`; `List()` remains but is the adapter's in-memory snapshot.
  Delete `CrashedSessionInfo`.

## Runtime (`pkg/commands/server/runtime.go`)

### Session authority

```go
type trackedSession struct {
    inst  pty.Instance
    stop  func()      // watcher stop-and-wait
}
// rt.sessionsMu sync.Mutex; rt.tracked map[string]trackedSession
```

- `onLaunched(info pty.SessionInfo)`: under `sessionsMu` — if an entry exists
  for the name, detach it and mark it superseded, swap the map entry (duplicate-add
  handling); release `sessionsMu`; join the old watcher via `stop()` (synchronous
  stop-and-wait). Then: under `sessionsMu` again, build `model.Session` (reuse
  per-session body of `refreshSessions`, `runtime.go:470-493`), `stateMgr.AddSession`,
  `reg.Watch(inst, rt.onExit, rt.onUnreachable)`, store entry, refresh adapter
  snapshot, release `sessionsMu`.
- `onExit(inst)`: under `sessionsMu` — no-op unless `tracked[inst.Name].inst
  matches inst by identity`; then delete entry, `stateMgr.RemoveSession`,
  best-effort `stopSystemdUnit(inst.SystemdUnit)` + stale-file removal
  (`.sock`/`.json` deleted only after re-verifying sidecar identity still
  matches) **for that instance only**.
- `onUnreachable(inst, bad)`: instance-matched; `stateMgr.Unreachable`.
- Kill route: keeps eager `RemoveSession` for snappy UX; also marks the
  tracked entry "removal already broadcast" (or simply relies on
  `RemoveSession`'s presence-gated broadcast) so watch EOF confirmation emits
  nothing (R15).
- `Runtime.Stop()` (`runtime.go:451`): before `cancel()`, release any
  lockheld (adoption lock), then detach and mark all watchers under
  `sessionsMu`, release `sessionsMu`, call every `stop()` (each waits for its
  goroutine after mutex released), clear map. Callback suppression covers
  callbacks already in flight.

### Boot (atomic adoption)

In `Start`, **before** `close(rt.ready)` (`runtime.go:440`): acquire flock
on socket directory (held until after adoption completes), boot cleanup of
lifecycle-record files (R11), then `for _, info := range reg.Adopt() {
rt.onLaunched(info) }` synchronously, release lock. HTTP serving gates on
`Ready()` already; server-mediated launches therefore cannot race adoption,
and `sessionsMu` serializes everything else. CLI direct-spawn during this
window: lock free -> spawn directly; lock held -> wait/retry API path
(R14, T4).

### Deletions and wiring

- Delete `runDaemonRefresh` (`runtime.go:434,496-521`), `refreshSessionsFunc`,
  `daemonAdapter.refresh()`, `IsSessionDead`/`CrashedSessions` adapter
  methods, matching `registryView` methods.
- `daemonAdapter.snap` is written by the runtime on add/remove and by the
  enrichment tick; `List()` keeps serving the in-memory copy.

### Enrichment tick (2s — preserves detector latency)

Every 2s, for each tracked session: refresh live cwd (`/proc/<shellpid>/cwd`),
pane PID, preview (`shouldRefreshPreview`/`refreshPreview`), worktree fields,
`applyMetadata`, stale-agent cleanup, then update adapter snapshot. Never
add/remove. Failures (socket dir unreadable, FD exhaustion, watch connection
unresponsive) leave sessions in current state (no removal); liveness state
(unreachable) already set by Watch callback. Cadence stays 2s so these
consumers keep today's latency:

| Consumer | Location | Feed after change |
|---|---|---|
| Agent process detector | `runtime.go:524` `listPanes` | adapter snapshot |
| Reconciler | `runtime.go:547` `lookupPane` | adapter snapshot; `Exists:false` now derives from authoritative membership — pane vanishes at removal, so stale tool events clear at least as fast |
| CWD resolver | `runtime.go:648` `SessionCWD` | adapter snapshot |
| Shell-name watcher | `runtime.go:683` | adapter snapshot |
| Peer relative-file resolution | `pkg/peer/session_stream.go:157` | adapter `List()` (unchanged signature) |
| Schedule concurrency cap | `pkg/server/routes_common.go:267` | `Options.DaemonReg.List()` (adapter) |
| Stats pane enumeration | `pkg/server/routes_sessions.go:711` | same |

## Server (`pkg/server`)

- `Options.DaemonReg` (`options.go:73`) changes from `*pty.Registry` to a
  server-defined interface implemented by `daemonAdapter`:
  `Create/Kill/Capture/CaptureTail/SocketPath/List`. All route uses compile
  against it; crash-route methods are gone.
- Delete crash endpoints (`routes_sessions.go:520-585`).
- `Options.RefreshSessions func()` (`options.go:52`) is deleted. Its three
  callers:
  - rename (`routes_sessions.go:419`): replace with
    `opts.StateMgr` broadcasting `sessions-changed` from `SetDisplayName`
    (or an explicit `NotifySessionsChanged` option) — membership untouched.
  - recover path (`:553`): deleted with crash routes.
  - terminal disconnect (`:1356`): delete the call — a killed daemon's
    removal is handled by the watch; a live daemon is a no-op today anyway.
- Launch service (`pkg/sessionlaunch/service.go`):
  `DaemonRegistry.Create` returns `(pty.SessionInfo, error)`; replace
  `Refresh RefreshFunc` with `OnLaunched func(pty.SessionInfo)`; `createLocal`
  calls it with the returned info. Kill path unchanged.
- WS attach (`routes_sessions.go:1322`) and peer layer: untouched (R9).

## CLI (`pkg/commands/sessiondaemon/sessiondaemon.go`)

- `session create` (`:64-76`): first try `POST` to the local server's launch
  API (reuse the existing local HTTP client used by other commands; server
  address discovery as done by `guppi notify`). Success → done, session is
  tracked+watched. Connection refused OR server socket absent (confirmed server
  absence): check socket directory lock; if held (server booting), wait/retry
  API path; if free, acquire lock, spawn directly via `reg.Create(...)` (new
  signature), print: "server not running; session will appear after next server
  start." Reachable server returning 5xx or timeout: surface error to user,
  never spawn directly. No daemon-to-server registration channel — rejected as
  new protocol surface; boot adoption covers it.
- `session list` (`:79-105`): try `GET /api/sessions`; fallback
  `reg.Scan()` (read-only) when server down. JSON flag preserved.
- `session kill`/`capture`: unchanged (socket-direct still works; kill causes
  watch EOF → removal).

## Frontend (`web/src`)

- `hooks/useSessions.ts`: delete the 5s polling interval and
  `!connection.live` visibility fallback. Snapshot fetches driven by WS
  (re)connect mark the dispatched `sessions/snapshot` action
  `authoritative: true` only on HTTP success; a failed fetch dispatches
  nothing (liveness stays unknown).
- `state/workspaceReducer.ts`:
  - `sessions/snapshot` with `authoritative: true`: clear `livenessUnknown`
    (even for an empty payload — empty successful snapshot means zero local
    sessions) and run the reconciliation formerly in `view/pruneMissing`
    (`:549-610`) immediately, no debounce: remove missing local session keys
    from the pane tree, repair `activeKey` and `singleView`, drop persisted
    group members for removed keys.
  - Delete `view/pruneMissing` action type (`:80`), case (`:549`), debounce
    bookkeeping; delete creator (`useWorkspace.ts:183-184`). Move — do not
    delete — the pane-repair helpers into the snapshot case (extract shared
    function).
- `App.tsx:678-690`: delete the prune effect. `terminalPool.disposeAbsent`
  moves to an effect keyed on the reconciled snapshot state (fires after
  authoritative snapshots and `session-removed`).
- `pruningSuspended` (`App.tsx:668`): its pane-prune job dies with the prune
  effect, but its second job — protecting sidebar project filters
  (`Sidebar.tsx:363-369`) — is preserved: rename the prop to
  `filterProtectionActive`, computed from `connection.livenessUnknown ||
  recovering`-equivalent (recovering state goes away with crash UI; use
  `livenessUnknown` alone). Filters are never auto-erased while liveness
  unknown (R8).
- Delete crash-recovery UI: `components/RecoveryPanel.tsx`,
  `hooks/useCrashedSessions.ts` (including its 10s API polling), the
  `useCrashedSessions()` call (`App.tsx:178`), and the dead
  `recovery-started` / `recovery-finished` / `sessions-crashed` event
  handlers (`App.tsx:633-660`).
- New: render sessions with `unreachable: true` using the existing
  disconnected/offline visual treatment (no new UI concept; session stays).

## Event semantics

- `session-removed`: exactly once per instance (presence-gated broadcast).
- `sessions-changed`: field updates, unreachable transitions, rename.
- Snapshot on reconnect is authoritative; since the server map is the only
  source, resurrection is structurally impossible (R7).

## Alternatives rejected

1. `FrameWatch` verb suppressing fan-out — protocol fork, legacy problem;
   deferred.
2. Remove-on-first-EOF (rev 1) — violates R6 for live PIDs; replaced by
   PID-verified removal + unreachable/backoff.
3. Daemon-to-server registration for CLI direct create — new protocol
   surface; server-API routing + boot adoption is simpler and correct.
4. Keeping lifecycle files for crash display — recreates the bug class.
5. Option A — rejected: live-shell survival is crucial.

## Risks

- Unreachable-but-alive daemon lingers in UI until reconnect or user kill —
  intentional trade (never drop a live PID); disconnected-overlay protection
  retained (R19).
- `Create` now blocks ≤2s waiting for sidecar (including nonce write) —
  matches existing attach retry tolerance; launch API latency unchanged in
  practice.
- 1s removal bound is best-effort under load — documented (R1).
- FD exhaustion (watcher or enrichment tick) leaves sessions unreachable
  until pressure clears — no removal (R6); recovery automatic (T21).
- Test fallout: registry/lifecycle/state-guard tests deleted/rewritten;
  failure-injection suite added (tasks Phase 6).
