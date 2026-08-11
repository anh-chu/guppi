# Session Architecture Simplification (rev 3)

**Status:** proposed
**Location:** `specs/changes/session-architecture-simplification/proposal.md`

## Problem

guppi tracks session existence in three places, reconciled asynchronously:

1. **Daemon sockets** — per-session detached daemon processes with unix sockets under a socket dir, discovered by periodic scan (`pkg/pty/registry.go`, 974 LOC).
2. **Lifecycle record files** — durable JSON records with states `active` / `cleanly_ended` / `crashed` / `recovered` / `dismissed` (`pkg/pty/lifecycle.go`, 261 LOC; written by `pkg/pty/daemon.go`, 705 LOC).
3. **In-memory state manager** — diffs discovery snapshots against previous state every 2s (`pkg/commands/server/runtime.go:498` `runDaemonRefresh`; `pkg/state/sessions.go:18` `UpdateSessions`).

Because discovery can transiently fail (socket dir unreadable, race between exit and scan), the state manager grew **mass-removal safety guards** (`pkg/state/sessions.go:36-113`): "all sessions vanished" skip logic, ">50% removal" quarantine, per-session `IsSessionDead` confirmation via lifecycle files. The frontend then mirrors this state a fourth time, with its own defenses: 5s polling fallback (`web/src/hooks/useSessions.ts:150-160`), prune debouncing requiring two observations 1s apart (`web/src/state/workspaceReducer.ts:549` `view/pruneMissing`), and "never prune while disconnected" rules (`web/src/App.tsx:678`).

Result: a recurring bug class — sessions not removed on shell exit, removed sessions reappearing, duplicated sidebar groups. Each fix adds another guard, which adds another state to reconcile.

## Constraint discovered: remote hosts

guppi is multi-host, but **not** via SSH-from-local. Remote sessions belong to remote guppi peers (`pkg/peer/manager.go` `HostState`; `pkg/model/types.go:9-11` `Host`/`HostName` fields; frontend `sessionKey` = `host/name` in `web/src/hooks/useSessions.ts:56`). Each peer runs its own server and owns its own local PTYs; the peer layer streams terminal I/O and mirrors state over WebSocket. Therefore **both options below apply per-host to local sessions only** — neither breaks remote sessions. One caveat: the peer streaming path reaches PTYs via `DaemonReg.SocketPath(name)` + unix dial (`pkg/peer/session_stream.go:116`, `pkg/peer/session.go:72-76` interface), and the local WS attach proxy does the same (`pkg/server/routes_sessions.go:1322`). Option A must replace this socket-path contract with an in-process attach interface (`io.ReadWriteCloser`-style); Option B keeps it unchanged.

## Current inventory (non-test LOC, rounded)

| Component | Files | LOC |
|---|---|---|
| PTY daemon process | `pkg/pty/daemon.go`, `daemon_client.go` | ~975 |
| Registry / discovery / crash detection | `pkg/pty/registry.go` | ~975 |
| Lifecycle store | `pkg/pty/lifecycle.go` | ~260 |
| State reconciliation + guards | `pkg/state/sessions.go` (guards ~100), `manager.go` | ~670 |
| Discovery loop + adapter | `pkg/commands/server/runtime.go` (portions) | ~200 |
| Crash-recovery routes (recover/dismiss/crashed) | `pkg/server/routes_sessions.go:520-585` | ~70 |
| Recovery manifest (already exists) | `pkg/recovery/manifest.go` | ~140 |
| Frontend polling fallback + prune debounce | `useSessions.ts`, `workspaceReducer.ts` prune case, `App.tsx` prune effect | ~120 |

## Option A — In-process PTYs (localterm model)

### Outcome
One server process owns all local PTYs directly (`creack/pty` already a transitive capability; daemon.go uses it). One in-memory map is the sole source of truth. PTY exit triggers synchronous teardown + `session-removed` broadcast. Discovery, lifecycle files, guards, polling fallback, prune debounce: deleted. Server restart kills live shells; a workspace manifest (extend existing `pkg/recovery/manifest.go`) respawns fresh shells with same name/cwd/shell/layout on startup.

### Smallest viable cut
1. New `pkg/pty/inproc.go`: `Manager` with `map[string]*Session`, `Create/Kill/Resize/Write/Attach/Capture/List`; ring buffer reused from daemon code; `cmd.Wait()` goroutine per session does synchronous removal + callback.
2. New attach interface replacing `SocketPath` in `pkg/peer/session.go` and `pkg/server/routes_sessions.go:1322` (WS proxy becomes direct pipe).
3. State manager: delete `UpdateSessions` diffing + guards; sessions added/removed only by explicit calls from PTY manager callbacks. Metadata (`m.meta`, naming, previews) stays.
4. Startup: read manifest, respawn shells marked as previously-live.
5. Frontend: delete 5s polling interval, `view/pruneMissing` action + debounce state, `App.tsx` prune effect. Keep refetch-on-WS-reconnect (already primary path in `useSessions.ts`).

