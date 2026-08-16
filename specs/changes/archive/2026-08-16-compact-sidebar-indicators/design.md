# Design: Compact sidebar clipping correction

Keep original collapsed presentation. Do not introduce a new visual system.

- Outer narrow placeholder remains `w-11 max-w-11 min-w-0`.
- Inner sidebar remains `min-w-0 overflow-hidden`.
- Collapsed session row keeps plain first uppercase label character, centered with `justify-center`.
- Existing host dot remains at row top-right.
- Existing grouped tool-session rendering remains unchanged.
- Collapsed selected/recently-done rows use background and text changes only, not accented borders.
- Expanded rows, hover expansion, context menus, drag/drop, and keyboard behavior stay unchanged.

The clipping fix comes from fixed rail sizing, min-width constraints, overflow clipping, and centered plain text. No avatars, rings, badges, connectors, or tooltip state are needed.
