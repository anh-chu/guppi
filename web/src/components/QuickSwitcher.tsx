import { useState, useEffect, useRef, useMemo } from 'react'
import { Session, sessionKey, sessionLabel } from '../hooks/useSessions'
import { ToolEvent } from '../hooks/useToolEvents'
import { toolColors } from '../theme'
import { cn } from '../lib/utils'

interface QuickSwitcherProps {
  sessions: Session[]
  waitingEvents: ToolEvent[]
  onSelect: (sessionName: string, windowIndex?: number) => void
  onOverview: () => void
  onCreateSession: () => void
  onClose: () => void
}

interface SwitcherItem {
  type: 'waiting' | 'session' | 'window' | 'nav' | 'action'
  label: string
  detail?: string
  sessionName: string
  windowIndex?: number
  statusColor?: string
  action?: string
}

function fuzzyMatch(query: string, text: string): boolean {
  const lower = text.toLowerCase()
  const q = query.toLowerCase()
  let qi = 0
  for (let i = 0; i < lower.length && qi < q.length; i++) {
    if (lower[i] === q[qi]) qi++
  }
  return qi === q.length
}

export function QuickSwitcher({ sessions, waitingEvents, onSelect, onOverview, onCreateSession, onClose }: QuickSwitcherProps) {
  const [query, setQuery] = useState('')
  const [selectedIndex, setSelectedIndex] = useState(0)
  const inputRef = useRef<HTMLInputElement>(null)
  const listRef = useRef<HTMLDivElement>(null)

  const allItems = useMemo<SwitcherItem[]>(() => {
    const items: SwitcherItem[] = []
    const sorted = [...waitingEvents].sort((a, b) => {
      const ta = new Date(a.timestamp).getTime()
      const tb = new Date(b.timestamp).getTime()
      return ta - tb
    })
    for (const evt of sorted) {
      const evtKey = evt.host ? `${evt.host}/${evt.session}` : evt.session
      items.push({
        type: 'waiting',
        label: `${evt.session}`,
        detail: `${evt.tool} — ${evt.message || 'waiting for input'}`,
        sessionName: evtKey,
        windowIndex: evt.window,
        statusColor: toolColors[evt.tool] || 'var(--warning)',
      })
    }
    // Navigation items
    items.push({
      type: 'nav',
      label: 'Overview',
      detail: 'Dashboard',
      sessionName: '',
    })

    // New session action
    items.push({
      type: 'action',
      label: 'New Session',
      detail: 'Create & switch',
      sessionName: '',
      action: 'create',
    })

    const hasMultipleHosts = sessions.some(s => s.host)
    for (const session of sessions) {
      const sk = sessionKey(session)
      // sk stays id-based (session.name is the immutable canonical id); the
      // row SHOWS the friendly label via sessionLabel() (name -> display_name).
      const label = hasMultipleHosts && session.host_name ? `${session.host_name}: ${sessionLabel(session)}` : sessionLabel(session)
      items.push({
        type: 'session',
        label,
        detail: `${session.windows.length} window${session.windows.length !== 1 ? 's' : ''}`,
        sessionName: sk,
      })
      if (session.windows.length > 1) {
        for (const win of session.windows) {
          items.push({
            type: 'window',
            label: `${label}/${win.name}`,
            detail: `window ${win.index}`,
            sessionName: sk,
            windowIndex: win.index,
          })
        }
      }
    }
    return items
  }, [sessions, waitingEvents])

  const filtered = useMemo(() => {
    if (!query.trim()) return allItems
    return allItems.filter(item => fuzzyMatch(query, item.label) || (item.detail && fuzzyMatch(query, item.detail)))
  }, [allItems, query])

  useEffect(() => { setSelectedIndex(0) }, [filtered.length, query])
  useEffect(() => {
    requestAnimationFrame(() => inputRef.current?.focus())
  }, [])

  // Capture Escape at the window level so it doesn't reach the terminal fullscreen handler
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault()
        e.stopImmediatePropagation()
        onClose()
      }
    }
    window.addEventListener('keydown', handler, true)
    return () => window.removeEventListener('keydown', handler, true)
  }, [onClose])

  useEffect(() => {
    const list = listRef.current
    if (!list) return
    const el = list.children[selectedIndex] as HTMLElement | undefined
    el?.scrollIntoView({ block: 'nearest' })
  }, [selectedIndex])

  const selectItem = (item: SwitcherItem) => {
    if (item.type === 'nav') {
      onOverview()
    } else if (item.type === 'action' && item.action === 'create') {
      onCreateSession()
    } else {
      onSelect(item.sessionName, item.windowIndex)
    }
  }

  const handleKeyDown = (e: React.KeyboardEvent) => {
    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault()
        setSelectedIndex(i => Math.min(i + 1, filtered.length - 1))
        break
      case 'ArrowUp':
        e.preventDefault()
        setSelectedIndex(i => Math.max(i - 1, 0))
        break
      case 'Enter':
        e.preventDefault()
        if (filtered[selectedIndex]) {
          selectItem(filtered[selectedIndex])
        }
        break
      case 'Escape':
        e.preventDefault()
        onClose()
        break
    }
  }

  const hasWaiting = filtered.some(i => i.type === 'waiting')
  const hasSessions = filtered.some(i => i.type !== 'waiting')

  return (
    <div
      data-quick-switcher
      className="fixed inset-0 z-[9999] flex items-start justify-center pt-[18vh] bg-black/70 backdrop-blur-sm"
      onClick={onClose}
    >
      <div
        className="w-[520px] max-h-[460px] bg-surface border border-hairline rounded-xl shadow-[0_32px_128px_rgba(0,0,0,0.8)] flex flex-col overflow-hidden"
        onClick={e => e.stopPropagation()}
      >
        <div className="p-4 border-b border-hairline bg-surface">
          <input
            ref={inputRef}
            value={query}
            onChange={e => setQuery(e.target.value)}
            onKeyDown={handleKeyDown}
            placeholder={waitingEvents.length > 0 ? 'Action required — press Enter...' : 'Search for a session, window, or action...'}
            className="w-full text-[17px] text-ink bg-transparent border-none outline-none font-sans font-medium placeholder:text-mute/40"
          />
        </div>
        <div ref={listRef} className="flex-1 overflow-y-auto py-2">
            {filtered.length === 0 && (
              <div className="p-10 text-mute/60 text-[13px] text-center font-medium">No results found</div>
            )}
            {filtered.map((item, i) => {
              const prevItem = i > 0 ? filtered[i - 1] : null
              const showSeparator = hasWaiting && hasSessions && item.type !== 'waiting' && prevItem?.type === 'waiting'
              const isSelected = i === selectedIndex

              return (
                <div key={`${item.type}-${item.label}-${item.windowIndex}-${item.action}`}>
                  {showSeparator && <div className="h-px bg-border/40 mx-4 my-2" />}
                  <div
                    onClick={() => selectItem(item)}
                    onMouseEnter={() => setSelectedIndex(i)}
                    className={cn(
                      'py-2 px-3 mx-2 rounded-md cursor-pointer flex items-center gap-3 transition-colors',
                      isSelected ? 'bg-surface-elevated text-primary' : 'text-ink',
                    )}
                  >
                    <div className="w-6 h-6 shrink-0 flex items-center justify-center">
                      {item.type === 'waiting' && (
                        <span className="w-1.5 h-1.5 rounded-full bg-warning animate-[pulse_1.5s_ease-in-out_infinite]" />
                      )}
                      {item.type === 'nav' && (
                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round" className="text-mute">
                          <rect x="3" y="3" width="7" height="7" /><rect x="14" y="3" width="7" height="7" /><rect x="3" y="14" width="7" height="7" /><rect x="14" y="14" width="7" height="7" />
                        </svg>
                      )}
                      {item.type === 'action' && (
                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round" className="text-mute">
                          <line x1="12" y1="5" x2="12" y2="19" /><line x1="5" y1="12" x2="19" y2="12" />
                        </svg>
                      )}
                      {(item.type === 'session' || item.type === 'window') && (
                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round" className="text-mute/60">
                          <rect x="3" y="3" width="18" height="18" rx="2" ry="2"/>
                        </svg>
                      )}
                    </div>
                    <span className={cn(
                      'text-[13px] flex-1 overflow-hidden text-ellipsis whitespace-nowrap font-medium',
                      item.type === 'window' && 'pl-2 text-mute/80 font-normal',
                    )}>
                      {item.label}
                    </span>
                    {item.detail && (
                      <span
                        className="text-xs shrink-0 font-bold tracking-tight uppercase px-2 py-0.5 rounded-xs bg-surface-elevated/50"
                        style={{
                          color: item.type === 'waiting' ? (item.statusColor || 'var(--warning)') : 'var(--muted-foreground)',
                        }}
                      >
                        {item.detail}
                      </span>
                    )}
                  </div>
                </div>
              )
            })}
          </div>
        <div className="py-2.5 px-4 border-t border-hairline bg-surface-elevated/20 text-xs text-mute/60 font-bold uppercase tracking-[0.1em] flex justify-between items-center">
          <div className="flex gap-4">
            <span>↑↓ Navigate</span>
            <span>↵ Select</span>
          </div>
          <div className="flex items-center gap-1.5">
            <span className="px-1.5 py-0.5 rounded-xs border border-hairline bg-surface font-mono text-[9px]">ESC</span>
            <span>Close</span>
          </div>
        </div>
      </div>
    </div>
  )
}