### Deleted
- `pkg/pty/daemon.go`, `daemon_client.go`, `lifecycle.go`, most of `registry.go` (~2,400 LOC + ~1,500 test LOC)
- Guards in `pkg/state/sessions.go` (~120 LOC), `runDaemonRefresh` loop, `daemonAdapter` (~250 LOC)
- Crash recover/dismiss routes + frontend crash-recovery UI
- systemd scope spawning
- Frontend: polling fallback, prune debounce (~120 LOC)

### Added
- `pkg/pty/inproc.go` ~400-500 LOC (much reused from daemon.go: ring buffer, resize, capture)
- Manifest respawn-on-start ~100 LOC (manifest format exists)
- Attach interface refactor in peer + server ~100 LOC changed

### Non-goals
Remote host protocol changes; session naming/metadata; scheduler; group sync.

### Migration / compat risks
- **Live shells die on server upgrade/restart/crash.** Biggest behavior change. Respawn restores cwd/shell/name but not running processes (agents, dev servers, ssh sessions inside shells).
- Existing daemon sessions at upgrade time: need one-shot adoption (kill + respawn) or orphan cleanup.
- `guppi notify` and any CLI that dials session sockets directly (`pkg/commands/notify/notify.go:477`) needs new path (HTTP endpoint).
- Server crash = all shells gone; today daemons survive.

### User-visible changes
- Shell exit removes session from sidebar instantly, always.
- No "disconnected — reconnecting" ghost sessions.
- No crash-recovery banner (concept deleted).
- Restarting guppi restarts your shells (fresh, same cwd).

### Acceptance
- GIVEN a session running `bash`, WHEN the user types `exit`, THEN the session disappears from every connected client's sidebar within 1s with no polling involved.
- GIVEN 10 sessions, WHEN the server is restarted, THEN 10 fresh shells appear with the same names, cwds, and sidebar layout, and each terminal shows a fresh prompt.
- GIVEN a session killed via UI, WHEN the server restarts, THEN that session is not respawned.
- GIVEN a remote peer session in the sidebar, WHEN the local architecture change ships, THEN remote attach/stream/kill behave identically.

### Effort
~2-3 weeks. Large deletion, moderate new code, but touches peer interface, server routes, CLI notify path, frontend; needs careful upgrade story.

## Option B — Keep daemons, gut reconciliation (rev 3)

### Outcome
Daemons stay (shells survive server restarts). But the server's in-memory list becomes the **sole authority**: a session exists because the server created it (or adopted it at startup) and dies only when its daemon PID is confirmed dead (via watch EOF + PID-liveness check). Detection is **event-driven**: the server holds one persistent control connection per daemon. **Watch EOF is a _symptom_, not the _decision_**: EOF triggers a PID-liveness check (matching PID + nonce or legacy start-time); only PID-confirmed death removes the session. If the PID is still alive, the session becomes "unreachable" and the watcher reconnects with backoff indefinitely. Delete: periodic 2s scan-and-diff, lifecycle files, mass-removal guards, `IsSessionDead`, crash detection, frontend polling fallback and prune debounce.

Server in-memory session list becomes the single authority. Sessions are added when launched and removed when the daemon PID is confirmed dead (EOF + PID check), detected by an event: the server holds one persistent watch connection per daemon. On boot the server does a one-time adoption scan of existing daemon sockets, then never scans again. Lifecycle files, mass-removal guards, and the 2s discovery diff loop are deleted.

1. Sessions removed only when daemon PID is confirmed dead (watch EOF triggers PID-liveness check; if PID alive, session becomes unreachable and retries; removal synchronized to verified death, not just EOF).
2. Sessions added only when launched or adopted at startup (no transient false removals).
3. Lifecycle files deleted (no crash records to reconcile against).
4. Front-end prune debounce, polling fallback, "never prune while disconnected" deleted.

### Smallest viable cut
1. Registry keeps `Create/Kill/Capture/SocketPath` but adds `Watch(inst, onExit, onUnreachable)`: persistent unix connection per daemon; goroutine blocks on read; EOF triggers PID-liveness check (nonce or legacy start-time identity verification); if PID dead → `onExit` once; if PID alive → `onUnreachable(true)` + backoff reconnect.
2. Startup adoption: one directory scan at boot (only time the filesystem is consulted) to adopt surviving daemons into the map + open watch connections.
3. Delete `runDaemonRefresh` ticker; `UpdateSessions` diff/guards replaced by explicit `AddSession`/`RemoveSession` (latter already exists, `pkg/state/sessions.go:497`).
4. Delete `lifecycle.go` + all `LifecycleStore` wiring; daemon stops writing records; crash recovery routes deleted (a crashed daemon appears as EOF, which triggers PID-death detection; if confirmed dead, session is removed like a normal exit; manifest respawn from `pkg/recovery` can offer restore if desired, but non-goal for viable cut).
5. Frontend: same deletions as Option A (polling interval, `pruneMissing`, prune effect); refetch-on-reconnect stays.
6. Details that discovery used to refresh (live cwd via `/proc`, previews — `pkg/state/sessions.go:166-230`) move to a lightweight enrichment tick that only *updates fields* on known sessions, never adds/removes.

