# Tasks — Session Architecture Simplification (Option B, rev 3)

Ordered: additive first, cutover, backend deletions, frontend, docs/E2E,
failure-injection. Run `go test ./...` + `pnpm test` (web/) after each task.
Phase gates: do not start a phase until the previous phase's tasks are checked.

## Phase 1 — Additive: identity, watch, adoption, state APIs

- [ ] **T1. `pty.Instance` + `Create` returns `SessionInfo`.**
      `pkg/pty/registry.go`: add `Instance{Name,Pid,Nonce,ProcStartTime,SystemdUnit}`
      (nonce: 8-byte hex random written by daemon at spawn; ProcStartTime:
      /proc/<pid>/stat field 22 for legacy daemons; SystemdUnit: systemd scope
      name, captured immutably at spawn/adoption). Change
      `Create(...) (SessionInfo, error)` — after spawn, daemon writes nonce
      to sidecar JSON, poll sidecar up to 2s, return populated info including
      SystemdUnit. Update all callers to compile (`sessionlaunch.DaemonRegistry`,
      `registryView`, `daemonAdapter`, `sessiondaemon.go:69`) — behavior unchanged
      for now. Verify: unit test — Create returns info with live PID, nonce, and
      SystemdUnit.
- [ ] **T2. `Registry.Watch(inst, onExit, onUnreachable) (stop, error)`.**
      Idle-client connection; on read error: `processAlive(inst.Pid)` with
      identity match (nonce or legacy ProcStartTime from /proc) = dead → onExit
      once; alive → onUnreachable(true) + backoff redial (250ms..5s cap,
      forever); on redial success re-read sidecar, mismatch with live PID →
      keep retrying indefinitely (session unreachable); only onExit when PID
      confirmed dead. Initial dial retry ≤2s. `stop()` = synchronous
      stop-and-wait, suppresses all callbacks (incl. in-flight). Verify: unit
      tests — daemon Kill → onExit <1s; SIGKILL → onExit; forced conn close
      with PID alive → onUnreachable then recovery, no onExit; sidecar
      mismatch with live PID → unreachable indefinitely, onExit only at PID
      death; stop() → goroutine exited, zero callbacks.
- [ ] **T3. `Registry.Scan()` (read-only) + `Adopt()` (scan + PID-dead
      stale-file cleanup).** Alive-PID sockets always adopted even if probe
      dial fails. PID-dead leftovers: re-verify sidecar identity (nonce or
      legacy ProcStartTime) before deleting .sock/.json. No failCount.
      Verify: unit test — live daemon adopted; dead leftover files removed
      after identity check; alive-PID/unresponsive-socket still returned by
      Adopt.
- [ ] **T4. State manager APIs + socket directory lock** in `pkg/state` and
      `pkg/pty`:
      `AddSession` (broadcast session-added/sessions-changed);
      `RemoveSession` presence-gated broadcast (returns bool; double call →
      one event); `UpdateSessionFields`; `Unreachable(name,bool)` +
      `Unreachable` field on `model.Session` serialized to clients. Lock file
      (flock) on socket directory: server acquires during adoption (T6), CLI
      fallback checks lock presence (T11).
      Verify: unit tests for exactly-once removal event and field-update
      broadcasts; lock coordination prevents direct-create race.

## Phase 2 — Cutover: runtime authority + launch API

- [ ] **T5. Runtime tracked-session map.** `runtime.go`: `sessionsMu`,
      `tracked map[string]trackedSession{inst,stop}`; `onLaunched(info)`: under
      `sessionsMu` detach old entry if exists and swap map entry (duplicate-name
      handling), release `sessionsMu`, call `stop()` on old watcher (synchronous
      stop-and-wait after mutex released), re-acquire `sessionsMu` to build
      `model.Session`, `stateMgr.AddSession`, `reg.Watch()`, store new entry,
      then release mutex. `onExit(inst)` (instance-matched removal + scope/file
      cleanup), `onUnreachable`. `Runtime.Stop()` (lock ordering): release adoption
      lock, detach and mark all watchers under `sessionsMu`, release mutex, call
      each `stop()` (waits for goroutine after mutex released). Callback
      suppression covers in-flight callbacks. Keep `runDaemonRefresh` running
      (removals become no-op diffs).
      Verify: integration — launch, exit, kill; instance mismatch test:
      simulate stale onExit(oldInst) after re-create → new session untouched;
      deadlock/race test: concurrent Runtime.Stop() with active watchers.
- [ ] **T6. Atomic boot adoption.** In `Start` before `close(rt.ready)`:
      acquire flock on socket directory, lifecycle-record boot cleanup (R11),
      then `Adopt()` → `onLaunched` per info, synchronously under
      `sessionsMu`, release flock. Lock holds until after adoption completes,
      preventing CLI direct-spawn race (T4/T11).
      Verify: restart test — first `/api/sessions` response contains all
      adopted sessions; shells (e.g. `sleep`) survive; leftover lifecycle
      files deleted. Race test: direct create during adoption window before
      Ready must end tracked exactly once (no duplicates).
