# Task 15 Proof Ledger

This file is the frozen Task 15 contract created by Task 0. It records the
exact starting environment/SHA, the closed Task 14 review history that
precedes it, the frozen acceptance ledger (verbatim acceptance-bar text from
`docs/task-15-plan.md`), the frozen benchmark thresholds, the stabilization
window, the legacy-deletion map, the rollback rule, and the backlog rule.
Nothing in this file may be altered by later Task 15 workers except adding
evidence to a row's Status/evidence trail. Acceptance-bar text is frozen and
must not be expanded or reworded after Task 0.

## 1. Starting environment record

- Exact starting SHA (`git rev-parse HEAD`): `e9c99aa1d34b08555039dd1c627c2e34e719ec67`
- Branch: `master`
- `git status --short` at freeze time:
  ```
  ?? docs/task-14-proof-ledger.md
  ?? docs/task-15-plan.md
  ```
- Go version: `go version go1.25.1 linux/amd64`
- Node version: `v24.14.1`
- npm version: `11.11.0`
- OS / kernel (`uname -a`):
  ```
  Linux devvm 6.17.0-29-generic #29-Ubuntu SMP PREEMPT_DYNAMIC Tue May  5 19:42:34 UTC 2026 x86_64 GNU/Linux
  ```
- CPU (`lscpu` summary):
  ```
  Architecture:        x86_64
  Model name:           11th Gen Intel(R) Core(TM) i7-1165G7 @ 2.80GHz
  CPU(s):               8 (On-line CPU(s) list: 0-3; Off-line CPU(s) list: 4-7)
  Thread(s) per core:   2
  Core(s) per socket:   4
  Socket(s):            1
  CPU max MHz:          4700.0000
  CPU min MHz:          400.0000
  BogoMIPS:             3379.20
  L1d cache: 192 KiB (4 instances)  L1i cache: 128 KiB (4 instances)
  L2 cache:  5 MiB (4 instances)    L3 cache: 12 MiB (1 instance)
  NUMA node(s): 1
  ```
- Playwright version: `Version 1.62.1`

## 2. Final Task 14 review range and final round-9 fix commit

Reconstructed from `git log`, matching commit messages that reference a
"Finding" (the review-round convention used throughout Task 14):

```
e9c99aa Finding: activity identity normalization for AppV2 (OwnerID vs fingerprint)
004703f Finding: remote tool-event dispatch is now recorded and broadcast
355d2cc fix(web,server): normalize peer-fingerprint/OwnerID identity in tool events & host UI (Finding C)
f6e21ac fix(peer): single-flight identical concurrent command RPCs (Finding B)
05a4703 fix(state): make Catalog.CommandReceipt durability-aware (Finding A)
28aaac1 fix(peer,server,web): stop conflating v2 OwnerID with peer transport fingerprint (Finding 1)
```

- Full closure range (oldest to newest Finding-tagged commits in this final
  review tail): `28aaac1..e9c99aa` (i.e. `28aaac1` through `e9c99aa`
  inclusive).
- Round grouping as tracked in prior worker memory: round 8 closed with
  `28aaac1`, `05a4703`, `f6e21ac`, `355d2cc`; round 9 closed with `004703f`
  and `e9c99aa`.
- **Final round-9 fix commit: `e9c99aa1d34b08555039dd1c627c2e34e719ec67`**
  ("Finding: activity identity normalization for AppV2 (OwnerID vs
  fingerprint)"). This is also the exact starting SHA for Task 15 recorded
  in section 1.

## 3. Frozen Task 15 acceptance ledger

Acceptance-bar text below is copied VERBATIM from `docs/task-15-plan.md`'s
"Frozen Task 15 acceptance ledger" section. It must not be paraphrased or
altered by any later Task 15 work. All rows start at Status `NOT STARTED`.

