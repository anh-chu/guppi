# Design

Use local state in `WikiPanel` for expanded mode. The existing panel root already owns the docked width and iframe, so toggling a class on that root preserves the viewer instead of remounting it.

Desktop expanded mode uses the same `fixed inset-0 z-40 bg-canvas flex flex-row` layout already used by mobile. The drag handle is rendered only when neither mobile mode nor expanded mode is active. The header button swaps its label, title, and icon between enter and exit states.

The existing width state remains untouched while expanded. Leaving expanded mode therefore restores the previous docked width without another measurement or persistence layer. A window-level keydown effect handles `Escape` only while expanded and prevents the event from reaching other fullscreen behavior.

Rejected alternative: the browser Fullscreen API. It would hide browser chrome and introduce permission/user-gesture behavior not needed for an in-app panel expansion.
