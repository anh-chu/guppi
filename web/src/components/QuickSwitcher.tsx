import { useState, useEffect, useRef, useMemo } from 'react'
import { Session, sessionKey } from '../hooks/useSessions'
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
      const label = hasMultipleHosts && session.host_name ? `${session.host_name}: ${session.name}` : session.name
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
      className="fixed inset-0 z-[9999] flex items-start justify-center pt-[20vh] bg-black/50"
      onClick={onClose}
    >
      <div
        className="w-[460px] max-h-[400px] bg-card border border-border rounded-xl shadow-2xl flex flex-col overflow-hidden"
        onClick={e => e.stopPropagation()}
      >
        <div className="p-3 px-4 border-b border-border">
          <input
            ref={inputRef}
            value={query}
            onChange={e => setQuery(e.target.value)}
            onKeyDown={handleKeyDown}
            placeholder={waitingEvents.length > 0 ? 'Waiting prompts first — press Enter...' : 'Go to session or window...'}
            className="w-full text-[15px] text-foreground bg-transparent border-none outline-none font-mono placeholder:text-muted-foreground"
          />
        </div>
        <div ref={listRef} className="flex-1 overflow-y-auto py-1">
            {filtered.length === 0 && (
              <div className="p-4 text-muted-foreground text-[13px] text-center">No matches</div>
            )}
            {filtered.map((item, i) => {
              const prevItem = i > 0 ? filtered[i - 1] : null
              const showSeparator = hasWaiting && hasSessions && item.type !== 'waiting' && prevItem?.type === 'waiting'

              return (
                <div key={`${item.type}-${item.label}-${item.windowIndex}-${item.action}`}>
                  {showSeparator && <div className="h-px bg-border mx-4 my-1" />}
                  <div
                    onClick={() => selectItem(item)}
                    onMouseEnter={() => setSelectedIndex(i)}
                    className={cn(
                      'py-2 px-4 cursor-pointer flex items-center gap-2 transition-colors',
                      i === selectedIndex ? 'bg-primary/15' : 'hover:bg-primary/5',
                    )}
                  >
                    {item.type === 'waiting' && (
                      <span className="w-1.5 h-1.5 rounded-full bg-warning shrink-0 animate-[pulse_1.5s_ease-in-out_infinite]" />
                    )}
                    {item.type === 'nav' && (
                      <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" className="shrink-0 text-primary">
                        <rect x="3" y="3" width="7" height="7" /><rect x="14" y="3" width="7" height="7" /><rect x="3" y="14" width="7" height="7" /><rect x="14" y="14" width="7" height="7" />
                      </svg>
                    )}
                    {item.type === 'action' && (
                      <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" className="shrink-0 text-accent">
                        <line x1="12" y1="5" x2="12" y2="19" /><line x1="5" y1="12" x2="19" y2="12" />
                      </svg>
                    )}
                    <span className={cn(
                      'text-sm flex-1 overflow-hidden text-ellipsis whitespace-nowrap',
                      item.type === 'window' ? 'text-muted-foreground font-normal pl-3' : 'text-foreground font-semibold',
                    )}>
                      {item.label}
                    </span>
                    {item.detail && (
                      <span
                        className="text-xs shrink-0"
                        style={{
                          color: item.type === 'waiting' ? (item.statusColor || 'var(--warning)') : 'var(--color-muted-foreground)',
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
        <div className="py-1.5 px-4 border-t border-border text-[11px] text-muted-foreground flex gap-3">
          <span>↑↓ navigate</span>
          <span>↵ select</span>
          <span>esc close</span>
        </div>
      </div>
    </div>
  )
}
