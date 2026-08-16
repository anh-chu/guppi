# Terminal scroll position during width resize

## Problem

A terminal at the bottom can jump far up in scrollback when its viewport becomes narrower. A wheel event at the bottom can mark the terminal as user-scrolled even when no scrolling occurred. The resize path then preserves a stale scroll anchor while xterm reflows wrapped lines.

## Desired outcome

Keep a terminal pinned to the bottom when it was at the bottom before a width resize. Preserve an intentional user scroll position when the user actually scrolls up.

## Scope

- Correct wheel gesture tracking in `web/src/lib/terminalPool.ts`.
- Add focused regression coverage in `web/src/lib/terminalPool.test.ts`.
- Update the terminal UX contract if needed to clarify the behavior.

## Non-goals

- Change xterm.js reflow behavior.
- Change terminal resize messages sent to the PTY.
- Change replay handling or scrollback limits.

## Risks

The scroll state must still change when a wheel gesture actually moves the viewport. Tests must distinguish a wheel event from the subsequent viewport scroll event.

## Acceptance criteria

1. A wheel event at the bottom that produces no viewport movement does not mark the terminal as user-scrolled.
2. A wheel gesture that moves the viewport off the bottom still marks the terminal as user-scrolled.
3. Width resize from the bottom does not restore an old scroll anchor.
4. Existing terminal tests pass.
