# Requirements: Compact sidebar clipping correction

## R1. Fixed narrow rail

**GIVEN** sidebar is collapsed in narrow mode
**WHEN** session rows and grouped tool content render
**THEN** child content is constrained inside the 44px rail and cannot widen the layout slot.

## R2. Preserve plain session indicator

**GIVEN** a session row renders while collapsed
**WHEN** its label is displayed
**THEN** the first uppercase label character is centered and fully visible, with no avatar or status decoration.

## R3. Preserve original host and group behavior

**GIVEN** multi-host or grouped sessions render while collapsed
**WHEN** their rows display
**THEN** the original host dot and grouped tool-session presentation remain available without widening the rail.

## R4. Remove collapsed accent borders

**GIVEN** a collapsed row is selected or recently completed
**WHEN** it renders
**THEN** it uses the original background/text treatment without a new accented border.

## R5. Preserve behavior

**GIVEN** sidebar is collapsed or expanded
**WHEN** user hovers, selects, focuses, drags, renames, or opens context actions
**THEN** existing behavior remains unchanged.
