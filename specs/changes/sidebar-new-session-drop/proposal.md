# Sidebar New-session drop

Dragging TopBar's New button onto a visible sidebar session currently reaches the row drop handler, but that handler only accepts sidebar reorder drags. The New-session MIME is therefore discarded.

Pass a New-session drop on a sidebar row to the same application callback used by pane and main-area New-session targets. The row's session key is the split target and the drop uses the established center placement.

This change does not alter sidebar session reordering, pairing, or other drop targets.
