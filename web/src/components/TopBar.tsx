import { useEffect, useMemo, useRef, useState } from 'react'
import type { ToolEvent } from '../hooks/useToolEvents'
import { usePreferences } from '../hooks/usePreferences'
import { toolColors, statusConfig } from '../theme'
import { LOUD_STATUSES } from '../lib/sessionState'
import { cn } from '../lib/utils'
import { HeaderOverflow } from './HeaderOverflow'

interface TopBarProps {
  currentView: string
  settingsActive?: boolean
  selfUpdateAvailable?: boolean
  updateVersion?: string
  onApplyUpdate?: () => Promise<void>
  updateApplying?: boolean
  onDismissUpdate?: () => void
  onOverview: () => void
  onSettings: () => void
  onWiki?: () => void
  onNewSession?: () => void
  onHelp?: () => void
  onPortForwards?: () => void
  onSchedules?: () => void
  events: ToolEvent[]
  connected: boolean | null
  onJumpToSession: (session: string, windowIndex?: number, pane?: string) => void
  onDismiss: (evt: ToolEvent) => void
  onDismissAll: () => void
  panesCount?: number
  onSplitPane?: (direction: 'h' | 'v') => void
  glance: { parked: number; working: number; waiting: number }
}

function formatPane(pane?: string): string {
  if (!pane) return ''
  return pane.startsWith('%') ? pane.slice(1) : pane
}

interface StatsData {
  cpu_percent?: number
  memory?: { total_mb: number; used_mb: number; percent: number }
  agent_panes?: number
}

function MicroDial({ percent, color }: { percent: number; color: string }) {
  const r = 6
  const circ = 2 * Math.PI * r
  const pct = Math.min(Math.max(percent, 0), 100)
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" className="-rotate-90">
      <circle cx="8" cy="8" r={r} fill="none" stroke="var(--surface-elevated)" strokeWidth="2.5" />
      <circle cx="8" cy="8" r={r} fill="none" stroke={color} strokeWidth="2.5" strokeLinecap="round" strokeDasharray={circ} strokeDashoffset={circ * (1 - pct / 100)} />
    </svg>
  )
}

function SystemDials({ connected }: { connected: boolean | null }) {
  const [stats, setStats] = useState<StatsData>({})
  const [open, setOpen] = useState(false)
  const rootRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    let active = true
    const poll = () => {
      fetch('/api/stats')
        .then(r => r.json())
        .then(data => {
          if (!active) return
          setStats({ cpu_percent: data.system?.cpu_percent, memory: data.system?.memory, agent_panes: data.agent_panes })
        })
        .catch(() => {})
    }
    poll()
    const id = setInterval(poll, 5000)
    return () => { active = false; clearInterval(id) }
  }, [])
  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => { if (!rootRef.current?.contains(e.target as Node)) setOpen(false) }
    document.addEventListener('mousedown', onDown)
    return () => document.removeEventListener('mousedown', onDown)
  }, [open])

  if (connected === false) {
    return (
      <span className="flex items-center rounded-sm border border-destructive/40 bg-destructive/10 px-2 py-0.5 text-[10px] font-bold text-destructive animate-[pulse_1.5s_ease-in-out_infinite]">offline</span>
    )
  }

  const cpu = stats.cpu_percent
  const mem = stats.memory?.percent
  const gb = (mb: number) => Math.round((mb / 1024) * 10) / 10
  const cpuColor = cpu !== undefined && cpu > 90 ? 'var(--destructive)' : 'var(--chart-primary)'
  const memColor = mem !== undefined && mem > 90 ? 'var(--destructive)' : 'var(--chart-secondary)'
  if (cpu === undefined && mem === undefined) return null

  return (
    <div ref={rootRef} className="group relative flex items-center">
      <button type="button" onClick={() => setOpen(v => !v)} title="System" className="flex items-center gap-1.5 rounded-sm px-1 py-0.5 hover:bg-surface-elevated transition-colors">
        {cpu !== undefined && <MicroDial percent={cpu} color={cpuColor} />}
        {mem !== undefined && <MicroDial percent={mem} color={memColor} />}
      </button>
      <div className={cn('pointer-events-none absolute right-0 top-full mt-2 z-50 min-w-44 rounded-md border border-hairline bg-surface-elevated px-3 py-2 text-[11px] font-medium text-mute shadow-xl group-hover:block', open ? 'block' : 'hidden')}>
        <div className="flex justify-between gap-6"><span>CPU</span><span className="tabular-nums text-ink">{cpu !== undefined ? `${Math.round(cpu)}%` : '—'}</span></div>
        <div className="mt-1 flex justify-between gap-6"><span>Memory</span><span className="tabular-nums text-ink">{mem !== undefined ? `${Math.round(mem)}%` : '—'}</span></div>
        {stats.memory && <div className="text-right tabular-nums text-mute/60">{gb(stats.memory.used_mb)} / {gb(stats.memory.total_mb)} GB</div>}
        {stats.agent_panes !== undefined && <div className="mt-1 flex justify-between gap-6"><span>Agents</span><span className="tabular-nums text-ink">{stats.agent_panes}</span></div>}
      </div>
    </div>
  )
}