### Deleted
- `pkg/pty/lifecycle.go` (~260 LOC) and lifecycle writes in `daemon.go` (~80 LOC)
- Registry scanning/liveness/failCount/crash-detection/recover/dismiss (~450 of 975 LOC in `registry.go`)
- `UpdateSessions` guards + diff (~180 LOC), `runDaemonRefresh` (~60 LOC)
- Crash recover/dismiss routes + UI
- Frontend polling fallback + prune debounce (~120 LOC)

### Added
- Watch connection + onExit plumbing ~150 LOC
- Boot adoption scan ~60 LOC
- Enrichment tick (fields only) ~60 LOC

### Non-goals
Removing daemons; changing attach socket protocol; peer interface changes (none needed — `SocketPath` contract untouched).

### Migration / compat risks
- Watch connection needs a daemon-side accept path that stays open silently — verify daemon's socket handler tolerates an idle client (or add a `watch` verb to daemon protocol; small daemon change, but daemons are per-user binaries respawned by the same executable, so old daemons at upgrade time may lack the verb — fall back to reconnect-probe for adopted legacy daemons, retire after one release).
- Stale lifecycle files on disk from old versions: ignore + clean once at boot.
- A wedged (not exited) daemon that stops responding is no longer auto-detected by failCount; kill still works via UI. Acceptable — matches "sessions never disappear without user action."

### User-visible changes
- Shell exit removes session within ~1s (daemon closes socket on exit, EOF is immediate) instead of up to 2s scan + guard hesitation.
- No ghost/reappearing sessions, no duplicated groups.
- Crash-recovery banner gone: a crashed daemon just disappears (optionally, later: manifest-based "restore session?" affordance).
- Shells still survive server restarts (unchanged).

### Acceptance
- GIVEN a session, WHEN the user types `exit`, THEN `session-removed` broadcasts within 1s and the session never reappears.
- GIVEN a running server, WHEN the socket directory is made temporarily unreadable, THEN no session is removed (nothing scans it).
- GIVEN 5 live daemons, WHEN the server restarts, THEN all 5 sessions reappear with live shells and watch connections established.
- GIVEN a daemon killed with `kill -9`, THEN its session is removed like a normal exit.

### Effort
~1-1.5 weeks. Mostly deletion; new code is one watch mechanism; no peer/CLI/attach protocol changes.

## Recommendation

**Option B.** It eliminates the entire bug class (the class comes from asynchronous reconciliation, not from daemons per se) at roughly half the effort and zero regression risk to the peer layer, CLI notify path, and — critically — preserves live-shell survival across server restarts, which is an explicitly designed property of this codebase (`pkg/pty/daemon.go:62`: daemons deliberately become session leaders "so we survive parent/server restarts") and is a real workflow here: long-running agents (claude, etc.) inside sessions must not die because the guppi binary was upgraded.

Option A is architecturally cleaner and deletes ~3x more code, but pays for it with the restart-kills-shells regression plus a peer/attach interface refactor. If the owner decides live-shell survival does not matter, Option A becomes the better long-term end state — and Option B is not wasted work on that path, since B's event-driven single-authority state model is exactly what A needs too; A then just swaps the daemon+watch backend for in-process PTYs.

## Superseded notes (rev 3)

**Watch EOF does not imply removal.** In earlier revisions, EOF on the watch connection was treated as session-dead. Rev 3 clarifies: EOF is a signal to check liveness; the PID-liveness check (process still alive + identity match: nonce or legacy start-time) decides removal. If the PID is alive, the session is "unreachable" and remains in the session list (protected by the disconnected-overlay visual treatment). Removal occurs only when the PID is confirmed dead.

**Terminal disconnected-overlay protection retained.** For live-but-unreachable daemons (transport loss, socket dir unreadable, FD exhaustion), the frontend renders the existing "disconnected" visual treatment via the new `unreachable` state, preserving the false-disconnect protection: users are not surprised by a vanished session that is still running.

## Key decision for the owner

**How much does live-shell survival across guppi restarts matter?**
- Matters (agents/dev-servers running inside sessions must survive `guppi` upgrade or crash): choose B.
- Doesn't matter (fresh shell at same cwd is fine): A is viable, but note the extra scope: peer `SocketPath` interface, WS attach proxy, `guppi notify` socket dial all change.

Remote-host note: no constraint blocks either option — remote sessions are peer-owned, not locally-spawned SSH PTYs, so neither option touches remote session mechanics. But the change must ship to every peer independently; mixed-version peer meshes work because the peer wire protocol is unchanged in both options.

## Critical files

- /home/sil/guppi/pkg/pty/registry.go - discovery/liveness/crash logic to gut or delete
- /home/sil/guppi/pkg/state/sessions.go - UpdateSessions diff + mass-removal guards to replace with explicit add/remove
- /home/sil/guppi/pkg/commands/server/runtime.go - 2s refresh loop, daemonAdapter wiring, lifecycle store setup
- /home/sil/guppi/pkg/pty/daemon.go - lifecycle writes, exit path, source for in-process PTY reuse (Option A)
- /home/sil/guppi/web/src/state/workspaceReducer.ts - pruneMissing debounce deletion (plus useSessions.ts polling fallback)