- [ ] **T7. Launch service signature.** `pkg/sessionlaunch/service.go`:
      `DaemonRegistry.Create` returns `(pty.SessionInfo, error)`; replace
      `Refresh RefreshFunc` with `OnLaunched func(pty.SessionInfo)`;
      `createLocal` calls it. Wire runtime `onLaunched`. Update scheduler /
      peer-launch construction sites.
      Verify: create via HTTP API → session-added immediately, watcher live;
      no `refreshSessionsFunc` in launch path.
- [ ] **T8. Replace `Options.RefreshSessions` callers.**
      Rename (`routes_sessions.go:419`): broadcast `sessions-changed` from
      `SetDisplayName` (or `NotifySessionsChanged` option). Terminal
      disconnect (`:1356`): delete call + comment. Delete
      `Options.RefreshSessions` (`options.go:52`) once callers are gone
      (crash-route caller at `:553` goes in T13).
      Verify: rename reflects in all clients; tab disconnect leaves live
      session listed.
- [ ] **T9. Enrichment tick (2s) + adapter snapshot rewire.**
      Tick updates per-session: live cwd, pane PID, preview, worktree fields,
      `applyMetadata`, stale-agent cleanup (`sessions.go:233-293`); writes
      `daemonAdapter.snap`; `sessions-changed` only on change. Adapt
      `loadDaemonSessionDetails` to take the session's own info (no
      `reg.List()`). Consumers verified on adapter snapshot: detector
      `listPanes` (`runtime.go:524`), reconciler `lookupPane` (`:547`),
      `SessionCWD` (`:648`), shell-name watcher (`:683`), peer file
      resolution (`session_stream.go:157`), schedule cap
      (`routes_common.go:267`), stats (`routes_sessions.go:711`).
      Verify: unit test — tick never changes count; cwd change reflected ≤2s;
      detector integration test: agent PID appears in `listPanes` within one
      tick (latency acceptance); removed session → `lookupPane` Exists=false
      immediately.
- [ ] **T10. Delete `runDaemonRefresh` + `refreshSessionsFunc` +
      `daemonAdapter.refresh()`** (`runtime.go:434,465-521,594-600`).
      Verify: full server integration — launch/exit/kill events flow;
      `grep -r runDaemonRefresh` empty.
- [ ] **T11. CLI routing.** `sessiondaemon.go`: `create` tries server launch
      API first (T4). On connection refused or server socket absent (confirmed
      server absence), check lock: if held (server booting), wait/retry API
      path; if free, acquire lock, spawn directly. Reachable server returning
      5xx or timeout: surface as user error, never spawn directly. Fallback
      (direct spawn) only on confirmed server absence + acquiring lock; never
      on HTTP errors from a reachable server. `list` tries `GET /api/sessions`,
      falls back to `reg.Scan()` on server down.
      Verify: both paths manually with server up/down; JSON output preserved;
      API 5xx returns error (no fallback spawn); lock coordination prevents race.

## Phase 3 — Delete reconciliation machinery (backend)

- [x] **T12. Delete `UpdateSessions` + guards** (`sessions.go:18-140`);
      shrink `state.DaemonRegistry` (drop `IsSessionDead`,
      `CrashedSessions`, `CrashedSessionInfo`). Update/delete guard tests.
- [x] **T13. Delete crash routes + concrete DaemonReg.**
      Remove `routes_sessions.go:520-585`; change `Options.DaemonReg`
      (`options.go:73`) from `*pty.Registry` to server interface
      (`Create/Kill/Capture/CaptureTail/SocketPath/List`) implemented by
      `daemonAdapter`; pass adapter at construction. Delete crash-route
      `RefreshSessions` caller and the option field (finishes T8).
      Verify: `curl /api/crashed-sessions` → 404; schedule cap + stats routes
      still work off adapter `List()`; `pkg/peer` compiles untouched.
- [x] **T14. Delete registry scanning/crash code.** `List()` scan +
      failCount/grace, `IsSessionDead`, crash APIs, `SetLifecycleStore`,
      `recoveryMu`, crash branch of `removeStale`. Keep
      `SocketPath/Create/Kill/Capture/CaptureTail/Watch/Scan/Adopt` +
      PID/systemd helpers.
      Verify: build + remaining registry tests green.
- [x] **T15. Delete `pkg/pty/lifecycle.go`** + tests; remove lifecycle writes
      from `daemon.go` (step 3b, `lifecycleStore` field, shutdown
      `Transition`); remove lifecycle-store setup from `runtime.go:90-97`
      (boot cleanup from T6 stays permanently).
      Verify: build; daemon exit still detected via watch.

## Phase 4 — Frontend

