# Requirements — Session Architecture Simplification (Option B, rev 3)

**Status:** approved (Option B: keep per-session daemons, gut reconciliation)
**Parent:** `proposal.md`
**Revision:** 3 — nonce-based instance identity, PID-reuse guard via legacy
start-time fallback, immutable SystemdUnit tracking, sidecar-mismatch death
detection, lock-ordered watcher lifecycle, CLI fallback gating, FD-exhaustion
handling.

## Scope

Local sessions only. The server's in-memory session list is the sole
authority. Sessions are added when launched through the server (or adopted
once at boot) and removed only when their daemon process is confirmed dead or
the user kills them. No periodic scanning, no lifecycle files, no
mass-removal guards, no frontend polling fallback or prune debounce. Live
shells — including running agents — must survive server restarts and must
never be killed or dropped because of transport ambiguity.

Out of scope: peer wire protocol, session naming, groups, scheduler, Option A,
any new user-facing features beyond an "unreachable" indicator required for
safety.

## Requirements

### R1 — Shell exit removes the session promptly (attached)

GIVEN a local session running an interactive shell with a terminal attached
WHEN the user types `exit`
THEN the server broadcasts exactly one `session-removed` for that session
instance, within 1 second under normal operating load (best-effort bound;
kernel EOF delivery is immediate, callback scheduling may exceed 1s only
under CPU starvation)
AND the session disappears from every connected client without polling
AND the session never reappears in a later snapshot or broadcast.

### R2 — Shell exit removes the session promptly (unattached)

GIVEN a local session with no terminal attached (no WS client, tab closed)
WHEN the shell process exits on its own
THEN the server detects watch-connection EOF, confirms the daemon PID is
dead, and broadcasts `session-removed` within the R1 bound
AND no client interaction is required for removal.

### R3 — Kill via UI unchanged

