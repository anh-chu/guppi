// Single source of truth for keyboard shortcuts shown in the Help modal and
// the Settings > Shortcuts panel. Keep this in sync with the bindings
// registered in App.tsx (tinykeys) and the xterm passthrough whitelist in
// hooks/useTerminal.ts.

const isMac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.userAgent)
export const modKey = isMac ? '⌘' : 'Ctrl'

export type ShortcutItem = { section: string } | { keys: string[]; label: string }

export function getShortcuts(): ShortcutItem[] {
  const mod = modKey
  return [
    { section: 'Navigation' },
    { keys: [`${mod}+Shift+K`], label: 'Quick Switcher' },
    { keys: [`${mod}+Shift+Enter`], label: 'New session' },
    { keys: [`${mod}+Shift+.`, `${mod}+Shift+,`], label: 'Next / Previous session' },
    { keys: [`${mod}+Shift+H`], label: 'Overview / Dashboard' },
    { keys: [`${mod}+,`], label: 'Settings' },
    { keys: [`${mod}+/`], label: 'Help' },
    { keys: [`${mod}+\\`], label: 'Toggle sidebar' },
    { keys: [`${mod}+Shift+G`], label: 'Toggle wiki panel' },

    { section: 'Session' },
    { keys: [`${mod}+Shift+\\`], label: 'Split pane' },

    { section: 'Terminal' },
    { keys: [`${mod}+Shift+F`], label: 'Toggle fullscreen' },
    { keys: ['Esc'], label: 'Exit fullscreen' },
  ]
}
