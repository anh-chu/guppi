import { useState, useEffect, useMemo, useRef, useCallback } from 'react'
import type { SessionView, SessionState } from '../state/session/viewModel'
import { sessionViewSignal, stateRank, type SessionPresentationAttrs } from '../state/session/viewModel'
import type { HostSnapshot } from '../state/session/wireTypes'
type Host = HostSnapshot
import { ToolEvent } from '../hooks/useToolEvents'
import { useSchedules } from '../hooks/useSchedules'
import { cn } from '../lib/utils'
import { describeCron } from '../lib/cron'
import { formatRelativeTime, formatUptime } from '../lib/time'
import { pathLeaf } from '../lib/path'
import { hostColor } from '../lib/hostColor'
import { AgentMark } from './AgentMark'
import { useGlance } from './GlancePopover'
import { SessionActionsMenu, SessionMenuTarget } from './SessionActionsMenu'

interface SidebarProps {
  sessions: SessionView[]
  selectedSession: string | null
  collapsed: boolean
  selfUpdateAvailable?: boolean
  collapseMode: 'small' | 'hidden'
  width?: number
  onWidthChange?: (width: number) => void
  hasMultipleHosts?: boolean
  localHostId?: string
  hosts?: Host[]
  onSessionSelect: (session: SessionView) => void
  getSessionEvents: (session: string) => ToolEvent[]
  sessionNeedsAttention: (session: string) => boolean
  isSessionInActiveTurn: (session: string) => boolean
  glance?: { needsYou: number; working: number; starting: number; idle: number; offline: number; crashed: number }
  onToggleCollapse?: () => void
  onSessionKilled?: (key: string) => void
  sessionAttrs: SessionPresentationAttrs
  setSessionAttr: (key: string, next: { background?: boolean; hidden?: boolean }) => void
  // Canonical session rename: routes through v2State.sessionCommand's
  // `label` action. There is no other (legacy REST) rename path any more.
  onRenameSession?: (key: string, label: string) => void
  // True while the session list is still converging after a WS (re)connect.
  // Pruning per-device ordering then would delete entries for sessions that
  // simply haven't reappeared yet.
  pruningSuspended?: boolean
  onQuickShell?: () => void
  crashedCount?: number
  onCrashedClick?: () => void
}

const STATE_BADGE: Record<SessionState, { label: string; color: string; bg: string; pulse: boolean }> = {
  crashed:   { label: 'crashed',   color: 'var(--error)',          bg: 'rgba(244,63,77,0.12)',  pulse: true },
  needs_you: { label: 'attention', color: 'var(--accent-yellow)', bg: 'rgba(255,197,51,0.12)', pulse: true },
  working:   { label: 'working',   color: 'var(--accent-green)',  bg: 'rgba(89,212,153,0.12)', pulse: true },
  starting:  { label: 'starting',  color: 'var(--info)',           bg: 'rgba(41,128,185,0.12)', pulse: true },
  idle:      { label: 'idle',      color: 'var(--mute)',          bg: 'transparent',           pulse: false },
  offline:   { label: 'offline',   color: 'var(--mute)',          bg: 'transparent',           pulse: false },
}

function readStoredList(key: string): string[] {
  try {
    const stored = localStorage.getItem(key)
    if (!stored) return []
    const parsed = JSON.parse(stored)
    return Array.isArray(parsed) ? parsed.filter((v): v is string => typeof v === 'string') : []
  } catch {
    return []
  }
}

function writeStoredList(key: string, values: string[]) {
  localStorage.setItem(key, JSON.stringify(values))
}