GIVEN a local session visible in the sidebar
WHEN the user kills it via the UI
THEN the existing kill path (FrameClose over the daemon socket) executes as today
AND `session-removed` is broadcast exactly once for that instance
AND metadata (name, preview) is dropped so a future session reusing the name
inherits nothing (persistent attrs/order/group behavior for reused names is
unchanged from today's documented contract).

### R4 — Daemon killed with `kill -9` is removed like a normal exit

GIVEN a local session whose daemon is SIGKILLed externally
WHEN the daemon dies
THEN the watch connection errors, the PID-dead check confirms death, the
session is removed and `session-removed` broadcast within the R1 bound
AND no crash-recovery banner, record, or endpoint is involved (concept deleted).

### R5 — Server restart adopts live daemons; shells survive; boot is atomic

GIVEN N live session daemons and a running server
WHEN the server is stopped and restarted
THEN no shell process is killed or respawned
AND at boot the server performs exactly one scan of the socket directory and
adopts all N daemons — recording each as an instance {name, daemonPID,
nonce} (or legacy {name, daemonPID, procStartTime} if nonce not present) — and
opens one watch connection per instance
AND adoption completes before `Ready()` closes and before the first API
response is served, so the first `/api/sessions` snapshot contains all N
AND after boot the socket directory is never scanned again for membership
decisions for the lifetime of the process
AND launches racing adoption are impossible: server-mediated launches are
serialized with adoption behind the same state authority and cannot execute
before Ready.

### R6 — Transient failures never remove sessions; live PIDs are sacred

GIVEN a running server with sessions tracked in memory
WHEN the socket directory becomes unreadable, a dial times out, a watch
connection resets, or any other transport-level failure occurs
THEN no session whose daemon PID (matched by PID + start-time for legacy
daemons) is still alive is ever removed from state, broadcast as removed,
killed, or file/scope-cleaned
AND a session whose watch connection is lost while its PID is alive enters an
"unreachable" state and the server reconnects with bounded exponential
backoff (see design) indefinitely
AND removal occurs only when a PID-liveness check confirms the daemon process
is dead, or the user explicitly kills the session.
Sessions with running agents must never be dropped by transport ambiguity.

### R7 — Removed sessions never resurrect; no ABA on name reuse

GIVEN a session instance that was removed (exit, kill, or daemon death)
WHEN a new session is created reusing the same name, or a client reconnects,
refreshes, or fetches a snapshot
THEN the removed instance does not reappear
AND no callback, watcher, or cleanup belonging to the old instance ever
removes, stops, or cleans files/systemd scopes of the new instance (instance
identity = nonce + daemonPID for new daemons, or daemonPID + procStartTime
for legacy daemons; match required for any destructive action).

### R8 — Frontend reconciles solely from events and authoritative snapshots

GIVEN the frontend running against the new server
THEN there is no periodic polling interval as a liveness fallback
AND there is no `view/pruneMissing` debounce requiring repeated observations
AND WHEN the events WebSocket (re)connects and the snapshot fetch succeeds,
THEN the client applies that snapshot as authoritative immediately: local
sessions absent from it are pruned from the session list, pane tree,
`singleView`, `activeKey`, terminal pool, and persisted groups, with the
pane-repair behavior currently in `view/pruneMissing` preserved
AND a successful empty snapshot is applied like any other (all local sessions
pruned); a failed snapshot fetch leaves liveness unknown and prunes nothing
AND sidebar project filters are never auto-erased while liveness is unknown
(the protection currently keyed off `pruningSuspended` is preserved).

### R9 — Remote peer sessions unaffected

GIVEN sessions belonging to remote guppi peers
WHEN this change ships
THEN remote session attach, streaming, kill, status, and peer relative-file
resolution behave identically
AND the `SocketPath` + unix-dial contract used by the peer stream path and
local WS attach proxy is unchanged.

### R10 — Legacy daemons adopted at upgrade

GIVEN daemons spawned by a previous guppi version
WHEN the upgraded server boots
THEN they are adopted like any other (idle-client watching uses the base
protocol every shipped daemon speaks)
AND attach, kill, and capture on them work unchanged.

### R11 — Stale lifecycle files cleaned at boot

GIVEN lifecycle record files on disk from a previous version
WHEN the upgraded server boots
THEN those files are deleted once at boot (cleanup runs every boot; old
daemons may keep writing files until they die)
AND they never influence add/remove decisions
AND crashed-session records produce no recovery banner (routes and UI deleted).

### R12 — Enrichment tick updates fields only, with parity

GIVEN tracked sessions displayed in the UI
WHEN the periodic enrichment tick runs
THEN it updates only fields on sessions already in state: live cwd via
`/proc/<pid>/cwd`, prompt preview, worktree detection/parent resolution,
`applyMetadata`, stale-agent metadata cleanup, and the adapter snapshot
consumed by the detector/reconciler/CWD resolver/shell-name watcher/peer
listing/stats/schedule cap
AND it never adds or removes a session and never broadcasts `session-removed`
AND a failure inside the tick leaves membership untouched
AND agent process detection and reconciliation latency does not regress
beyond today's bounds (snapshot age ≤ the current 2s refresh cadence).

### R13 — Launch API returns the launched instance

GIVEN a session launched via the launch service (HTTP API, scheduler, or peer)
WHEN the daemon has been spawned
THEN the registry returns the concrete `SessionInfo` (PID, created, cwd,
shell) for the new instance
AND the runtime adds exactly that instance to state and starts its watcher —
no zero-argument refresh, no re-scan.

### R14 — CLI paths remain correct

GIVEN the `termyard session create` and `termyard session list` CLI commands
WHEN the local server is running
THEN `create` routes through the server API (session becomes visible and
watched immediately) and `list` is served from the server API
AND WHEN the server is not running, `create` spawns a daemon directly (it
will be adopted at next server boot; this limitation is printed and
documented) and `list` falls back to a one-shot read-only socket-dir scan
AND `session kill` / `capture` behavior is unchanged.

### R15 — Exactly-once events and clean shutdown

GIVEN the server broadcasting session events
THEN `session-removed` is emitted exactly once per removed instance (eager
UI-kill removal and later watch-EOF confirmation do not double-broadcast)
AND on `Runtime.Stop()` all watchers are stopped synchronously — detached,
marked under mutex, mutex released, then goroutines awaited — before teardown,
so server shutdown never broadcasts removals or cleans scopes/files of live
daemons; callback suppression covers callbacks already in flight.

## Acceptance summary (manual E2E)

1. `exit` in attached session → gone < 1s, never returns.
2. `exit` in unattached session → gone < 1s.
3. UI kill → gone, exactly one `session-removed`, daemon + process group dead.
4. `kill -9 <daemon pid>` → gone < 1s.
5. 5 sessions with `sleep 999`; restart server → 5 back before first API
   response, sleeps alive.
6. Sever a watch connection (socat proxy or `gdb`-close FD) while daemon PID
   alive → session shows unreachable, is NOT removed, recovers on reconnect.
7. Kill session A, immediately create new "A" → old callbacks never touch new A.
8. `chmod 000` socket dir 30s → zero removals.
9. Remove session; switch groups, reload, toggle WS → no resurrection, no
   duplicate groups, pane tree/terminal pool cleaned.
10. Remote peer session attach/kill/file-open → unchanged.
11. Pre-upgrade daemon at upgrade → adopted, exit detected.
12. Stale lifecycle files at boot → cleaned.
13. Server up: `termyard session create/list` via API; server down: direct
    spawn + scan fallback works, adoption picks daemon up at next boot.
14. WS reconnect with empty successful snapshot → all local sessions pruned
    (list, panes, pool, groups); failed fetch → nothing pruned, filters intact.
