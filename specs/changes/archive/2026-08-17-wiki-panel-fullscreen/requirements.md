# Requirements

## R1. Enter fullscreen from desktop

**GIVEN** the wiki panel is open on a desktop viewport
**WHEN** the user activates the header control titled `Enter fullscreen`
**THEN** the panel covers the app viewport with `fixed inset-0` positioning and a high overlay z-index
**AND** the current iframe and wiki content remain mounted
**AND** the resize handle is hidden.

## R2. Exit fullscreen

**GIVEN** the wiki panel is expanded
**WHEN** the user activates the header control titled `Exit fullscreen`
**THEN** the panel returns to docked layout
**AND** its width returns to the width it had before expansion.

## R3. Escape exits fullscreen

**GIVEN** the wiki panel is expanded
**WHEN** the user presses `Escape`
**THEN** expanded mode ends
**AND** the wiki panel stays open.

## R4. Mobile behavior

**GIVEN** the viewport is mobile or uses a coarse pointer
**WHEN** the wiki panel is open
**THEN** it remains full-screen modal mode
**AND** no fullscreen toggle control is rendered.

## R5. State preservation

**GIVEN** the user enters or exits expanded mode
**WHEN** the layout changes
**THEN** the current file, iframe source, and wiki navigation state are preserved.
