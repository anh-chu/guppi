# v2 Direct Cutover Plan

This document governs Task 15: the irreversible switch from legacy
per-field state (`state.Manager`, legacy peer sync messages, `AppLegacy`)
to the v2 owner-catalog/workspace redesign as the *only* runtime path, and
the subsequent deletion of the legacy code it replaces.

It assumes the reader has read `pkg/state/INVARIANTS.md` (the frozen v2
identity/state contracts) and `docs/v2-baseline.md` (pre-redesign
performance baselines from Task 1). It does not restate those; it defines
what must be true before cutover, in what order cutover happens, what
counts as proof, what is then safe to delete, and what happens if it goes
wrong.

The redesign this plan closes out spans 18+ commits (`git log --oneline
3902bf9..HEAD`): the crash-safe v2 store and owner catalog (Tasks 1-9), the
durable browser bootstrap/stream and typed command routes (Task 10), the
normalized browser projections and command client (Tasks 11-12), the
`AppV2` UI cutover (Tasks 13-14), and four rounds of external review that
fixed identity, peer-trust, shadow-mode, and wire-format bugs found after
the fact. This plan exists so a fifth round isn't needed after Task 15,
because Task 15 cannot be undone by re-enabling a flag.

## 1. Preflight checklist

All of the following must be checked off before `TERMYARD_V2_STATE=1` is
flipped in production and before Task 15's deletion work starts. Nothing
here is optional; an unchecked item is a blocked cutover, not a judgment
call.

### 1.1 Tests that must pass, right before cutover

- [ ] `go build ./...` and `go vet ./...` clean on the exact commit being
      cut over.
- [ ] `go test ./pkg/state/... ./pkg/server/... ./pkg/ws/... ./pkg/peer/... ./pkg/commands/server/... -race`
      all green. These are the packages that own v2 identity, catalog,
      command routing, peer sync, and the runtime that wires them
      together.
- [ ] `go test ./... -race` green repo-wide (the only historically
      accepted exemption is the pre-existing `pkg/wikilite`
      `TestSupervisorStatusFresh` flake, which is unrelated to this
      redesign and must be re-verified as still-unrelated at cutover
      time, not assumed).
- [ ] `web`: `npm run typecheck`, `npm test` (Vitest unit/component
      suite), `npm run build` all green.
- [ ] `web`: `npm run test:e2e` green, INCLUDING `e2e/multi-node.spec.ts`.
      As of Task 15 Task 2, this file drives a REAL two-(and
      three-)process harness (`e2e/fixtures/termyardCluster.ts`) -- there is
      no remaining `test.skip`/`test.fixme` in this file. Its real create-
      dependent cases (attach/label/kill/offline-reconnect/restart-routing/
      remote-create-via-UI) currently FAIL because they surfaced two real,
      reproducible production defects (not harness bugs): (1)
      `pkg/state/remote_create.go`'s `RemoteCreateRequest.Target` is a
      non-pointer `SessionRef` whose `omitempty` tag Go's `encoding/json`
      never honors for struct types, so a normal (non-split) cross-node
      remote create always serializes an invalid zero-value target and is
      rejected by `SessionRef.UnmarshalJSON` on the receiving peer; (2)
      `pkg/pty/registry_stable.go`'s `Start()` passes `--daemon-key`,
      `--owner`, `--session-id`, `--generation`, `--command-id` flags to the
      `session-daemon` subcommand, but `pkg/commands/sessiondaemon/sessiondaemon.go`
      never defines those flags, so the daemon child process fatals
      immediately on every v2 stable-binding session start (local or
      remote) and the create silently times out/retries/drops. Cutover must
      not proceed until both are fixed and this file is fully green.
- [ ] `TestV2CommandBodyAsBrowserWouldProduceIt` and the shared
      `testdata/session_ref_fixtures.json` golden-fixture tests
      (`pkg/state/ids_test.go`'s `TestSessionRefGoldenFixture` and
      `web/src/state/v2/wireCodec.test.ts`) pass, proving Go and browser
      agree on the SessionRef wire string form.
