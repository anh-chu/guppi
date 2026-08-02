//
// xtermCompat.ts — the only compatibility boundary for unavoidable xterm.js
// internal access.  Prefer the public API everywhere else.
//

import type { Terminal } from '@xterm/xterm'

type XtermWithCore = Terminal & {
  _core?: {
    viewport?: { scrollBarWidth?: number }
    _charSizeService?: { measure?: () => void }
  }
}

/**
 * Some xterm.js fallback scrollbar logic can steal width from the renderer
 * when the terminal is moved off-screen.  Force the fallback width to zero.
 */
export function neutralizeXtermScrollbarFallback(term: Terminal): void {
  try {
    const viewport = (term as XtermWithCore)._core?.viewport
    if (viewport && typeof viewport.scrollBarWidth === 'number') {
      viewport.scrollBarWidth = 0
    }
  } catch {
    /* ignored */
  }
}

/**
 * Ask xterm.js to re-measure character metrics after a font change.
 */
export function measureXtermCharSize(term: Terminal): void {
  try {
    ;(term as XtermWithCore)._core?._charSizeService?.measure?.()
  } catch {
    /* ignored */
  }
}