| ID | Required checkpoint | Acceptance bar | Production trace | Sibling grep/search performed | Status |
|---|---|---|---|---|---|
| T15-01 Exact-head reproducibility | Proof, Deletion | The release-candidate and final deletion SHAs each have independently inspectable build, unit, race, vet, frontend, and E2E results. | (none yet) | (none yet) | NOT STARTED |
| T15-02 Immutable identity | Proof | No canonical/durable/socket/terminal/command key derives from a mutable label, and OwnerID is never used as a peer fingerprint. | (none yet) | (none yet) | NOT STARTED |
| T15-03 Single authority | Proof, Deletion | V2 operation constructs and mutates no legacy authority; after deletion no legacy authority remains compiled. | (none yet) | (none yet) | NOT STARTED |
| T15-04 Crash-safe durability | Proof | Every accepted result is durably acknowledged; injected write interruption recovers one complete valid document and never replays uncertain success. | (none yet) | (none yet) | NOT STARTED |
| T15-05 Idempotent commands | Proof | Create, label, kill, dismiss, retry, recover, remote create, and workspace commands replay the original result; overlapping external side effects execute once. | (none yet) | (none yet) | NOT STARTED |
| T15-06 Peer trust and ordering | Proof | Every v2 payload is bound to the authenticated connection identity; stale/foreign snapshots and replies are rejected without corrupting current state. | (none yet) | (none yet) | NOT STARTED |
| T15-07 Remote completeness | Proof | Real browser commands and terminal attachment reach the selected owner; typed business errors remain typed across RPC. | (none yet) | (none yet) | NOT STARTED |
| T15-08 Stream and reconnect safety | Proof | Initial/live coalescing, slow clients, close/send races, connection replacement, browser reload, and server restart preserve the latest confirmed state without panic or deadlock. | (none yet) | (none yet) | NOT STARTED |
| T15-09 Lifecycle correctness | Proof | Create, ready, natural exit, kill, crash, recovery, and removal maintain one logical SessionID and correct generation/phase transitions. | (none yet) | (none yet) | NOT STARTED |
| T15-10 Real two-node proof | Proof, Deletion | Before cutover, two isolated server/daemon processes prove remote create, catalog visibility, remote attach/I/O, offline retention/reconnect, and v2/legacy incompatibility. After deletion, rerun the v2/v2 cases and prove no legacy startup mode remains. No mandatory skip remains. | (none yet) | (none yet) | NOT STARTED |
| T15-11 Performance continuity | Proof | PTY, create-to-ready, catalog projection, state serialization, command response, and browser render measurements remain within frozen thresholds. | (none yet) | (none yet) | NOT STARTED |
| T15-12 Bounded resources | Proof | The soak run shows bounded receipts, state size, pending work, goroutines, processes, WebSockets, sockets, timers, and terminal-pool entries, with no ghost state. | (none yet) | (none yet) | NOT STARTED |
| T15-13 Coordinated cutover | Cutover | All nodes use the same build, legacy processes/state are intentionally removed, capability convergence is verified, and smoke checks pass before normal use. | (none yet) | (none yet) | NOT STARTED |
| T15-14 Legacy deletion | Deletion | Feature flags, legacy stores/routes/messages/reducers/name routing, and dead compatibility branches are absent; only v2 authority remains. | (none yet) | (none yet) | NOT STARTED |
| T15-15 No compatibility residue | Deletion | No importer, exporter, dual-write, shadow, fallback, mixed-version adapter, or silent legacy read remains. | (none yet) | (none yet) | NOT STARTED |

## 4. Frozen benchmark thresholds

These thresholds are frozen before any measurement is taken and may not be
changed after final measurements are visible:

- Latency medians may not regress by more than **20%** against the Task 15
  starting SHA on the same machine.
- Fixed-count serialized sizes may not grow by more than **10%** without an
  approved contract change.
- Label/move/split/select/resize may cause **zero** terminal remounts or
  reconnects when generation is unchanged.