- [x] **T16. Authoritative snapshot reconciliation.**
      `useSessions.ts`: snapshot dispatches carry `authoritative: true` only
      on successful fetch triggered by WS (re)connect; failed fetch dispatches
      nothing. `workspaceReducer.ts` `sessions/snapshot` (authoritative):
      clear `livenessUnknown` (empty payload included — zero local sessions
      is a valid state) and reconcile immediately — extract pane-repair logic
      from `view/pruneMissing` (`:549-610`) into a shared helper: prune pane
      tree, repair `activeKey`/`singleView`, drop removed keys from persisted
      groups.
      Verify: reducer tests — reconnect snapshot missing a session prunes
      list+panes+groups in one dispatch; empty authoritative snapshot prunes
      all local; failed-fetch path prunes nothing.
- [x] **T17. Delete polling + pruneMissing.** Remove 5s interval and
      visibility fallback (`useSessions.ts:146-172` region); delete
      `view/pruneMissing` type (`workspaceReducer.ts:80`), case (`:549`),
      debounce state; creator (`useWorkspace.ts:183-184`); prune effect
      (`App.tsx:678-690`). Move `terminalPool.disposeAbsent` to an effect
      keyed on reconciled snapshot/removal state. Rename `pruningSuspended`
      to `filterProtectionActive` = `connection.livenessUnknown`; keep
      `Sidebar.tsx:363-369` filter protection intact. Update
      `workspaceReducer.test.ts:64-89` and Sidebar tests.
      Verify: devtools — no periodic `/api/sessions` while WS live; filters
      survive a disconnect/reconnect cycle.
- [x] **T18. Delete crash-recovery UI + dead handlers.**
      `components/RecoveryPanel.tsx`, `hooks/useCrashedSessions.ts` (incl.
      10s polling), `useCrashedSessions()` call (`App.tsx:178`), mount, and
      `recovery-started`/`recovery-finished`/`sessions-crashed` handlers
      (`App.tsx:633-660`); sidebar crashed-count toolbar item.
      Verify: `pnpm build` + tests; `grep -r useCrashedSessions|RecoveryPanel|sessions-crashed` empty.
- [x] **T19. Unreachable rendering.** Render `session.unreachable` with the
      existing disconnected/offline treatment; session stays listed and
      attachable-when-recovered.
      Verify: component test with unreachable flag set.

## Phase 5 — Docs

- [x] **T20. Update `docs/ux-contracts.md`.**
      - Discovery/removal/pruning (~409-415): removal = `session-removed`
        broadcast or absence from an authoritative reconnect snapshot,
        immediate; 1s bounded best-effort removal timing; no polling.
      - New: local session "unreachable" state — transport loss with live
        daemon PID shows disconnected treatment, never removes; terminal
        false-disconnect behavior.
      - Crash-recovery section (~427-433): delete with note.
      - API table (~640-643): delete `/api/crashed-sessions` rows; note the
        intentional external API break.
      - Sidebar toolbar (~141-147): remove crashed count.
      - CLI (~598-602): `session create/list` server-API routing + offline
        fallback and its adoption-at-next-boot limitation.
      - Browser event protocol (~695-711): add `session-added` /
        `session-removed` (exactly-once) as authority signals.
      - Timing/transients (~742-752): new removal SLA, watcher FD/output
        cost, unreachable backoff.
      - Fix verification pointers referencing deleted code.

## Phase 6 — Failure-injection tests + E2E

- [ ] **T21. Failure-injection suite (Go, real daemons where possible):**
      1. Watch conn reset with live PID (kill TCPish proxy / close FD) →
         unreachable, reconnect, never removed.
      2. Dial timeout on fresh Create (daemon delayed) → watcher retries;
         PID alive → no removal.
      3. Output flood (`yes` in shell) → watcher stable, removal still <1s
         on exit.
      4. Shutdown race: `Runtime.Stop()` during active watchers → zero
         removal broadcasts, goroutines joined.
      5. Name reuse: kill A, create A immediately; delayed old-instance
         callback → new A untouched (state, files, scope).
      6. Concurrent adoption/create: launch through API immediately at
         Ready → no duplicate, no lost session.
      7. Duplicate callbacks: eager UI-kill removal + watch EOF → exactly
         one `session-removed`.
      8. FD exhaustion (lower RLIMIT_NOFILE for the server process, hold
         open dummy FDs until socket operations fail): sessions become
         unreachable (watcher/enrichment fail to dial), none removed, verify
         recovery automatic after pressure clears (close dummy FDs, sessions
         reconnect).
- [ ] **T22. Frontend failure tests:** empty authoritative reconnect
      snapshot; failed snapshot fetch (nothing pruned, filters kept);
      pane-tree/terminal-pool/group pruning on reconnect.
- [ ] **T23. Manual E2E** — execute Acceptance summary items 1-14 from
      `requirements.md` (rev 3); record results in this file.
