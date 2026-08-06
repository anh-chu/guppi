# Destructive rewrite: canonical schema-3 state, single runtime

**Starting SHA (plan doc anchor):** `5ddd59b05d780c020e4a94de6f548d6b813328d4`
**Final SHA (this freeze):** `2f23518f437fe6c4a871ed8b25b4de09f07d44fb`
(`5ddd59b..2f23518`, 10 commits.)

## What changed

Between these two commits, termyard's dual-runtime "v2" redesign was made
the **only** runtime path and every legacy alternative was deleted, not
just disabled:

- `pkg/state`: replaced the transition (schema-2) document format with
  canonical schema 3 (`f5262ac`); folded presentation/scheduler semantics
  into canonical commands (`b9c9a47`).
- `pkg/commands/server/runtime.go`: deleted `TERMYARD_V2_STATE` /
  `v2Mode` entirely. The store/catalog/command-service/remote-create/
  state-stream stack is always constructed; there is no code path left
  that can select anything else (`b692ea4`).
- `pkg/server`: deleted `state.Manager`, the legacy session-attrs/
  session-order/groups/group-name HTTP routes (no backing store exists
  to route them to), and `options.go`'s legacy `StateMgr`/`AttrsStore`/
  `OrderStore`/`GroupStore` fields outright (`d2d1a58`). Canonical routes
  were renamed from `/api/v2/*` to `/api/state/*` and every `V2*` type/
  symbol name in production code was renamed to drop the `v2` prefix
  (`2f23518`).
- `pkg/peer`: deleted the legacy state-sync peer protocol
  (`MsgStateUpdate`/`MsgStateEvent`/`MsgPeerState`/`MsgSessionAction`/
  `MsgRequestState` and their payload types/handlers); peers must
  advertise canonical capabilities or are rejected at handshake
  (`ae79a77`).
- `web/src`: deleted `AppLegacy`, the workspace reducer, `/api/sessions`
  polling, `useGroupSync`, `featureFlags.ts` (`isV2StateEnabled()`,
  `VITE_V2_STATE`, `termyard.v2State` localStorage override), and the
  mode-splitting logic in `App.tsx`. `App.tsx` is now a bare auth/setup
  shell that renders the canonical `SessionApp` unconditionally
  (`f8772cc`, `2f23518`).
- `pkg/config`: `V2StateDir` was renamed to `StateDir` and its on-disk
  path changed from `<DataDir>/v2` to `<DataDir>/state`.

**This is destructive, not migratory.** The schema-3 store is not a
superset or subset of the legacy per-field state files (session-attrs
JSON, session-order JSON, group JSON, the old `state.Manager` store) it
replaced, and no bidirectional converter between them was ever built or
is planned. A node upgrading across this change does not carry old
sessions, layouts, groups, or ordering forward — see "Reset procedure"
below, which is the **only** supported upgrade path.

## Residue verification

`scripts/proof.sh`'s `residue-check` step (and the equivalent CI step in
`.github/workflows/tests.yml`) runs, repo-wide:

```bash
git grep -nE 'TERMYARD_V2_STATE|VITE_V2_STATE|termyard\.v2State|AppLegacy|isV2StateEnabled|V2[A-Z]|/api/v2|/ws/v2|state\.Manager|sessionattrs|sessionorder|groupsync|MsgStateUpdate|MsgStateEvent|MsgPeerState|MsgSessionAction|MsgRequestState|_compat|Compat[A-Z]' -- . ':!docs/history/**'
git grep -nEi 'legacy mode|v2 mode|shadow mode|fallback to legacy|transition-only' -- '*.go' '*.ts' '*.tsx' '*.yml' '*.yaml'
```

As of this freeze, every remaining match in production (non-test,
non-historical-doc) code is one of:

- a test that explicitly asserts the legacy thing has zero effect or is
  absent (e.g. `pkg/commands/server/runtime_test.go`'s
  `TestNewRuntimeEnvVarCannotSelectAlternatePath`, which sets
  `TERMYARD_V2_STATE=1` specifically to prove it no longer changes
  anything; `pkg/server/route_table_test.go`'s comment proving legacy
  routes are unregistered);
- `_compat`/`Compat[A-Z]` in `pkg/state/INVARIANTS.md` and its tests,
  which document a real, current, intentional part of schema 3 (mutable
  display data such as `Name`/`Cwd`/`Generation` lives in a `_compat`
  substructure, never in canonical identity fields) -- this is not
  legacy residue, it is the current contract;
