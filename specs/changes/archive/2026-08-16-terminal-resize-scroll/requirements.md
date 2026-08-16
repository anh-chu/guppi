# Requirements

## R1. Track actual viewport movement

**GIVEN** the terminal viewport is at the bottom
**WHEN** a wheel event occurs but xterm emits no viewport movement
**THEN** the terminal remains in its follow-output state

## R2. Preserve intentional scroll-up

**GIVEN** the terminal viewport is at the bottom
**WHEN** a wheel gesture moves the viewport above the bottom
**THEN** the terminal enters user-scrolled state and later fits preserve that position

## R3. Keep bottom position across width reflow

**GIVEN** the terminal is following output at the bottom
**WHEN** its width shrinks and xterm reflows scrollback
**THEN** the terminal remains at the bottom instead of restoring a stale absolute line

## R4. Keep existing resize behavior

**GIVEN** a terminal resize occurs
**WHEN** the terminal has an open PTY connection
**THEN** the existing resize message behavior remains unchanged
