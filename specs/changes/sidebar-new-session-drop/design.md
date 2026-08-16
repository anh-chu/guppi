# Design

`TopBar` identifies New-session drags with `application/x-termyard-new-session`. `App` already owns `handleDropNewSession`, which determines the target session's host and working directory before creating and splitting the new session. `TiledView` invokes that handler with a target key and placement zone.

`Sidebar` needs an optional callback with the same target and placement shape. Its row `onDrop` checks the New-session MIME before the existing `draggingKey` guard, invokes the callback with the row key and `center`, then returns. `App` passes `handleDropNewSession` to that callback. Center is the established default placement for a target with no edge zone.