- historical planning/design documents moved to `docs/history/` (see
  below), which predate this rewrite and are excluded from the residue
  grep by design;
- comments in `pkg/pty/daemon.go`/`pkg/pty/registry_stable.go` describing
  the **stable PTY daemon identity binding** scheme (`StableBinding`
  requires non-empty `Owner`/`SessionID`/`Generation`; a binding without
  them is described in code comments as "legacy mode" for that PTY
  socket-binding concept specifically). This is a different, narrower
  legacy/stable distinction than the browser-state redesign this
  document covers, and it is **not dead**: `pty.Registry.Create` (used
  only by the standalone `termyard session create/list/kill/capture` CLI
  subcommand tree in `pkg/commands/sessiondaemon`, which has never gone
  through the browser/server canonical state graph) still constructs
  daemons without a stable binding. Whether that standalone CLI tool
  should be deleted, kept, or migrated onto stable bindings is a
  **separate, undecided scope question** -- it was not part of the
  browser-state legacy this rewrite targeted, and deleting or changing a
  live CLI feature was not authorized here. Flagged for a follow-up
  decision, not silently resolved.
- one stale doc-only route-name reference (`testdata/command_result_fixture.json`'s
  `description` field said `/api/v2/session-commands`; fixed in this
  pass to `/api/state/session-commands`) and one stale comment
  (`pkg/server/routes_sessions.go`'s dead-route explanation named
  `AppLegacy`; reworded to describe the absence without naming a type
  that no longer exists in code) and one stale schema number
  (`pkg/state/INVARIANTS.md` said "must be exactly `2`"; the constant is
  `SchemaVersion = 3`; fixed).

Historical documents moved to `docs/history/` in this pass (each now
carries a "Historical" banner noting it predates the rewrite and must
not be followed as current instructions): `v2-baseline.md`,
`v2-direct-cutover.md`, `group-order-sync.md`,
`group-order-sync-review.md`, `scheduler-plan.md`,
`symmetric-peering.md`. `docs/task-14-proof-ledger.md`,
`docs/task-15-plan.md`, and `docs/task-15-proof-ledger.md` (leftover
planning artifacts from the prior staged/flag-based cutover approach
this destructive rewrite made obsolete -- that approach was abandoned in
favor of the hard switch in `b692ea4`) were deleted outright; their only
operationally relevant content (starting SHA, benchmark thresholds,
reset semantics) is captured above and below.

## Local verification actually run (this environment, this sandbox)

No CI infrastructure and no multi-node hardware exist in this sandbox.
What was actually executed, honestly:

