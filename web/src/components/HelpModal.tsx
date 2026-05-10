import { useEffect } from 'react'

interface HelpModalProps {
  onClose: () => void
}

const isMac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.userAgent)
const mod = isMac ? '⌘' : 'Ctrl'

type ShortcutItem = { section: string } | { keys: string[]; label: string }

function getShortcuts(): ShortcutItem[] {
  return [
    { section: 'Navigation' },
    { keys: [`${mod}+K`], label: 'Quick Switcher' },
    { keys: [`${mod}+Shift+]`, `${mod}+Shift+[`], label: 'Next / Previous session' },
    { keys: [`${mod}+Shift+O`], label: 'Overview / Dashboard' },
    { keys: [`${mod}+,`], label: 'Settings' },
    { keys: [`${mod}+/`], label: 'Help' },
    { keys: [`${mod}+\\`], label: 'Toggle sidebar' },

    { section: 'Session' },
    { keys: [`${mod}+Shift+\\`], label: 'Split pane' },

    { section: 'Terminal' },
    { keys: [`${mod}+Shift+F`], label: 'Toggle fullscreen' },
    { keys: ['Esc'], label: 'Exit fullscreen' },
  ]
}

function Kbd({ children }: { children: string }) {
  return (
    <kbd className="inline-flex items-center justify-center min-w-[28px] h-6 px-1.5 rounded-xs border border-hairline bg-gradient-to-b from-[#121212] to-[#0d0d0d] text-mute text-xs font-mono font-bold">
      {children}
    </kbd>
  )
}

export function HelpModal({ onClose }: HelpModalProps) {
  const shortcuts = getShortcuts()

  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault()
        e.stopImmediatePropagation()
        onClose()
      }
    }
    window.addEventListener('keydown', onKeyDown, true)
    return () => window.removeEventListener('keydown', onKeyDown, true)
  }, [onClose])

  return (
    <div
      className="fixed inset-0 z-[10000] flex items-center justify-center bg-black/70 backdrop-blur-sm"
      onClick={(e) => { if (e.target === e.currentTarget) onClose() }}
    >
      <div className="bg-surface border border-hairline rounded-xl shadow-[0_32px_128px_rgba(0,0,0,0.8)] w-full max-w-md mx-4 overflow-hidden font-sans">
        <div className="flex items-center justify-between px-6 py-4 border-b border-hairline bg-surface">
          <h2 className="text-[13px] font-bold text-ink tracking-widest uppercase">Keyboard Shortcuts</h2>
          <button onClick={onClose} className="text-mute/40 hover:text-ink text-2xl leading-none px-1 transition-colors">×</button>
        </div>
        <div className="px-6 py-6 max-h-[75vh] overflow-y-auto bg-surface">
          {shortcuts.map((item, i) => {
            if ('section' in item && item.section) {
              return (
                <div key={i} className={`text-[11px] font-bold text-primary uppercase tracking-widest ${i > 0 ? 'mt-8' : ''} mb-3 ml-1`}>
                  {item.section}
                </div>
              )
            }
            return (
              <div key={i} className="flex items-center justify-between py-2 group hover:bg-surface-elevated/10 rounded-md px-2 -mx-2 transition-colors">
                <span className="text-[13px] font-medium text-mute/80 group-hover:text-ink transition-colors">{'label' in item ? item.label : ''}</span>
                <div className="flex items-center gap-1.5">
                  {'keys' in item && item.keys?.map((k, j) => (
                    <Kbd key={j}>{k}</Kbd>
                  ))}
                </div>
              </div>
            )
          })}
        </div>
        <div className="px-6 py-3.5 border-t border-hairline bg-surface-elevated/20 text-xs font-bold uppercase tracking-widest text-mute/40 flex items-center gap-2">
          <span>Press</span>
          <Kbd>Esc</Kbd>
          <span>to close reference</span>
        </div>
      </div>
    </div>
  )
}
