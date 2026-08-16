# Compact sidebar clipping correction

## Problem

The 44px collapsed rail clipped its plain session initial and allowed child content to influence its visual width.

## Desired outcome

Restore the original plain collapsed-sidebar presentation while ensuring the initial remains fully visible and the rail never widens.

## Scope

- Keep the 44px collapsed rail fixed with `w-11 max-w-11` and `min-w-0` constraints.
- Center the original uppercase session initial in collapsed rows.
- Preserve the original host dot and grouped tool-session rendering.
- Remove compact-avatar redesign visuals and accent borders from collapsed rows.
- Preserve hover expansion and expanded sidebar behavior.

## Non-goals

- Add status rings, badges, avatars, connectors, or tooltips.
- Change session grouping, status calculation, or expanded rows.
- Change collapse persistence or hover animation.

## Acceptance criteria

1. Collapsed rail remains exactly 44px and child content cannot widen it.
2. Plain session initial is fully visible and centered.
3. Original host dot and grouped tool-session behavior remain intact.
4. Collapsed selected rows have no accented border treatment.
5. Hover expansion, expanded layout, and existing interactions remain unchanged.
6. Focused tests and typecheck pass.