export function TopBar({
  currentView,
  settingsActive,
  selfUpdateAvailable,
  onOverview,
  onSettings,
  onWiki,
  onNewSession,
  onHelp,
  onPortForwards,
  onSchedules,
  events,
  connected,
  onJumpToSession,
  onDismiss,
  onDismissAll,
  panesCount,
  onSplitPane,
  glance,
  updateVersion,
  onApplyUpdate,
  updateApplying,
  onDismissUpdate,
}: TopBarProps) {
  const actionable = useMemo(() => events.filter(e => LOUD_STATUSES.has(e.status)), [events])
  const [showAll, setShowAll] = useState(false)
  const dismissTimers = useRef<Map<string, number>>(new Map())
  const { prefs } = usePreferences()
  const alertRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const onDown = (e: MouseEvent) => {
      if (showAll && !alertRef.current?.contains(e.target as Node)) setShowAll(false)
    }
    document.addEventListener('mousedown', onDown)
    return () => document.removeEventListener('mousedown', onDown)
  }, [showAll])

  useEffect(() => {
    const seconds = prefs.agent_banner.auto_dismiss_seconds
    if (seconds <= 0) return

    events.forEach((evt, i) => {
      if (!LOUD_STATUSES.has(evt.status)) return
      const key = `${evt.tool}-${evt.session}-${evt.pane}-${i}`
      if (dismissTimers.current.has(key)) return
      const timer = window.setTimeout(() => {
        onDismiss(evt)
        dismissTimers.current.delete(key)
      }, seconds * 1000)
      dismissTimers.current.set(key, timer)
    })

    return () => {
      dismissTimers.current.forEach(t => clearTimeout(t))
      dismissTimers.current.clear()
    }
  }, [events, prefs.agent_banner.auto_dismiss_seconds, onDismiss])

  const primary = actionable[0]
  const extras = actionable.slice(1)

  return (
    <header className="flex items-center gap-3 px-4 h-11 border-b border-hairline bg-canvas shrink-0 font-sans text-sm font-semibold relative">
      <div className="flex items-center gap-3 shrink-0">
        <button className={cn('flex items-center gap-2 shrink-0 rounded-sm px-1.5 py-1 -mx-1 transition-colors', currentView === 'overview' ? 'bg-surface-elevated' : 'hover:bg-surface-elevated')} onClick={onOverview} title="Home">
          <img src="/favicon.svg" alt="termyard" width="18" height="18" className="rounded-sm" />
          <span className="font-display text-[11px] font-bold tracking-[0.12em] text-ink">Termyard</span>
        </button>

        <div className="flex items-center gap-1">
          <button
            type="button"
            onClick={onOverview}
            title="Home"
            className={cn('p-1.5 rounded-sm transition-colors', currentView === 'overview' ? 'bg-surface-elevated text-ink' : 'text-mute hover:bg-surface-elevated hover:text-ink')}
          >
            <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round"><path d="M3 10.5 12 3l9 7.5" /><path d="M5 9.5V21h14V9.5" /></svg>
          </button>
          {onNewSession && (
            <button
              type="button"
              draggable
              onDragStart={(e) => { e.dataTransfer.setData('application/x-termyard-new-session', '1'); e.dataTransfer.effectAllowed = 'copy' }}
              onClick={onNewSession}
              title="New session (drag onto a pane to split)"
              className="p-1.5 rounded-sm hover:bg-surface-elevated text-ink transition-colors"
            >
              <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round"><rect x="3" y="3" width="18" height="18" rx="2" /><path d="M12 8v8M8 12h8" /></svg>
            </button>
          )}
          {currentView === 'session' && panesCount !== undefined && panesCount < 4 && onSplitPane && (
            <>
              <button
                type="button"
                onClick={() => onSplitPane('h')}
                title="Split horizontally"
                aria-label="Split pane horizontally"
                className="p-1.5 rounded-sm hover:bg-surface-elevated text-ink transition-colors"
              >
                <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round"><rect x="3" y="3" width="7" height="18" rx="1" /><rect x="14" y="3" width="7" height="18" rx="1" /></svg>
              </button>
              <button
                type="button"
                onClick={() => onSplitPane('v')}
                title="Split vertically"
                aria-label="Split pane vertically"
                className="p-1.5 rounded-sm hover:bg-surface-elevated text-ink transition-colors"
              >
                <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round"><rect x="3" y="3" width="18" height="7" rx="1" /><rect x="3" y="14" width="18" height="7" rx="1" /></svg>
              </button>
            </>
          )}
        </div>

        {glance.waiting > 0 && (
          <div className="flex items-center gap-2 text-[11px] font-mono">
            <span className="text-warning inline-flex items-center gap-1">
              <span className="animate-[pulse_1.5s_ease-in-out_infinite]">●</span>
              {glance.waiting} waiting
            </span>
          </div>
        )}
      </div>

      <div ref={alertRef} className="relative flex-1 min-w-0">
        {!primary && selfUpdateAvailable && (
          <div className="relative">
            <button
              type="button"
              disabled={updateApplying}
              onClick={() => { if (!updateApplying) void onApplyUpdate?.().catch(() => {}) }}
              className="w-full flex items-center gap-2 px-3 py-1.5 pr-8 rounded-sm border border-warning/30 bg-warning/8 text-left min-h-8"
            >
              <span className="w-1.5 h-1.5 rounded-full bg-warning animate-[pulse_1.5s_ease-in-out_infinite] shrink-0" />
              <span className="text-[11px] font-bold shrink-0 text-warning">UPDATE</span>
              {updateApplying ? (
                <span className="text-[11px] text-ink truncate">Installing, reconnecting…</span>
              ) : (
                <>
                  <span className="text-[11px] text-ink truncate">
                    {updateVersion ? `${updateVersion} available` : 'A new version is available'}
                  </span>
                  <span className="text-[11px] text-mute/70 truncate">— click to install</span>
                </>
              )}
            </button>
            {!updateApplying && (
              <button
                type="button"
                aria-label="Dismiss update"
                className="absolute right-2 top-1/2 -translate-y-1/2 rounded-full border border-warning/30 bg-surface px-2 py-0.5 text-[10px] font-bold text-warning hover:text-ink"
                onClick={(e) => { e.stopPropagation(); onDismissUpdate?.() }}
              >
                ×
              </button>
            )}
          </div>
        )}
        {primary && (
          <div className="relative">
            <button
              type="button"
              onClick={() => onJumpToSession(primary.host ? `${primary.host}/${primary.session}` : primary.session, primary.window, primary.pane)}
              className="w-full flex items-center gap-2 px-3 py-1.5 pr-8 rounded-sm border border-warning/30 bg-warning/8 text-left min-h-8"
            >
              <span className="w-1.5 h-1.5 rounded-full bg-warning animate-[pulse_1.5s_ease-in-out_infinite] shrink-0" />
              <span className="text-[11px] font-bold shrink-0" style={{ color: toolColors[primary.tool] || 'var(--mute)' }}>
                {primary.tool.toUpperCase()}
              </span>
              <span className="text-[11px] font-medium shrink-0 text-warning">
                {(statusConfig[primary.status] || { label: primary.status }).label}
              </span>
              <span className="text-[11px] text-ink truncate">
                {primary.host_name ? `${primary.host_name}: ` : ''}{primary.session}
              </span>
              {(primary.window !== undefined || primary.pane) && (
                <span className="text-[10px] text-mute/50 shrink-0 font-mono">
                  {primary.window !== undefined ? `w${primary.window}` : ''}{primary.pane ? `:p${formatPane(primary.pane)}` : ''}
                </span>
              )}
              {primary.message && <span className="text-[11px] text-mute/70 truncate">— {primary.message}</span>}
            </button>
            <button
              type="button"
              aria-label="Dismiss alert"
              className="absolute right-2 top-1/2 -translate-y-1/2 rounded-full border border-warning/30 bg-surface px-2 py-0.5 text-[10px] font-bold text-warning hover:text-ink"
              onClick={(e) => { e.stopPropagation(); onDismiss(primary) }}
            >
              ×
            </button>

            {extras.length > 0 && (
              <button
                type="button"
                onClick={() => setShowAll(v => !v)}
                className="absolute right-2 top-1/2 -translate-y-1/2 rounded-full border border-warning/30 bg-surface px-2 py-0.5 text-[10px] font-bold text-warning"
              >
                +{extras.length}
              </button>
            )}

            {showAll && extras.length > 0 && (
              <div className="absolute left-0 right-0 top-full mt-2 z-50 rounded-md border border-hairline bg-surface-elevated shadow-xl overflow-hidden">
                {extras.map((evt, i) => {
                  const cfg = statusConfig[evt.status] || { color: 'var(--warning)', label: evt.status }
                  return (
                    <div
                      key={`${evt.tool}-${evt.session}-${evt.pane}-${i}`}
                      className="flex items-center gap-2 px-3 py-2 text-[12px] hover:bg-surface transition-colors"
                    >
                      <button
                        type="button"
                        className="flex min-w-0 flex-1 items-center gap-2 text-left"
                        onClick={() => {
                          onJumpToSession(evt.host ? `${evt.host}/${evt.session}` : evt.session, evt.window, evt.pane)
                          setShowAll(false)
                        }}
                      >
                        <span className="w-1.5 h-1.5 rounded-full shrink-0" style={{ background: cfg.color }} />
                        <span className="font-bold" style={{ color: toolColors[evt.tool] || 'var(--mute)' }}>{evt.tool.toUpperCase()}</span>
                        <span className="text-warning font-medium">{cfg.label}</span>
                        <span className="text-ink truncate">{evt.host_name ? `${evt.host_name}: ` : ''}{evt.session}</span>
                      </button>
                      <button
                        type="button"
                        className="text-mute hover:text-ink transition-colors"
                        onClick={(e) => { e.stopPropagation(); onDismiss(evt) }}
                      >
                        ×
                      </button>
                    </div>
                  )
                })}
                <div className="border-t border-hairline flex items-center justify-end px-3 py-2 gap-2">
                  <button
                    type="button"
                    onClick={() => { onDismissAll(); setShowAll(false) }}
                    className="text-[11px] font-bold text-mute hover:text-ink transition-colors"
                  >
                    Clear all
                  </button>
                </div>
              </div>
            )}
          </div>
        )}
      </div>

      <div className="flex items-center gap-2 shrink-0">
        <SystemDials connected={connected} />

        <HeaderOverflow
          onPortForwards={onPortForwards}
          onSchedules={onSchedules}
          onWiki={onWiki}
          onHelp={onHelp}
        />

        <button
          onClick={onSettings}
          title="Settings"
          className={cn('relative p-1.5 rounded-sm transition-colors', settingsActive ? 'bg-surface-elevated text-ink' : 'hover:bg-surface-elevated text-ink')}
        >
          {selfUpdateAvailable && <span className="absolute -right-0.5 -top-0.5 h-2 w-2 rounded-full bg-warning ring-2 ring-canvas" />}
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round">
            <circle cx="12" cy="12" r="3" /><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1 0 2.83 2 2 0 0 1-2.83 0l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-2 2 2 2 0 0 1-2-2v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 0 1-2.83 0 2 2 0 0 1 0-2.83l.06-.06A1.65 1.65 0 0 0 4.68 15a1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1-2-2 2 2 0 0 1 2-2h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 0 1 0-2.83 2 2 0 0 1 2.83 0l.06.06A1.65 1.65 0 0 0 9 4.68a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 2-2 2 2 0 0 1 2 2v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83 0 2 2 0 0 1 0 2.83l-.06.06A1.65 1.65 0 0 0 19.4 9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 2 2 2 2 0 0 1-2 2h-.09a1.65 1.65 0 0 0-1.51 1z" />
          </svg>
        </button>
      </div>
    </header>
  )
}
