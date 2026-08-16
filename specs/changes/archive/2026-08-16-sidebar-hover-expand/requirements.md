# Requirements: Sidebar hover expansion

## R1. Expand narrow collapsed sidebar on pointer enter

**GIVEN** sidebar is collapsed and configured with `collapseMode="small"`
**WHEN** pointer enters the narrow sidebar column
**THEN** sidebar temporarily renders at its configured normal width over the main content, while its narrow collapsed width remains reserved in layout, and shows expanded sidebar content using a 220ms ease-out transition.

## R2. Collapse after pointer leaves

**GIVEN** sidebar is temporarily expanded by hover
**WHEN** pointer leaves the sidebar
**THEN** after a 120ms leave grace period, sidebar returns to its narrow collapsed presentation over 180ms with an ease-in transition, and the main content remains in its original position.

## R3. Keep persistent state unchanged

**GIVEN** sidebar is collapsed
**WHEN** hover expansion starts or ends
**THEN** the collapse toggle callback is not invoked and `termyard:sidebar-collapsed` remains unchanged.

## R4. Keep hidden mode unchanged

**GIVEN** sidebar is collapsed and configured with `collapseMode="hidden"`
**WHEN** the page is rendered
**THEN** sidebar remains width zero and uses its existing explicit expand control; no hover expansion is required.

## R5. Keep collapsed rail width fixed

**GIVEN** sidebar is collapsed in narrow mode and a session has tool or pane display content
**WHEN** the sidebar renders that session
**THEN** child content is clipped within the fixed narrow rail and cannot widen the layout slot.

## R6. Preserve existing controls

**GIVEN** sidebar is expanded either persistently or temporarily
**THEN** existing filter, grouping, quick-shell, collapse, session selection, and resize controls behave as before.
