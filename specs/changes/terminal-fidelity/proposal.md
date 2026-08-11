# Terminal Fidelity: PTY should feel like a native terminal

## Problem

Termyard's terminal panes have four recurring quirks that break the "feels like iTerm / Windows Terminal" bar:

1. **Scroll jumps, both directions.** While scrolled up reading, the viewport sometimes snaps to the bottom. While pinned at the bottom following output, it sometimes yanks up to a random buffer position.
2. **Self-shrinking terminal.** Content occasionally renders at a smaller grid than the pane (often after pane show/hide, splits, or multi-client use) until a manual resize forces a correct refit.
3. **Key combos not passed through.** Some modifier combos (e.g. Alt+Up) never reach the PTY.
4. **Scrollback loss.** Older history is sometimes missing even though the scrollback preference (lines) should retain it.

## Investigated causes (hypotheses, to be verified per task)

1. **Scroll:** `fitPreservingScroll` (web/src/lib/terminalPool.ts:170–230) infers scroll intent from geometry (`baseY - viewportY` snapshots, a 2px at-bottom check) captured mid-frame while xterm updates viewport state asynchronously during rapid writes. False positives restore phantom offsets (jump off bottom); false negatives clear `userScrolled` and snap to bottom. `userScrolled` is also not reset across reattach/replay.
2. **Resize:** fit on a zero-sized/hidden container silently keeps stale dimensions (default 80x24 from terminal creation, terminalPool.ts:867–871); the deferred rAF refit is skipped by the same zero-size guard. Multiple clients on one session send uncoalesced resize messages, last-write-wins.
3. **Keys:** `macOptionIsMeta: true` (terminalPool.ts:875) conflicts with the custom key handler's `!e.altKey` checks (terminalPool.ts:1250–1330); app-level shortcut capture can consume combos before xterm.
4. **Scrollback:** unit mismatch. xterm scrollback pref is lines (default 50k); the daemon ring buffer is bytes (8MB default, pkg/pty/daemon.go:41). Long or alt-screen-heavy sessions wrap the ring and lose old primary-buffer history regardless of the lines pref.

## Desired outcome

A terminal pane that behaves like a native terminal for scrolling, resizing, keyboard input, and history retention.

## Scope

- web/src/lib/terminalPool.ts (scroll tracking, fit logic, key handler)
- web/src/components/Terminal.tsx (resize observation)
- pkg/pty/daemon.go (ring buffer sizing)
- Related prefs plumbing if ring size becomes derived from or aligned with the scrollback preference
- docs/ux-contracts.md updates for any user-visible behavior change

## Non-goals

- New rendering backends, sixel/kitty images, OSC 52 clipboard, hyperlinks
- RPC/GUI agent pane work
- Multi-client resize UX redesign beyond preventing corruption (a precedence policy may be noted as a follow-up)
- Scrollback search or performance work

## Risks

- Scroll logic is timing-sensitive; a wrong fix trades one jump for another. Mitigate with explicit repro scenarios before/after.
- Changing `macOptionIsMeta` or key handling can break users who rely on current Option-key behavior on macOS.
- Larger ring buffers increase daemon memory per session (bounded, configurable).
- Reattach/replay interacts with all four areas; regressions there are the recent DA1-leak class of bug.

## Acceptance criteria

1. **Scroll**
   - Pinned at bottom: output streaming, fits, resizes, and reattach never move the viewport off the bottom without a user scroll input.
   - Scrolled up: output streaming below, fits, and resizes never move the reading position; anchoring survives reflow.
   - Scroll intent (`pinned to bottom`) derives only from real user input events, never inferred from geometry mid-write; state resets sanely on reattach.
2. **Resize**
   - A fit against a zero-sized or hidden container never commits dimensions; when the container becomes visible/nonzero, a refit runs automatically and the grid matches the pane.
   - Repro: hide pane via split changes, show again — content fills pane with no manual resize.
3. **Keys**
   - Alt+Arrow (and Alt+letter) combos reach the PTY as ESC-prefixed sequences on all platforms (subject to macOS Option settings being coherent).
   - App shortcuts that intentionally shadow terminal input are enumerated in docs/ux-contracts.md; everything else passes through.
4. **Scrollback**
   - Daemon retention is sized so the configured scrollback lines preference is actually reachable for typical output (sizing rule documented); running a long high-output session then scrolling up shows history to the configured depth or a documented byte bound.
5. All existing tests pass; new targeted tests cover scroll-intent tracking and zero-size fit guard where feasible.

## Suggested task order

1. Bug 4 (ring sizing) — mechanical, verify units, align.
2. Bug 3 (key passthrough) — small, testable.
3. Bug 2 (fit guards + refit on visibility) — medium.
4. Bug 1 (scroll intent rework) — hardest, needs repro-first workflow.