| Check | Result | Evidence |
|---|---|---|
| `go build ./...` | **PASS** | clean, no output |
| `go vet ./...` | **PASS** | clean, no output |
| `go test ./... -count=1` | **PASS** except one pre-existing unrelated flake | `pkg/wikilite`'s `TestSupervisorStatusFresh` fails (`fresh supervisor reports installed true`) on every run, on and off this branch; not part of this rewrite's scope. All other 20 packages with tests pass. |
| `go test -race ./pkg/state ./pkg/server ./pkg/peer ./pkg/ws ./pkg/scheduler ./pkg/sessionlaunch -count=1` | **PASS** | clean, all six core packages green |
| `go test -race ./pkg/commands/server -count=1` | **FLAKY** | Passes most runs; fails intermittently (`TestRuntimeEnricherEnrichIsPureCacheLookup` vs. a leaked background goroutine from a *different* test, `TestRuntimeCancellationStops`, both racing the same package-level `readProcCwd` test-double variable). Reproduced 3/5 and 1/3 runs in separate attempts. This is a **test-hygiene bug** (a prior test's `Runtime.Start()` background refresh goroutine is not guaranteed stopped by the time the test returns, so it can outlive into the next test and race a monkey-patched package var), not a legacy/v2-residue issue and not touched by this rewrite. Flagged, not fixed, in this pass -- fixing it means changing `Runtime.Stop()`'s shutdown-wait guarantee, which is a real code change outside this quality-gate task's scope. |
| `go test -race ./... -count=1` | consistent with the above two rows | one run observed `pkg/commands/server` pass cleanly alongside the same pre-existing `pkg/wikilite` flake |
| `cd web && npm run typecheck` | **PASS** | clean |
| `cd web && npm run test:ci` | **PASS** | 27 files, 331/331 tests |
| `cd web && npm run build` | **PASS** | builds `pkg/server/dist` (132 modules, ~1MB total assets) |

**Not run, and why:**

- **Real E2E (`web/e2e/multi-node.spec.ts`, `smoke.spec.ts`)**: requires
  spawning real termyard server/daemon processes with Playwright driving
  real browsers, and (for the multi-node cases) multiple isolated
  processes acting as peers. No display server, no verified two-process
  peering setup was exercised in this sandbox session. Not run; not
  fabricated.
- **Benchmarks** (`BenchmarkDaemonCreateToSocketReady`,
  `BenchmarkStableEchoLatency`, `BenchmarkSerializedSize`): these exist
  in the nightly workflow (`benchmarks` job) and are runnable locally
  with `go test -bench`, but no baseline from a prior commit on *this*
  machine was captured to compare against in this pass, so any single
  number reported now would be presented with nothing to compare it to.
  Not run in this pass to avoid fabricating a "no regression" claim with
  no baseline; the nightly CI job is the intended place this gets
  tracked over time, machine-to-machine.
- **Soak run**: `scripts/soak.sh` does not exist in this repo (the
  nightly workflow has a guarded hook for it that no-ops when absent).
  No soak run was performed.
- **Multi-node real hardware proof**: this sandbox is a single machine
  with no second node to pair against. Any two-node claim in this
  document is a code/config claim (e.g. "the peer protocol rejects
  non-canonical peers"), verified by the relevant Go unit/integration
  test, not by an actual live two-node run.
- **Exact-binary-used-by-E2E verification**: since E2E was not run in
  this pass, this could not be verified end to end. The CI workflow
  (`.github/workflows/tests.yml`'s `e2e` job) is structured to reuse the
  exact `go` job's built binary as an artifact rather than rebuild
  independently -- that wiring was inspected and is correct, but no live
  CI run was observed in this pass to confirm it in practice.

## Reset procedure (the only upgrade instruction)

There is no migration path from any pre-rewrite state to this build.
Any node upgrading across the `5ddd59b..2f23518` range (or from any
commit before it) must perform a destructive reset:

1. **Stop every termyard process for this node**, both the server and
   any running session daemons:
   ```bash
   pkill -f 'termyard server' || true
   pkill -f 'termyard session-daemon' || true
   ```
   Confirm nothing is left with `pgrep -af termyard`.

2. **Delete the state directory.** The canonical store lives at
   `<DataDir>/state` (see `pkg/config.StateDir()`, which is
   `<DataDir>/state`, where `DataDir()` is `$XDG_DATA_HOME/termyard` or
   `~/.local/share/termyard`). An intermediate pre-final-cutover build
   may instead have written `<DataDir>/v2` (the old `V2StateDir` path
   before it was renamed to `StateDir`) -- delete that too if present:
   ```bash
   DATA_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/termyard"
   rm -rf "$DATA_DIR/state" "$DATA_DIR/v2"
   ```

3. **Delete any legacy per-field JSON stores** left over from a
   pre-rewrite version, if the same `DataDir` was ever used with a build
   older than this range (these files, and the packages that wrote them,
   no longer exist in code -- nothing reads them anymore, but they are
   not harmless clutter to leave around if this machine is reused):
   ```bash
   rm -f "$DATA_DIR"/session-attrs.json "$DATA_DIR"/session-order.json \
         "$DATA_DIR"/groups.json "$DATA_DIR"/group-order.json
   ```
   (Exact filenames varied by pre-rewrite version; if in doubt, delete
   everything directly under `$DATA_DIR` except artifacts you know you
   still need -- there is no reader left for any of it.)

4. **Delete session daemon sockets/lifecycle state**, which live outside
   `DataDir` (per-UID `/tmp` directory) and outlive a plain state-dir
   wipe:
   ```bash
   rm -rf "/tmp/termyard-sessions-$(id -u)"
   ```

5. **Start fresh.** Launch the new binary; it creates a new, empty
   schema-3 store at `<DataDir>/state` on first run. All sessions,
   layouts, groups, and ordering from before the reset are gone --
   confirm before step 1 that this is acceptable for the machine being
   upgraded.

6. **For a peered mesh**, repeat steps 1-5 on every node before
   re-pairing. A node running this build cannot exchange canonical state
   with a node still running a pre-rewrite build at all (there is no
   legacy fallback wire protocol left) -- upgrade every node in the mesh
   together, not one at a time.

There is no flag, environment variable, or config option that restores
the old behavior. `TERMYARD_V2_STATE` (and `VITE_V2_STATE` /
`termyard.v2State` on the frontend) have zero effect in this build --
`pkg/commands/server/runtime_test.go`'s
`TestNewRuntimeEnvVarCannotSelectAlternatePath` proves it.
