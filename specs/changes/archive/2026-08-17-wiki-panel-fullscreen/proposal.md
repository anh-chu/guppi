# Wiki panel fullscreen mode

## Problem

The wiki panel is limited to its docked width, even when a file needs more room to read or browse.

## Desired outcome

Let users expand the desktop wiki panel to fill the app viewport, then return to its previous docked size without losing the open file or iframe state.

## Scope

- Add an expand/collapse control to the desktop wiki panel header.
- Make expanded mode fill the viewport and hide the resize handle.
- Let `Escape` leave expanded mode.
- Update the wiki panel UX contract and add focused component coverage.

## Non-goals

- Browser or operating-system fullscreen through the Fullscreen API.
- Changing mobile behavior, which already uses full-screen modal mode.
- Persisting fullscreen state across panel closes or page reloads.

## Risks

- The expanded panel could cover app controls if its stacking and close affordance are wrong.
- Keyboard handling could accidentally close the panel instead of only leaving expanded mode.

## Acceptance criteria

1. On desktop, the wiki header exposes an `Enter fullscreen` control.
2. Activating it makes the wiki panel cover the app viewport, hides the drag handle, and keeps the current wiki content mounted.
3. The control changes to `Exit fullscreen`; activating it restores the docked panel and its prior width.
4. Pressing `Escape` while expanded exits expanded mode without closing the panel.
5. Mobile remains full-screen and does not show a redundant expand control.
6. Existing tests pass, with focused coverage for toggle and Escape behavior.
