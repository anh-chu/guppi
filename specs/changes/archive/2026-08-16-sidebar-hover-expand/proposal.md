# Sidebar hover expansion

## Problem

When the sidebar is collapsed to its narrow column, its contents and navigation controls disappear behind the collapsed layout. Users must click the expand control or use the keyboard shortcut to inspect or select a session.

## Desired outcome

Hovering over the collapsed narrow sidebar temporarily overlays the main content with the sidebar at its normal width. Moving the pointer away collapses it again. Hover expansion must not change the persisted collapse preference or main-content layout.

## Scope

- Add temporary overlay hover expansion to the narrow collapsed sidebar.
- Preserve existing click and keyboard collapse behavior.
- Preserve the hidden collapse mode, which has no visible hover target.
- Add focused component coverage and update the UX contract.

## Non-goals

- Change sidebar width persistence.
- Change hidden-mode behavior.
- Change touch gestures or keyboard shortcuts.
- Change per-group collapse state.

## Risks

- Pointer transitions during width animation could cause premature collapse.
- Overlay positioning could allow the main content to shift if the collapsed-width placeholder is not preserved.
- Rendering expanded content while the persisted state remains collapsed must not overwrite local storage.

## Acceptance criteria

1. Given sidebar is collapsed in `small` mode, when pointer enters its narrow column, sidebar displays normal-width content and controls overlaid on the main content.
2. Given temporary hover expansion is active, when pointer leaves sidebar, sidebar returns to narrow collapsed width and main content keeps its original position.
3. Hover expansion does not call the persistent collapse toggle or change `termyard:sidebar-collapsed`.
4. Hidden mode remains width zero and keeps its existing expand control and touch/keyboard behavior.
5. Collapsed tool and pane display content cannot widen the narrow layout slot.
6. Existing sidebar tests and focused hover tests pass.