- **No** invariant failure, process leak, or monotonic resource leak is
  allowed.

## 5. Stabilization window

- Default duration: **24 hours** of normal use.
- Required events during the window (per Task 8 of the plan):
  - normal local and remote session use;
  - at least one peer disconnect/reconnect;
  - at least one server restart;
  - at least one session crash/recovery;
  - browser reload/reconnect;
  - state/backup validation and resource sampling.
- The window is not to be shortened after a problem appears.

## 6. Legacy-deletion map (verbatim from plan's "Legacy-deletion map" table)

Delete or structurally reduce only after the cutover/stabilization gate:

| Area | Verified paths/components |
|---|---|
| Runtime flag/branch | `pkg/commands/server/runtime.go`, `web/src/lib/featureFlags.ts`, `web/src/App.tsx` |
| Legacy authority | `pkg/state/manager.go`, `pkg/state/manager_test.go` |
| Legacy auxiliary stores | `pkg/sessionattrs`, `pkg/sessionorder`, `pkg/groupsync` |
| Server option fields | legacy fields in `pkg/server/options.go` after call-site removal |
| Legacy routes | legacy handlers/registration in `pkg/server/routes_sessions.go`; preserve v2/shared routes |
| Legacy server state hooks | legacy `StateMgr` wiring in `pkg/server/server.go`, `pkg/server/group_naming.go`, and only those call sites proven obsolete |
| Legacy WS state publication | legacy `StateSource` subscription path in `pkg/ws/hub.go` and corresponding tests |
| Legacy peer state messages | `MsgStateUpdate`, `MsgStateEvent`, `MsgPeerState`, `MsgSessionAction`, `MsgRequestState` and payloads/handlers in `pkg/peer/protocol.go`, `pkg/peer/session.go`, `pkg/peer/session_state.go` |
| Legacy attrs/order/group peer messages | delete with `sessionattrs`, `sessionorder`, and `groupsync` only after confirming no v2/shared caller remains |
| Scheduler concrete manager field | unused `*state.Manager` field/parameter in `pkg/scheduler/runner.go` |
| Legacy browser authority | `web/src/hooks/useWorkspace.ts`, `web/src/hooks/useSessions.ts`, `web/src/hooks/useGroupSync.ts`, `web/src/state/workspaceReducer.ts`, and dedicated tests |
| App mode split | `AppLegacy`, mode-splitting tests, and localStorage/build-time v2 override |
| Name-based attach/keys | legacy-only terminal route/pool branches after repository-wide caller search proves no supported caller remains |

Do not delete `MsgToolEvent`, activity messages, statistics, capture, file
read/upload, stream registries, or other peer features merely because they
live beside legacy state messages.

## 7. Rollback rule (verbatim from plan)

- Before step 14 of the cutover sequence: restore the old binary/config and
  archived legacy files if needed.
- "After step 14, there is no state-preserving rollback. Rollback requires
  stopping the mesh and explicitly discarding all v2 changes since
  cutover."

## 8. Review/backlog rule (verbatim from plan)

- "Non-critical cosmetic or explicitly out-of-scope gaps go to the backlog
  and do not extend Task 15."
- A finding may block Task 15 only when it is Critical, High, or Medium and
  directly cites/violates a frozen T15-01 through T15-15 ledger row.

## 9. Notes on `docs/v2-direct-cutover.md`

Task 0's instructions permit updating "only the Task 15 status/header" in
`docs/v2-direct-cutover.md` if needed. That document does not currently
contain a dedicated Task 15 status/header field for Task 0 to set (it is
written as a standing runbook, not a per-task status tracker), and the plan
does not explicitly require adding one at Task 0. This freeze intentionally
**skips** modifying `docs/v2-direct-cutover.md` in Task 0; any header/status
update there is left to whichever later task (per the plan's own File and
component map, `docs/v2-direct-cutover.md` is owned by Workstream B /
the cutover-rehearsal integrator) actually needs it.