export function Sidebar({
  sessions,
  selectedSession,
  collapsed,
  selfUpdateAvailable,
  collapseMode,
  width = 288,
  onWidthChange,
  hasMultipleHosts,
  localHostId,
  hosts,
  onSessionSelect,
  getSessionEvents,
  sessionNeedsAttention,
  isSessionInActiveTurn,
  glance,
  onToggleCollapse,
  onSessionKilled,
  sessionAttrs,
  setSessionAttr,
  pruningSuspended,
  onRenameSession,
  onQuickShell,
  crashedCount = 0,
  onCrashedClick,
}: SidebarProps) {
  const glancePreview = useGlance(!!hasMultipleHosts)
  const { schedules } = useSchedules()
  // background/hidden are SERVER-AUTHORITATIVE and arrive via props. They are
  // NOT cached in localStorage -- the server owns the truth and broadcasts
  // session-attrs-updated, which App refetches and passes back down here.
  const hiddenSet = sessionAttrs.hidden
  const backgroundSet = sessionAttrs.background
  const scheduleById = useMemo(() => new Map(schedules.map(schedule => [schedule.id, schedule])), [schedules])
  const [projectFilters, setProjectFilters] = useState<string[]>(() => readStoredList('termyard:project-filters'))
  const [hiddenExpanded, setHiddenExpanded] = useState(false)
  const [scheduledExpanded, setScheduledExpanded] = useState(() => {
    try { return localStorage.getItem('termyard:scheduled-collapsed') !== '1' } catch { return true }
  })
  const toggleScheduledExpanded = useCallback(() => {
    setScheduledExpanded(prev => {
      const next = !prev
      try { localStorage.setItem('termyard:scheduled-collapsed', next ? '0' : '1') } catch {}
      return next
    })
  }, [])
  const [menu, setMenu] = useState<{ target: SessionMenuTarget; x: number; y: number } | null>(null)
  const [filterOpen, setFilterOpen] = useState(false)
  const [resizing, setResizing] = useState(false)
  const startResize = useCallback((e: React.MouseEvent) => {
    e.preventDefault()
    setResizing(true)
    const startX = e.clientX
    const startW = width
    const onMove = (ev: MouseEvent) => {
      const next = Math.min(560, Math.max(260, startW + (ev.clientX - startX)))
      onWidthChange?.(next)
    }
    const onUp = () => {
      setResizing(false)
      window.removeEventListener('mousemove', onMove)
      window.removeEventListener('mouseup', onUp)
      document.body.style.cursor = ''
      document.body.style.userSelect = ''
    }
    document.body.style.cursor = 'col-resize'
    document.body.style.userSelect = 'none'
    window.addEventListener('mousemove', onMove)
    window.addEventListener('mouseup', onUp)
  }, [width, onWidthChange])
  const [hoveredBg, setHoveredBg] = useState<string | null>(null)
  const [expandedScheduleGroups, setExpandedScheduleGroups] = useState<Set<string>>(() => {
    try {
      const stored = localStorage.getItem('termyard:expanded-schedule-groups')
      if (stored) return new Set(JSON.parse(stored))
    } catch {}
    return new Set()
  })
  const [collapsedHosts, setCollapsedHosts] = useState<Set<string>>(() => {
    try {
      const stored = localStorage.getItem('termyard:collapsed-hosts')
      if (stored) return new Set(JSON.parse(stored))
    } catch {}
    return new Set()
  })
  const toggleScheduleExpanded = useCallback((id: string) => {
    setExpandedScheduleGroups(prev => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      try { localStorage.setItem('termyard:expanded-schedule-groups', JSON.stringify([...next])) } catch {}
      return next
    })
  }, [])
  const toggleHostCollapsed = useCallback((id: string) => {
    setCollapsedHosts(prev => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      try { localStorage.setItem('termyard:collapsed-hosts', JSON.stringify([...next])) } catch {}
      return next
    })
  }, [])
  const filterRef = useRef<HTMLDivElement>(null)
  const touchTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  useEffect(() => {
    if (!menu && !filterOpen) return
    const handler = (event: MouseEvent) => {
      const target = event.target as Node | null
      if (filterOpen && target && filterRef.current?.contains(target)) return
      setMenu(null)
      setFilterOpen(false)
    }
    window.addEventListener('click', handler)
    return () => window.removeEventListener('click', handler)
  }, [menu, filterOpen])

  useEffect(() => {
    writeStoredList('termyard:project-filters', projectFilters)
  }, [projectFilters])

  const projects = useMemo(
    () => Array.from(new Set(sessions.map(s => s.cwd).filter((value): value is string => Boolean(value)))).sort(),
    [sessions],
  )

  useEffect(() => {
    if (projectFilters.length === 0 || pruningSuspended) return
    const validProjects = new Set(projects)
    const nextFilters = projectFilters.filter(project => validProjects.has(project))
    if (nextFilters.length !== projectFilters.length) {
      setProjectFilters(nextFilters)
    }
  }, [projectFilters, projects, pruningSuspended])

  // Signal per session, computed once and reused for both ordering and badges.
  const signalOf = useCallback(
    (session: SessionView) => sessionViewSignal(session, getSessionEvents(session.key), isSessionInActiveTurn(session.key)),
    [getSessionEvents, isSessionInActiveTurn],
  )

  // Deterministic order: needs-attention first, then working, idle, offline;
  // within each bucket, newest first, then by key.
  const orderedSessions = useMemo(() => {
    return [...sessions].sort((a, b) => {
      const aState = signalOf(a).state
      const bState = signalOf(b).state
      if (aState !== bState) return stateRank[aState] - stateRank[bState]
      const at = a.createdAt || ''
      const bt = b.createdAt || ''
      if (at !== bt) return bt.localeCompare(at)
      return a.key.localeCompare(b.key)
    })
  }, [sessions, signalOf])

  const visibleSessions = useMemo(() => {
    const filtered = orderedSessions.filter(session => !hiddenSet.has(session.key) && !backgroundSet.has(session.key))
    if (projectFilters.length === 0) return filtered
    const allowed = new Set(projectFilters)
    return filtered.filter(session => session.cwd && allowed.has(session.cwd))
  }, [orderedSessions, hiddenSet, backgroundSet, projectFilters])

  // Scheduled sessions render in their own pinned footer block, not inline.
  const mainSessions = useMemo(() => visibleSessions.filter(session => !session.scheduleId), [visibleSessions])
  const hiddenSessions = orderedSessions.filter(session => hiddenSet.has(session.key))
  const backgroundSessions = orderedSessions.filter(session => backgroundSet.has(session.key))

  const toggleBackground = (key: string) => {
    setSessionAttr(key, { background: !backgroundSet.has(key) })
    setMenu(null)
  }

  const openMenu = (session: SessionView, x: number, y: number) => {
    setMenu({ target: { key: session.key, label: session.label, worktreeBranch: session.worktreeBranch }, x, y })
  }

  const handleTouchStart = (session: SessionView) => (e: React.TouchEvent) => {
    const touch = e.touches[0]
    const x = touch.clientX
    const y = touch.clientY
    touchTimerRef.current = setTimeout(() => {
      touchTimerRef.current = null
      openMenu(session, x, y)
    }, 600)
  }

  const handleTouchEnd = () => {
    if (touchTimerRef.current !== null) {
      clearTimeout(touchTimerRef.current)
      touchTimerRef.current = null
    }
  }

  type HostBucket = {
    hostId: string
    name: string
    online: boolean
    sessions: SessionView[]
  }

  const hostGroups = useMemo((): HostBucket[] => {
    if (!hasMultipleHosts) return []
    const localBucketId = localHostId ?? ''
    const buckets = new Map<string, HostBucket>()

    const ensureBucket = (hostId: string, name: string) => {
      const existing = buckets.get(hostId)
      if (existing) return existing
      const bucket: HostBucket = { hostId, name, online: false, sessions: [] }
      buckets.set(hostId, bucket)
      return bucket
    }

    const hostNameFor = (hostId: string, fallback?: string) => (
      hostId === localBucketId
        ? 'This machine'
        : hosts?.find(host => host.peer_id === hostId || host.owner_id === hostId)?.name ?? fallback ?? hostId
    )

    for (const session of mainSessions) {
      const hostId = session.isLocal ? localBucketId : session.ownerId
      const bucket = ensureBucket(hostId, hostNameFor(hostId, session.host?.name))
      bucket.sessions.push(session)
      bucket.online ||= session.hostOnline
    }

    // Surface connected online peers even with zero sessions, so the list
    // confirms a machine is linked rather than hiding idle peers. Offline
    // idle peers stay hidden to avoid clutter.
    for (const host of hosts ?? []) {
      if (host.local || host.peer_id === localBucketId || !host.online) continue
      ensureBucket(host.peer_id, hostNameFor(host.peer_id, host.name)).online = true
    }

    const localBucket = buckets.get(localBucketId)
    const remoteBuckets = Array.from(buckets.values())
      .filter(bucket => bucket.hostId !== localBucketId)
      .sort((a, b) => a.name.localeCompare(b.name))

    return [
      ...(localBucket && localBucket.sessions.length > 0 ? [localBucket] : []),
      ...remoteBuckets,
    ]
  }, [hasMultipleHosts, hosts, localHostId, mainSessions])

  // Schedule groups render in a dedicated pinned block above the Hidden section,
  // out of the scrolling session list -- recurring/background work, not active.
  const scheduleGroups = useMemo(() => {
    const groups: { scheduleId: string; schedule: (typeof schedules)[number] | undefined; sessions: SessionView[] }[] = []
    const seen = new Set<string>()
    for (const session of visibleSessions) {
      const scheduleId = session.scheduleId
      if (!scheduleId || seen.has(scheduleId)) continue
      seen.add(scheduleId)
      const scheduleSessions = visibleSessions
        .filter(item => item.scheduleId === scheduleId)
        .sort((a, b) => (b.createdAt || '').localeCompare(a.createdAt || ''))
      groups.push({ scheduleId, schedule: scheduleById.get(scheduleId), sessions: scheduleSessions })
    }
    return groups
  }, [visibleSessions, scheduleById])

  const renderSessionItem = (session: SessionView, isHiddenSection = false, inHostGroup = false) => {
    const sk = session.key
    const isSelected = selectedSession === sk
    const needsAttention = sessionNeedsAttention(sk)
    const events = getSessionEvents(sk)
    const signal = signalOf(session)
    const isOffline = signal.state === 'offline'
    const stripeColor = hasMultipleHosts && !session.isLocal ? hostColor(session.ownerId, localHostId) : null
    const hostLabel = stripeColor ? (session.host?.name ?? session.ownerId ?? 'remote') : null
    const activeEvent = events.find(e => e.status === 'active' && !e.auto_detected)
    const activityLabel = activeEvent?.message
    const activityIsLive = !!activityLabel
    const activityDisplay = activityLabel
      ?? (signal.state === 'needs_you' ? 'Waiting for input'
        : signal.state === 'working' ? 'Working...'
        : signal.state === 'offline' ? 'Offline'
        : 'Idle')
    const projectName = pathLeaf(session.cwd)
    const badge = STATE_BADGE[signal.state]

    return (
      <li key={sk} data-session-key={sk}>
        <div
          role="button"
          tabIndex={0}
          {...glancePreview.trigger({ name: session.id, host: session.ownerId, display_name: session.displayName, host_name: session.host?.name })}
          onClick={() => onSessionSelect(session)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' || e.key === ' ') {
              e.preventDefault()
              onSessionSelect(session)
            }
          }}
          onContextMenu={(e) => {
            e.preventDefault()
            openMenu(session, e.clientX, e.clientY)
          }}
          onTouchStart={handleTouchStart(session)}
          onTouchEnd={handleTouchEnd}
          onTouchMove={handleTouchEnd}
          className={cn(
            'relative flex flex-col w-full p-2.5 rounded-sm transition-all duration-200 text-ink',
            'hover:bg-white/[0.05]',
            isSelected && 'bg-white/[0.08] !text-primary border border-white/20',
            needsAttention && !isSelected && 'border-l border-warning bg-warning/5',
            !isSelected && !needsAttention && 'border border-transparent',
            (isHiddenSection || isOffline) && 'opacity-60',
          )}
        >
          {collapsed && stripeColor && (
            <span
              className="absolute top-1 right-1 w-2 h-2 rounded-full pointer-events-none"
              style={{ backgroundColor: stripeColor }}
              title={hostLabel ? `Host: ${hostLabel}` : undefined}
              aria-label={hostLabel ? `Host: ${hostLabel}` : 'remote host'}
            />
          )}
          <div className="flex items-center gap-2 w-full">
            {!collapsed && session.agentType && <AgentMark agentType={session.agentType} className="h-3.5 w-3.5 shrink-0" />}
            {!collapsed && stripeColor && !inHostGroup && (
              <span
                className="w-2 h-2 rounded-full shrink-0 pointer-events-none"
                style={{ backgroundColor: stripeColor }}
                title={hostLabel ? `Host: ${hostLabel}` : undefined}
                aria-label={hostLabel ? `Host: ${hostLabel}` : 'remote host'}
              />
            )}
            <span className="flex-1 flex items-baseline gap-1 min-w-0 overflow-hidden text-left">
              {!collapsed && session.worktreeBranch && (
                <span
                  className="shrink min-w-0 truncate text-[12px] font-medium tracking-tight text-mute/40"
                  title={session.worktreeBranch}
                >
                  {session.worktreeBranch}
                </span>
              )}
              {!collapsed && session.worktreeBranch && (
                <svg
                  className="shrink-0 self-center text-primary/50"
                  width="11" height="11" viewBox="0 0 24 24" fill="none"
                  stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"
                  aria-hidden
                >
                  <line x1="6" x2="6" y1="3" y2="15" />
                  <circle cx="18" cy="6" r="3" />
                  <circle cx="6" cy="18" r="3" />
                  <path d="M18 9a9 9 0 0 1-9 9" />
                </svg>
              )}
              {!collapsed && projectName && (
                <span
                  className="shrink min-w-0 truncate text-[12px] font-medium tracking-tight text-mute/60"
                  title={session.cwd}
                >
                  {projectName}<span className="text-mute/30">/</span>
                </span>
              )}
              <span
                className={cn(
                  'shrink-0 max-w-full text-[12px] font-medium tracking-tight overflow-hidden text-ellipsis whitespace-nowrap',
                  isSelected && '!text-primary',
                )}
                title={session.label !== session.id ? `${session.label} (${session.id})` : session.id}
              >
                {collapsed ? session.label.charAt(0).toUpperCase() : session.label}
              </span>
            </span>
            {!collapsed && (
              <span className="shrink-0 text-[10px] text-mute/50 font-medium tabular-nums" title={`Uptime: ${formatUptime(session.createdAt)}`}>
                {formatUptime(session.createdAt)}
              </span>
            )}
          </div>

          {!collapsed && (
            <div className="mt-1 flex items-center gap-1.5 min-w-0">
              <span className={cn('min-w-0 truncate text-[10px]', activityIsLive ? 'text-mute/70' : 'text-mute/40')} title={activityDisplay}>
                {activityDisplay}
              </span>
              <span
                className={cn('shrink-0 ml-auto text-[9px] leading-none font-medium px-1.5 py-0.5 rounded-xs tabular-nums', badge.pulse && 'animate-[pulse_1.5s_ease-in-out_infinite]')}
                style={{ color: badge.color, background: badge.bg }}
              >
                {badge.label}
              </span>
            </div>
          )}
        </div>
      </li>
    )
  }

  const renderScheduleItem = (scheduleId: string, scheduleSessions: SessionView[], schedule?: (typeof schedules)[number]) => {
    const latest = scheduleSessions[0]
    const isExpanded = expandedScheduleGroups.has(scheduleId)
    const maxExpandedChildren = 6
    // Collapsed groups hide ALL runs; the indicator below surfaces count + state.
    const childSessions = isExpanded ? scheduleSessions.slice(0, maxExpandedChildren) : []
    const overflow = isExpanded && scheduleSessions.length > maxExpandedChildren ? scheduleSessions.length - maxExpandedChildren : 0
    const latestSignal = latest ? signalOf(latest) : null
    const latestColor = latestSignal ? STATE_BADGE[latestSignal.state].color : STATE_BADGE.idle.color
    const attentionCount = scheduleSessions.filter(s => signalOf(s).state === 'needs_you').length
    const sessionHost = latest?.ownerId || ''
    // Definition unknown locally: the schedule lives on another peer (its runs may
    // be local here or on another host) or was removed. Without registry sync we
    // cannot tell deleted from defined-elsewhere, so never claim 'deleted' -- render
    // neutrally by host reachability. ponytail: upgrade to real enabled/paused
    // state when schedule defs sync peer-to-peer (2b).
    const known = !!schedule
    const enabled = schedule?.enabled ?? true
    const host = schedule?.host || sessionHost
    const hostOnline = !host || hosts?.some(item => item.peer_id === host && item.online)
    const stateLabel = known ? (!enabled ? 'paused' : !hostOnline ? 'peer offline' : 'active') : (!hostOnline ? 'peer offline' : 'scheduled')
    const neutralColor = 'text-mute/70 border-hairline bg-surface-elevated/70'
    const stateColor = !known ? neutralColor : !enabled ? 'text-amber-400 border-amber-400/30 bg-amber-400/10' : !hostOnline ? neutralColor : 'text-emerald-400 border-emerald-400/30 bg-emerald-400/10'
    const scheduleName = schedule?.name || (latest?.id ? latest.id.replace(/-\d+$/, '') : '') || scheduleId
    return (
      <li key={`schedule:${scheduleId}`} data-schedule-id={scheduleId}>
        <div className={cn('rounded-sm border border-hairline bg-surface/70 overflow-hidden', !enabled && 'opacity-75')}>
          <button
            type="button"
            onClick={() => toggleScheduleExpanded(scheduleId)}
            className="w-full text-left px-2.5 py-2 flex items-start gap-2 transition-colors hover:bg-white/[0.05]"
          >
            <span className="text-[11px] text-mute/70 font-mono pt-0.5 shrink-0">{isExpanded ? '\u25be' : '\u25b8'}</span>
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2 min-w-0">
                <span className="text-[12px] font-semibold text-ink truncate">{scheduleName}</span>
                <span className={cn('shrink-0 text-[10px] font-bold px-1.5 py-0.5 rounded-xs border uppercase tracking-widest', stateColor)}>
                  {stateLabel}
                </span>
              </div>
              <div className="mt-0.5 text-[10px] text-mute/60 flex items-center gap-1.5">
                <span className="truncate" title={schedule?.cronSpec}>
                  {(schedule?.cronSpec ? describeCron(schedule.cronSpec) : null) ?? schedule?.cronSpec ?? '\u2014'}  \u00b7  next {formatRelativeTime(schedule?.nextRun)}  \u00b7  {schedule?.runCount ?? scheduleSessions.length} runs
                </span>
                <span className="shrink-0 inline-flex items-center gap-1" title={`${scheduleSessions.length} session${scheduleSessions.length === 1 ? '' : 's'}, latest ${latestSignal?.state ?? 'idle'}`}>
                  <span className="w-1.5 h-1.5 rounded-full" style={{ background: latestColor }} />
                  {scheduleSessions.length}
                  {attentionCount > 0 && <span className="text-accent-red font-semibold">{attentionCount}\u26a0</span>}
                </span>
              </div>
            </div>
          </button>
          {childSessions.length > 0 && (
            <div className="px-1.5 pb-1.5 pl-5 space-y-0.5">
              {childSessions.map((session) => renderSessionItem(session, false))}
              {overflow > 0 && (
                <div className="px-2 pt-1 text-[10px] text-mute/60 font-medium">+{overflow} more</div>
              )}
            </div>
          )}
        </div>
      </li>
    )
  }

  const isHidden = collapsed && collapseMode === 'hidden'
  const filterLabel = projectFilters.length === 0 ? 'All projects' : `${projectFilters.length} projects`

  return (
    <aside
      style={!collapsed ? { width: Math.max(width, 260), minWidth: 260 } : undefined}
      className={cn(
      'relative flex flex-col h-full bg-canvas font-sans text-sm font-medium',
      !resizing && 'transition-[width] duration-300',
      collapsed
        ? collapseMode === 'hidden' ? 'w-0 overflow-hidden' : 'w-16'
        : '',
      !isHidden && 'border-r border-hairline',
    )}>
      {glancePreview.popover}
      {!collapsed && (
        <div
          onMouseDown={startResize}
          className={cn(
            'absolute top-0 right-0 z-20 h-full w-1 cursor-col-resize hover:bg-primary/40',
            resizing && 'bg-primary/60',
          )}
        />
      )}
      {!collapsed && (
        <div className="px-2 pt-2" ref={filterRef}>
          <div className="flex items-center gap-1.5">
            <button
              type="button"
              onClick={() => setFilterOpen(value => !value)}
              className="flex-1 min-w-0 rounded-md border border-hairline bg-surface-elevated px-3 py-2 text-left text-xs text-mute hover:text-ink font-medium transition-colors truncate"
            >
              {filterLabel}
            </button>
            {crashedCount > 0 && (
              <button
                type="button"
                onClick={onCrashedClick}
                title={`${crashedCount} crashed session${crashedCount === 1 ? '' : 's'} \u2014 click to recover`}
                className="shrink-0 rounded-md border px-2 py-2 transition-colors flex items-center gap-1"
                style={{ borderColor: 'var(--accent-red)', background: 'rgba(255,97,97,0.12)', color: 'var(--accent-red)' }}
                onMouseEnter={(e) => { (e.currentTarget as HTMLElement).style.background = 'rgba(255,97,97,0.22)' }}
                onMouseLeave={(e) => { (e.currentTarget as HTMLElement).style.background = 'rgba(255,97,97,0.12)' }}
              >
                <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                  <path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z" />
                  <line x1="12" y1="9" x2="12" y2="13" />
                  <line x1="12" y1="17" x2="12.01" y2="17" />
                </svg>
                <span style={{ fontSize: '10px', fontWeight: 700 }}>{crashedCount}</span>
              </button>
            )}
            {onQuickShell && (
              <button
                type="button"
                onClick={onQuickShell}
                title="Quick Shell"
                className="shrink-0 rounded-md border border-hairline bg-surface-elevated px-2 py-2 text-mute hover:text-ink transition-colors"
              >
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                  <polyline points="4 17 10 11 4 5" />
                  <line x1="12" y1="19" x2="20" y2="19" />
                </svg>
              </button>
            )}
            {onToggleCollapse && (
              <button
                type="button"
                onClick={onToggleCollapse}
                title="Collapse sidebar"
                className="shrink-0 rounded-md border border-hairline bg-surface-elevated px-2 py-2 text-mute hover:text-ink transition-colors"
              >
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                  <rect x="3" y="3" width="18" height="18" rx="2" /><path d="M9 3v18" /><path d="M15 9l-3 3 3 3" />
                </svg>
              </button>
            )}
          </div>
          {filterOpen && (
            <div className="mt-1 rounded-lg border border-hairline bg-surface p-2">
              <label className="flex items-center gap-2 px-1 py-1 text-xs text-ink font-medium">
                <input
                  type="checkbox"
                  checked={projectFilters.length === 0}
                  onChange={() => setProjectFilters([])}
                  className="rounded-xs border-hairline bg-surface-elevated"
                />
                All projects
              </label>
              <div className="max-h-48 overflow-y-auto">
                {projects.map(project => (
                  <label key={project} className="flex items-center gap-2 px-1 py-1 text-xs text-ink font-medium" title={project}>
                    <input
                      type="checkbox"
                      checked={projectFilters.includes(project)}
                      onChange={() => {
                        setProjectFilters(current => current.includes(project)
                          ? current.filter(value => value !== project)
                          : [...current, project])
                      }}
                      className="rounded-xs border-hairline bg-surface-elevated"
                    />
                    <span className="truncate">{pathLeaf(project)}</span>
                  </label>
                ))}
              </div>
            </div>
          )}
        </div>
      )}

      <nav className="flex-1 overflow-y-auto p-2">
        <ul className="space-y-0.5">
          {mainSessions.length === 0 && (
            <li className="p-3 text-mute text-sm">
              {collapsed ? '\u2014' : 'No sessions'}
            </li>
          )}

          {hasMultipleHosts && !collapsed ? (
            hostGroups.map(hostGroup => {
              const open = !collapsedHosts.has(hostGroup.hostId)
              return (
                <li
                  key={`host:${hostGroup.hostId}`}
                  data-host-id={hostGroup.hostId}
                  className={cn('flex flex-col rounded-sm', !hostGroup.online && 'opacity-75')}
                >
                  <button
                    type="button"
                    onClick={() => toggleHostCollapsed(hostGroup.hostId)}
                    className="w-full flex items-center gap-2 px-2.5 py-2 text-left rounded-sm bg-white/[0.04] transition-colors hover:bg-white/[0.07]"
                  >
                    <span className="text-[10px] font-mono text-mute/60 shrink-0 w-3">
                      {open ? '\u25be' : '\u25b8'}
                    </span>
                    <span className="text-[11px] font-medium truncate flex-1 text-left">
                      {hostGroup.name}
                    </span>
                    {!hostGroup.online && (
                      <span className="text-[9px] font-semibold uppercase tracking-widest rounded-xs border border-hairline px-1.5 py-0.5 text-mute/50">
                        offline
                      </span>
                    )}
                    <span className="text-[10px] font-mono text-mute/50 shrink-0">
                      \u00b7 {hostGroup.sessions.length}
                    </span>
                  </button>
                  {open && (hostGroup.sessions.length > 0 ? (
                    <ul className="space-y-0.5 pl-1">
                      {hostGroup.sessions.map(session => renderSessionItem(session, false, true))}
                    </ul>
                  ) : (
                    <p className="pl-6 pr-2.5 pb-2 text-[11px] text-mute/40 select-none">no sessions</p>
                  ))}
                </li>
              )
            })
          ) : (
            mainSessions.map(session => renderSessionItem(session, false))
          )}
        </ul>
      </nav>

      {scheduleGroups.length > 0 && !collapsed && (
        <div className="border-t border-hairline bg-canvas shrink-0">
          <button
            type="button"
            onClick={toggleScheduledExpanded}
            className="w-full px-3 py-1.5 text-[10px] uppercase tracking-widest font-semibold text-mute/60 select-none flex items-center gap-1 hover:text-mute transition-colors"
          >
            <span
              className="inline-block transition-transform duration-150"
              style={{ transform: scheduledExpanded ? 'rotate(90deg)' : 'rotate(0deg)' }}
            >
              \u25b6
            </span>
            Scheduled ({scheduleGroups.length})
          </button>
          {scheduledExpanded && (
            <ul className="px-2 pb-2 space-y-1">
              {scheduleGroups.map(g => renderScheduleItem(g.scheduleId, g.sessions, g.schedule))}
            </ul>
          )}
        </div>
      )}

      {hiddenSessions.length > 0 && !collapsed && (
        <div className="border-t border-hairline bg-canvas shrink-0">
          <button
            type="button"
            onClick={() => setHiddenExpanded(!hiddenExpanded)}
            className="w-full px-3 py-1.5 text-[10px] uppercase tracking-widest font-semibold text-mute/60 select-none flex items-center gap-1 hover:text-mute transition-colors"
          >
            <span
              className="inline-block transition-transform duration-150"
              style={{ transform: hiddenExpanded ? 'rotate(90deg)' : 'rotate(0deg)' }}
            >
              \u25b6
            </span>
            Hidden ({hiddenSessions.length})
          </button>
          {hiddenExpanded && (
            <ul className="px-2 pb-2 space-y-0.5">
              {hiddenSessions.map(session => renderSessionItem(session, true))}
            </ul>
          )}
        </div>
      )}

      {backgroundSessions.length > 0 && !collapsed && (
        <div className="border-t border-hairline bg-canvas shrink-0">
          <div className="px-3 py-1.5 text-[10px] uppercase tracking-widest font-semibold text-mute/60 select-none">
            Background
          </div>
          <ul className="px-2 pb-2 space-y-0.5">
            {backgroundSessions.map(session => {
              const sk = session.key
              const isSelected = selectedSession === sk
              const signal = signalOf(session)
              const active = signal.state === 'working'
              return (
                <li key={sk}>
                  <div
                    role="button"
                    tabIndex={0}
                    onClick={() => onSessionSelect(session)}
                    onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onSessionSelect(session) } }}
                    onMouseEnter={() => setHoveredBg(sk)}
                    onMouseLeave={() => setHoveredBg(null)}
                    onContextMenu={(e) => {
                      e.preventDefault()
                      openMenu(session, e.clientX, e.clientY)
                    }}
                    className={cn(
                      'relative flex items-center gap-2 w-full px-2.5 py-1 rounded-sm transition-all duration-200 min-w-0',
                      'hover:bg-white/[0.05] cursor-pointer',
                      isSelected && 'bg-white/[0.08] !text-primary border border-white/20',
                      !isSelected && 'border border-transparent',
                    )}
                  >
                    <span
                      className={cn(
                        'w-1.5 h-1.5 rounded-full shrink-0 transition-colors',
                        active
                          ? 'bg-success animate-[pulse_1.5s_ease-in-out_infinite]'
                          : 'bg-muted-foreground/40',
                      )}
                      title={active ? 'working' : 'idle'}
                    />
                    <span className="text-[12px] font-medium tracking-tight shrink-0 text-mute">
                      {session.label}
                    </span>
                    {hoveredBg === sk && (
                      <button
                        type="button"
                        onClick={(e) => { e.stopPropagation(); toggleBackground(sk) }}
                        title="Bring to foreground"
                        className="shrink-0 ml-auto flex items-center justify-center w-5 h-5 rounded-xs hover:bg-surface-card text-mute hover:text-ink transition-colors"
                      >
                        <svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
                          <path d="M6 1.5v9M2.5 6l3.5-3.5L9.5 6" />
                        </svg>
                      </button>
                    )}
                  </div>
                </li>
              )
            })}
          </ul>
        </div>
      )}

      {!collapsed && (
        <div className="mt-auto shrink-0 border-t border-hairline px-3 py-1.5 text-[11px] font-mono text-mute/60 flex items-center gap-1.5 whitespace-nowrap overflow-hidden">
          <span>{sessions.length} session{sessions.length === 1 ? '' : 's'}</span>
          {glance && <><span>\u00b7</span><span>{glance.working} working</span></>}
          {glance && glance.needsYou > 0 && <><span>\u00b7</span><span className="text-warning font-bold">{glance.needsYou} waiting</span></>}
          {selfUpdateAvailable && (
            <span className="ml-auto rounded-full border border-warning/40 bg-warning/10 px-2 py-0.5 text-[10px] font-bold text-warning">update</span>
          )}
        </div>
      )}

      {collapsed && onToggleCollapse && (
        <button
          type="button"
          onClick={onToggleCollapse}
          title="Expand sidebar"
          className="fixed left-2 top-14 z-30 p-1.5 rounded-md border border-hairline bg-surface-elevated/60 backdrop-blur-sm text-mute hover:text-ink hover:bg-surface-elevated transition-colors"
        >
          <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
            <rect x="3" y="3" width="18" height="18" rx="2" /><path d="M9 3v18" /><path d="M13 9l3 3-3 3" />
          </svg>
        </button>
      )}

      {menu && (
        <SessionActionsMenu
          target={menu.target}
          x={menu.x}
          y={menu.y}
          hiddenSet={hiddenSet}
          backgroundSet={backgroundSet}
          setSessionAttr={setSessionAttr}
          onSessionKilled={onSessionKilled}
          onClose={() => setMenu(null)}
          onRenameSession={onRenameSession}
        />
      )}
    </aside>
  )
}
