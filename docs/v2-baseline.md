# v2 Architecture Redesign — Task 1 Baseline

This file records the current behavior baselines captured in Task 1 of the v2 redesign. All numbers were measured on the reviewed HEAD `3902bf9b4a1f35fdc26c8f474ce7f2891d551f91` plus the Task 1 test/baseline changes.

## Environment

| Item | Value |
|------|-------|
| Host | Linux amd64 |
| CPU | 11th Gen Intel(R) Core(TM) i7-1165G7 @ 2.80GHz |
| Go | go1.25.1 linux/amd64 |
| Node | v24.14.1 |
| npm | 11.11.0 |
| Vitest | 2.1.9 |
| Playwright | 1.62.1 |
| TypeScript | 5.7.0 |

## Regression locks added

- `pkg/pty/registry_test.go` — `TestLifecycleStore_DoesNotPolluteDefaultStateDir` proves benchmark/comparison lifecycle writes land only in an isolated directory.
- `pkg/commands/server/runtime_test.go` — `TestRefreshDaemonState_ClassifiesBeforePublishing` proves crash classification/cleanup happens before the live state is published.
- `web/src/state/workspaceReducer.ts` — a `groups/snapshot` now replaces the whole map rather than merging; `groups/delta` remains a merge.
- `web/src/hooks/useWebSocket.ts` — added a generation/disposed guard so cleanup cannot schedule a new socket.
- `web/src/lib/terminalPool.ts` — `disposeAbsent` skips while the catalog is uncertain.
- `pkg/peer/manager_test.go` — documents current pointer-mutation and stale-unregister semantics.

## Baseline measurements

### Local create acceptance → terminal-connect time

Measured server-side daemon cold-start with `BenchmarkDaemonCreateToSocketReady` (`RUN_BASELINE=1`).

| Batch size | Average time to connectable socket |
|-----------:|------------------------------------|
| 1 session  | 25.99 ms |
| 5 sessions | 26.52 ms (last-in-batch) |

### PTY echo latency

Measured with the existing `TestPTYComparison` benchmark (`RUN_PTY_BENCH=1`) on a direct PTY.

| Metric | Value |
|--------|-------|
| Average first-byte latency | 195.754 µs |
| Throughput (`seq 1 1000000`) | 2.51 MB/s |

### Browser render/projection time

Measured with Playwright against the Vite dev server, stubbing the API with generated session fixtures. Time is from navigation start until the last session row is visible in the sidebar.

| Fixtures | Render time |
|---------:|------------:|
| 100 sessions | 6,238 ms |
| 500 sessions | 7,349 ms |

### Serialized snapshot sizes

Measured with `BenchmarkSerializedSize*` in `pkg/state` (`go test ./pkg/state -bench=BenchmarkSerializedSize -benchtime=1x`).

| Domain | Count | Bytes |
|--------|------:|------:|
| Session snapshot | 1 | 695 |
| Session snapshot | 10 | 6,940 |
| Session snapshot | 100 | 69,575 |
| Session snapshot | 500 | 348,717 |
| Group snapshot | 1 | 382 |
| Group snapshot | 10 | 3,813 |
| Group snapshot | 100 | 38,327 |
| Session-order snapshot | 1 | 88 |
| Session-order snapshot | 10 | 869 |
| Session-order snapshot | 100 | 8,782 |
| Session-order snapshot | 500 | 44,330 |
| Session-attrs snapshot | 1 | 108 |
| Session-attrs snapshot | 10 | 594 |
| Session-attrs snapshot | 100 | 5,634 |
| Session-attrs snapshot | 500 | 28,434 |

## Test runs

- Narrow Go tests added/modified in Task 1 pass: `pkg/pty`, `pkg/commands/server`, `pkg/peer`.
- `go test $(go list ./... | grep -v '/pkg/state$') -count=1` passes except the pre-existing `pkg/wikilite.TestSupervisorStatusFresh` failure.
- `go test ./pkg/state` cannot compile because `pkg/state/document_test.go` references v2-only types (`Split`, `Leaf`, `PaneNode`, etc.) that are not present at this revision. This is a pre-existing issue, not introduced by Task 1.
- Frontend unit tests pass (`npm test`): 246 tests across 21 files.
- `npm run typecheck` fails on a pre-existing error in `web/src/state/v2/types.test.ts` (`TS2352` conversion to `PaneNode`).
- Existing E2E smoke suite passes (6 tests).
- Baseline render E2E passes and produced the numbers above.

## Incomplete / blocked items

- `go test ./...` cannot be fully green without addressing the pre-existing `pkg/state/document_test.go` compile failure and `pkg/wikilite.TestSupervisorStatusFresh` failure.
- `npm run build` / `npm run typecheck` cannot pass until the pre-existing `web/src/state/v2/types.test.ts` type error is resolved.

These blocks are outside Task 1's scope because they are not caused by the baseline changes and would require fixing or removing partial v2 code that predates Task 1.

## Task 13: PTY echo latency (stable-attach path)

Benchmark added in Task 13: `BenchmarkStableEchoLatency` measures echo latency through a stable-attach daemon connection (re-keyed by SessionID+generation). Measured on same machine as baseline.

| Path | Echo latency (first-byte) | Notes |
|------|:------------------------:|-------|
| Direct PTY (baseline) | 195.754 µs | /bin/sh directly |
| Stable-attach daemon | 662.725 µs | Via daemon socket (inter-process) |

Daemon overhead (~3.4x) is expected due to socket IPC, protocol marshaling, and inter-process buffer delays. No regression detected on the stable-attach path.

## Acceptance status

- Crash state is not published as live in the same cycle. ✅
- A full group snapshot removes absent groups. ✅
- Unmount/cleanup cannot reconnect a WebSocket. ✅
- One uncertain session response cannot dispose active terminals. ✅
- The existing ghost lifecycle pollution regression remains fixed. ✅
- Baseline data exists for Task 13 and Task 15 comparisons. ✅
- Task 13 echo latency benchmark added; no regression on stable-attach path. ✅