- [ ] The v2 baseline benchmarks in `docs/v2-baseline.md` have been
      re-measured on the cutover commit and do not regress beyond a
      reasonable margin (no fixed number is prescribed here since v1
      baselines predate the redesign entirely; a regression judgment call
      belongs to whoever signs off cutover, not this document -- but the
      numbers must be re-measured and recorded, not skipped).

### 1.2 Invariants that must hold (verified, not assumed)

Every rule in `pkg/state/INVARIANTS.md` is a precondition, in particular:

- [ ] No canonical identity field (`OwnerID`, `SessionID`, `LayoutID`,
      `SplitID`, `CommandID`, `SessionRef`) is ever derived from a mutable
      display label. (Regression class already fixed once --
      see the commit "Fix mutable-label identity confusion in AppV2
      session keys".)
- [ ] Every `AppDocument` is schema 2, single-owner, and session membership
      is exclusive across layouts (`CheckSessionMembershipAcrossLayouts`).
- [ ] `AppDocument.Revision` / `WorkspaceRecord.Revision` /
      `LayoutRecord.Revision` are the only source of ordering; no
      wall-clock LWW field exists on any canonical type.
- [ ] `BrowserWorkspaceSnapshot`'s (browser store's) generation/revision
      acceptance rule holds: same connection generation rejects `revision
      <= stored`; a new connection generation always accepts, regardless
      of numeric revision. Covered by `web/src/state/v2/store.test.ts`.

### 1.3 Manual verification steps

Automated coverage above is necessary but not sufficient for a
production irreversible cutover. Before flipping the flag on a real
multi-node deployment:

- [ ] Pair two real nodes (see docs/multi-host.md's Connect flow) with
      `TERMYARD_V2_STATE=1` on both, and manually confirm: create a
      session on node B, watch it appear in node A's dashboard without a
      manual refresh; kill it on node A, watch it disappear on node B.
- [ ] Manually kill and restart the daemon process under one node mid-session
      and confirm the browser reconnects and the terminal does not visibly
      remount. `e2e/multi-node.spec.ts`'s "case 4" (stop/restart B) now
      exercises the real-backend version of this automatically, but is
      currently blocked by the two production defects noted above (item
      1.3) -- do not treat that automated case as sufficient until it
      passes for real; a manual pass is still required until then.
- [ ] Manually verify crash recovery: kill `-9` the server process with a
      live v2 session, restart it, confirm the session's phase reflects
      the crash correctly (not silently dropped, not silently
      resurrected) per the crash-safety work in Tasks 1-9.
- [ ] Confirm `TERMYARD_V2_STATE=1` was the ONLY env/config change made;
      no other behavior was toggled at the same time (isolates any
      post-cutover regression report to the actual cause).

### 1.4 Known limitations and gaps (honest as of this writing)

These are real, currently-shipped gaps in the v2 path. They do not by
themselves block cutover (the legacy path has its own gaps and is being
retired regardless), but they must be listed here so cutover sign-off is
an informed decision, not a surprise discovered in production:

- **Remote session creation targeting a specific host is wired but
  currently broken, not unimplemented.** This claim is stale as of Task 15
  Task 2's real two-node E2E harness: `App.tsx`'s `handleCreateSession`
  now sends the New Session modal's selected host as `target_owner` on
  `POST /api/v2/session-commands`, and `routes_state_v2.go`'s
  `handleV2SessionCommand` routes a `target_owner` create through
  `PeerMgr.RequestRemoteCreate` -> `pkg/state/remote_create.go`'s
  `RemoteCreateCoordinator` over the real peer RPC -- it does not silently
  create locally. It fails for a different, confirmed reason: see item 1.3
  above's defect (1) (the `RemoteCreateRequest.Target` marshal bug), which
  the real harness's "case 1" reproduces end to end.
- **Drag-and-drop has no v2 equivalent.** The legacy path's "drag New
  Session onto a pane to split" and "drag a session onto another pane to
  swap/move" gestures (exercised by `smoke.spec.ts`'s drag-and-drop test)
  are not wired into `AppV2`/`TiledView`'s v2 branch. Splitting still
  works via the UI's explicit split button in v2 mode; only the drag
  gesture is missing.
- **Schedule-ID association for remote sessions is unsupported in v2.**
  The legacy `session-attrs` schedule-id field
  (`sessionattrs.Attr.ScheduleID` / `peer.SessionAttr.ScheduleID`) has no
  v2 equivalent; `AppV2` hardcodes `V2_EMPTY_SESSION_ATTRS` (see
  `App.tsx`), so no v2 session can have a schedule id at all yet.
- **Real two-node E2E coverage now exists but is red, not incomplete.**
  `e2e/multi-node.spec.ts` (Task 15 Task 2) drives a real two-(and
  three-)process harness (`e2e/fixtures/termyardCluster.ts`) covering
  remote catalog visibility/creation, remote attach+I/O, label/kill
  convergence, offline retention/reconnect, restart routing, and
  v2-only-rejects-legacy-peer. There is no remaining `test.skip`. The
  create-dependent cases currently FAIL, and correctly so: they surfaced
  two real production defects (see item 1.3 above) that block v2 session
  creation entirely, independent of any harness limitation. The prior
  claim that `pkg/pty/daemon.go`'s `DaemonConfig.SocketDir` has no override
  is also stale and has been fixed: `pkg/commands/server/runtime.go`'s
  `defaultSessionDir()` already honors `TERMYARD_SESSION_DIR`, and the
  harness uses it to give every node its own isolated daemon socket
  directory. This is now a real, automated coverage path for the exact
  scenario item 1.3 asks you to check manually above -- it just cannot
  pass yet because of the two defects it found.
- **Presentation records are not part of the durable workspace stream.**
  Only bootstrap (`GET /api/v2/bootstrap`'s `presentations` field) carries
  them; `ApplyWorkspaceCommand`'s `WorkspaceActionPresent` path never
  publishes to the live stream. A browser that's open across a present
  action from another node will not see it live.
- **No layout-switching support in the state stream.**
  `ws.NewStateStreamHub` always streams the first layout returned by
  `catalog.Layouts()` (sorted by ID); there is no per-connection layout
  selection. Fine today because v2 only ever has one layout in view, but
  worth flagging before any multi-layout UI work lands on top of this.

## 2. Cutover sequence

v2-only nodes reject any peer that does not advertise both v2 capabilities
(`pkg/peer/protocol.go`'s `requiresV2Peer`/`peerCapsSatisfyV2`, gated on
`CapV2Catalog` + `CapV2Command`, which are only advertised when
`deps.V2CommandSvc != nil` -- i.e. only when `TERMYARD_V2_STATE=1` was set
AND the v2 store/catalog initialized successfully on that node, per
`pkg/commands/server/runtime.go`). This means: **a v2-only node dropped
into a mesh with even one still-legacy peer cannot sync with that peer at
all** -- there is no legacy fallback path left once `V2CommandSvc` is
non-nil (see the commit "Complete legacy elimination in v2 mode: stop
legacy loops, reject non-v2 peers"). Therefore cutover across a multi-node
mesh MUST be a coordinated all-nodes upgrade, in this exact order:

1. **Freeze new pairings.** Stop pairing new nodes into the mesh for the
   duration of the cutover window (a node paired mid-cutover with
   inconsistent capabilities on either side is the exact failure mode this
   order avoids).
2. **Upgrade every node's binary first, flag still off.** Deploy the
   cutover-commit binary to every node in the mesh with
   `TERMYARD_V2_STATE` unset (or `0`). At this point every node is still
   legacy-only at runtime, but every node now HAS the capability to run v2
   if flipped -- this step exists purely to avoid a binary-version skew
   during the flag flip in step 3.
3. **Verify the preflight checklist (section 1) on every node
   individually**, with the binary from step 2 but the flag still off.
   Confirm connectivity/basic health is unaffected by the new binary
   before touching the flag anywhere.
4. **Flip `TERMYARD_V2_STATE=1` on ALL nodes as close to simultaneously as
   operationally possible**, and restart each node's process. There is no
   safe "flip one node, watch it for a day, then flip the next" rollout
   for a mesh that has ANY live peer pairings: the moment the first node
   restarts with the flag on, `requiresV2Peer` becomes true on that node,
   and every peer that hasn't ALSO restarted with the flag on will be
   rejected at the next handshake attempt (existing bootstrap; new
   `/ws/peer` connections use the mutual-auth challenge/response, and
   `peerCapsSatisfyV2` is checked against the OTHER side's advertised
   `AuthOKPayload.Capabilities`/`AuthPayload.Capabilities`). A single-node
   deployment (no peers at all) does not have this constraint and may
   flip independently.
5. **Verify each node individually post-flip**: `GET /api/peers` shows
   every previously-connected peer back at `status: "connected"` (not
   stuck in backoff), and `GET /api/v2/bootstrap` returns a non-empty
   `local` catalog matching what was running pre-cutover.
6. **Run the manual verification steps from section 1.3 against the live
   mesh**, not just against a throwaway two-node test setup.
7. Only after step 6 passes on the real mesh does cutover move to the
   proof gate (section 3) and, once that passes, the deletion gate
   (section 4).

## 3. Proof gate

"Proof" for this project is not a vibe check -- it is exactly the
acceptance ledger this redesign has been held to across its external
review rounds, restated here as concrete pass/fail checks. Cutover does
not proceed past section 2 step 7 until every row below is a checked pass,
with the specific verification named, not a general "looks fine":

| Property | Pass condition | Verified by |
|---|---|---|
| **Immutable identity** | No canonical identity field (`OwnerID`/`SessionID`/`LayoutID`/`SplitID`/`CommandID`/`SessionRef`) is ever derived from, or invalidated by, a mutable display label/name change. | `pkg/state/ids_test.go`, `pkg/state/document_test.go`; browser: `web/src/lib/terminalPool.test.ts`'s identity-survives-rename tests; `App.test.tsx`'s AppV2 key-stability tests. |
| **Single authority** | Exactly one code path ever mutates a given piece of state at runtime -- v2 mode fully replaces (does not shadow-write alongside) `state.Manager`, legacy peer sync messages, and the legacy WS hub subscribe path. | `pkg/commands/server/runtime_test.go` (v2-mode/legacy-mode paired tests proving no dual-write); `pkg/ws/hub_test.go` (stateCh nil when `V2Catalog() != nil`); `pkg/peer/session_dupreg_test.go`. |
| **Peer trust** | A peer's v2 catalog/command data is only ever accepted from a session whose owner id was established at mutual-auth time (challenge/response + capability check); no embedded foreign-owner ref is ever accepted into the wrong owner's catalog. | `pkg/peer/v2_capability_gate_test.go`, `pkg/server/routes_state_v2_remote_test.go` (foreign-owner embedded ref rejection). |
| **Crash safety** | A killed `-9` process, restarted, reflects an accurate session phase (not silently dropped, not silently resurrected as healthy) and the v2 store's atomic write/read round-trips without partial-write corruption. | `pkg/commands/server/runtime_test.go`'s `TestRefreshDaemonState_ClassifiesBeforePublishing`; v2 store atomic-write tests in `pkg/state`; manual step in section 1.3. |
| **Non-blocking hot path** | Catalog projection (the code path serving every browser bootstrap/stream read) never performs blocking I/O (e.g. `/proc` reads) inline; such reads happen on a background refresh cadence only. | `pkg/commands/server/runtime.go`'s `v2RuntimeEnricher` design (background `refreshRuntimeCache`, in-memory-only `Enrich`); PTY bench in `docs/v2-baseline.md`. |
| **UI correctness** | The browser never shows stale/duplicate/partially-applied state, and reconnect/generation changes never cause visible remounts of live terminals. | `web/src/state/v2/store.test.ts` (generation/revision acceptance rule); `web/src/lib/terminalPool.test.ts` (terminal instance identity survives rename/generation-change); `e2e/multi-node.spec.ts` (Task 15 Task 2: real reconnect/restart convergence, cases 4-5); `e2e/smoke.spec.ts`. |
| **Fault-tolerant transport** | A slow/disconnected browser client never blocks delivery to other clients; a retried command with the same `CommandID` is deduplicated, not double-applied. | `pkg/ws/state_stream_test.go` (slow-client-does-not-block-fast-client, coalescing-to-latest-revision); Go-side `CommandReceipt` dedup tests in `pkg/state`; browser-level smoke in `e2e/multi-node.spec.ts`'s retry-idempotency test. |

If any row is not currently a clean pass on the exact commit being cut
over, cutover stops there. Do not proceed to section 4 with a row in a
"mostly passes" or "passes except for a known flake" state -- fix or
explicitly re-scope the row first.

## 4. Deletion gate

Once section 3 is fully green on the real deployed mesh (section 2 step
7's manual verification included), the following legacy code becomes safe
to delete. This list was produced by grepping the current codebase for the
actual v2 gating symbols (`v2Mode`, `V2CommandSvc`, `V2Catalog`), not
guessed:

- **`pkg/state/manager.go`'s `Manager` type and every call site.**
  Current call sites (confirmed via `grep -rl "state.Manager" pkg/
  --include=*.go`, excluding `_test.go`): `pkg/commands/server/runtime.go`,
  `pkg/peer/manager.go`, `pkg/peer/protocol.go`, `pkg/peer/session.go`,
  `pkg/scheduler/runner.go`, `pkg/server/options.go`, `pkg/ws/hub.go`.
  Each of these currently branches on `rt.v2Mode` /
  `deps.V2CommandSvc != nil` / `h.stateMgr.V2Catalog() != nil` to skip the
  legacy path when v2 is active; once v2 is the ONLY path, delete the
  legacy branch and the `Manager`-typed field/parameter entirely, not just
  the branch condition.
- **Legacy peer message types and their handlers**: `MsgStateUpdate`,
  `MsgStateEvent`, `MsgPeerState`, `MsgSessionAction`, `MsgRequestState`
  (all defined in `pkg/peer/protocol.go`), plus their `StateUpdatePayload`,
  `StateEventPayload`, `PeerStatePayload`, `SessionActionPayload` wire
  types and every `case Msg...:` handler for them in
  `pkg/peer/session.go`/`pkg/peer/session_state.go`. These are already
  gated off (dropped, not sent) whenever `deps.V2CommandSvc != nil` --
  deletion removes the dead branch, it does not change runtime behavior at
  cutover time. Do NOT delete `MsgSessionAttrsSnapshot`/
  `MsgSessionAttrsDelta`/`MsgSessionOrderSnapshot`/`MsgSessionOrderDelta`/
  `MsgGroupSnapshot`/`MsgGroupDelta`/`MsgToolEvent`/`MsgActivityUpdate`/
  `MsgStats`/`MsgCapturePane*` -- those are NOT part of the legacy
  state-sync path this redesign replaces and stay regardless of v2
  cutover.
- **The legacy WS hub subscribe path** in `pkg/ws/hub.go`'s `Hub.Run`:
  the `if h.stateMgr.V2Catalog() == nil { stateCh = h.stateMgr.Subscribe()
  ... }` branch and the `state.StateEvent`-driven `case evt := <-stateCh:`
  arm become dead code once `V2Catalog()` is always non-nil; delete both,
  and `state.Manager`'s `Subscribe`/`Unsubscribe`/publish machinery once
  nothing calls it anymore.
- **Legacy `/session/*` HTTP routes** in `pkg/server/routes_sessions.go`:
  `POST /session/new`, `/session/display-name`, `/session/regenerate-name`,
  `/session/rename`, `/session/select-window`, `/session/kill`, and the
  `GET /sessions`, `GET/POST /session-attrs`, `GET/POST /session-order`
  routes, along with `registerSessionsRoutes`'s legacy branches. These are
  already gated (each currently checks `opts.V2CommandSvc` and routes
  there when present, per the "Address external review: eliminate shadow
  mode" and "gated ... legacy writes behind V2CommandSvc checks" commits)
  -- once v2 is the only path, delete the legacy branch of each handler,
  and any handler that has NO v2 equivalent at all (nothing has been
  identified as v2-equivalent-free among these; if Task 15 finds one,
  that is a new decision to raise, not to silently drop).
- **`TERMYARD_V2_STATE` / `isV2StateEnabled()` itself.**
  `pkg/commands/server/runtime.go`'s `c.Bool("no-auth")`-style flag check
  and `web/src/lib/featureFlags.ts`'s `isV2StateEnabled()` (including its
  `VITE_V2_STATE` build env and `termyard.v2State` localStorage override)
  both go away -- v2 becomes unconditional, not opt-in.
- **The `AppLegacy`/`AppV2` split in `web/src/App.tsx`.** Once
  `isV2StateEnabled()` is gone, `AppLegacy` (and every hook it alone uses:
  `useWorkspace`'s legacy reducer path, `useSessions`'s legacy
  `/api/sessions` polling, etc.) is deleted; `AppV2` is renamed to `App`
  (or inlined) and becomes the only render path. `App.test.tsx`'s
  "mode-splitting" describe block (which exists specifically to prove the
  two paths stay isolated) is deleted at the same time, since there is
  only one path left to test.
- **`e2e/multi-node.spec.ts`'s `test.skip` blocker is resolved** (Task 15
  Task 2): the file no longer has any `test.skip`/`test.fixme`, and drives
  a real two-(and three-)process harness via `TERMYARD_SESSION_DIR`
  (already supported by `pkg/commands/server/runtime.go`'s
  `defaultSessionDir()`, so no further Registry-construction change was
  needed here). What remains before this deletion pass is to get its
  create-dependent cases from red to green by fixing the two production
  defects it found (see item 1.3 above), not to build any more harness.

Do not delete anything not on this list without treating that as a new,
separately-reviewed decision -- this list is deliberately exhaustive for
what the redesign's OWN gating symbols point to, not a general invitation
to also clean up unrelated legacy code while in the area.

## 5. Rollback note

**There is no state-preserving rollback for this cutover.** This was
already decided earlier in this redesign (not a new decision introduced by
this document): the v2 store's on-disk format
(`pkg/state/document.go`'s `AppDocument`, schema 2) is not a superset or
subset of the legacy per-field state files it replaces, and no
bidirectional converter between them exists or is planned. Once a node's
v2 store has diverged from what its legacy state files would have said
(which happens immediately, from the first command executed after
cutover), there is no operation that reconstructs a consistent legacy
state from the v2 store, or vice versa.

**If cutover fails, the fallback is redeploying the previous binary
version, not a live downgrade.** Concretely:

- Keep the pre-cutover binary artifact (and, for a peered mesh, confirm
  every node's pre-cutover binary is retrievable) until section 3's proof
  gate has been green in production for a duration the team judging
  cutover is comfortable with.
- If a fatal issue is discovered post-cutover, the recovery path is: stop
  the affected node(s), redeploy the last known-good pre-cutover binary,
  restart with `TERMYARD_V2_STATE` unset. This restores legacy runtime
  behavior going forward but does NOT restore any session/layout state
  created or mutated while running on v2 -- that state lives only in the
  v2 store format and is not read by the legacy binary.
- Because of this, cutover should not be attempted against sessions/data
  anyone cannot afford to lose track of, and the coordinated-all-nodes
  sequence in section 2 exists specifically to minimize the window during
  which a partial, hard-to-reason-about mixed state (some nodes v2, some
  not, mid-mesh) can exist at all.
