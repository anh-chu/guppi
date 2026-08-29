import { useState, useEffect, useLayoutEffect, useMemo, useRef, useCallback, Fragment } from 'react'
import type { MouseEvent as ReactMouseEvent } from 'react'
import { generateKeyBetween } from 'fractional-indexing'
import { Session, sessionKey, sessionLabel, sessionScheduleID } from '../hooks/useSessions'
import type { SessionAttrSets } from '../hooks/useSessionAttrs'
import { Host } from '../hooks/useHosts'
import { ToolEvent } from '../hooks/useToolEvents'
import { ActivitySnapshot } from '../hooks/useActivity'
import { useSchedules } from '../hooks/useSchedules'
import { cn } from '../lib/utils'
import { renameSession, aiNameSession as aiNameSessionApi, killSession as killSessionApi } from '../lib/sessionActions'
import { describeCron } from '../lib/cron'
import { formatRelativeTime, formatUptime } from '../lib/time'
import { pathLeaf, projectLabel } from '../lib/path'
import { isToolSession, sessionProjection, LOUD_STATUSES } from '../lib/sessionState'
import { hostColor } from '../lib/hostColor'
import { AgentMark } from './AgentMark'
import { useGlance } from './GlancePopover'

function SparkleIcon({ spinning, size = 11 }: { spinning?: boolean; size?: number }) {
  if (spinning) {
    return (
      <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" className="animate-spin text-primary">
        <path d="M21 12a9 9 0 1 1-6.219-8.56" />
      </svg>
    )
  }
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="currentColor">
      <path d="M12 2l1.6 5.2L19 9l-5.4 1.8L12 16l-1.6-5.2L5 9l5.4-1.8L12 2zM19 14l.8 2.6L22 17.5l-2.2.9L19 21l-.8-2.6L16 17.5l2.2-.9L19 14z" />
    </svg>
  )
}

interface SidebarProps {
  sessions: Session[]
  selectedSession: string | null
  collapsed: boolean
  selfUpdateAvailable?: boolean
  collapseMode: 'small' | 'hidden'
  width?: number
  onWidthChange?: (width: number) => void
  hasMultipleHosts?: boolean
  localHostId?: string
  hosts?: Host[]
  onSessionSelect: (session: Session) => void
  getSessionEvents: (session: string) => ToolEvent[]
  isSessionInActiveTurn: (session: string) => boolean
  isSessionRecentlyDone: (session: string) => boolean
  getSessionActivity: (session: string) => ActivitySnapshot | undefined
  agentCount?: number
  glance?: { parked: number; working: number; waiting: number }
  onToggleCollapse?: () => void
  layoutGroups?: { id: string; leaves: string[]; isActive: boolean; activeKey: string | null; name: string | undefined }[]
  sessionOrderRanks: Record<string, string>
  setSessionOrderRank: (key: string, rank: string) => void
  onSwitchGroup?: (groupId: string, focusKey?: string) => void
  onRenameGroup?: (groupId: string, name: string) => void
  forceAiName?: (groupId: string) => Promise<boolean>
  namingGroupId?: string | null
  onPairSessions?: (keyA: string, keyB: string) => void
  onSessionKilled?: (key: string) => void
  sessionAttrs: SessionAttrSets
  setSessionAttr: (key: string, next: { background?: boolean; hidden?: boolean }) => void
  backgroundSession: (key: string, next: boolean) => Promise<void>
  // True while the session list is still converging after a WS (re)connect.
  // Pruning per-device ordering then would delete entries for sessions that
  // simply haven't reappeared yet.
  filterProtectionActive?: boolean
  // Direct PTY session keys currently in the pane tree

  onQuickShell?: () => void
}

interface RenameState {
  key: string
  name: string
  label: string
  host?: string
}

const statusBadgeConfig = {
  working: { label: 'working', color: 'var(--accent-green)',  bg: 'rgba(89,212,153,0.12)',  pulse: true  },
  waiting: { label: 'waiting', color: 'var(--accent-yellow)', bg: 'rgba(255,197,51,0.12)',  pulse: true  },
  stuck:   { label: 'stuck',   color: 'var(--accent-red)',    bg: 'rgba(255,97,97,0.12)',   pulse: false },
  error:   { label: 'error',   color: 'var(--accent-red)',    bg: 'rgba(255,97,97,0.12)',   pulse: false },
  idle:    { label: 'idle',    color: 'var(--mute)',          bg: 'transparent',            pulse: false },
  process: { label: 'process', color: 'var(--accent-blue)',   bg: 'rgba(87,193,255,0.12)',  pulse: false },
  shell:   { label: 'shell',   color: 'var(--mute)',          bg: 'transparent',            pulse: false },
} as const

type StatusBadge = keyof typeof statusBadgeConfig

const STATUS_BUCKETS: { id: string; label: string; statuses: StatusBadge[] }[] = [
  { id: 'attention', label: 'Needs attention', statuses: ['error', 'stuck', 'waiting'] },
  { id: 'working',   label: 'Working',         statuses: ['working'] },
  { id: 'idle',      label: 'Idle',            statuses: ['idle'] },
  { id: 'shell',     label: 'Shell',           statuses: ['shell'] },
  { id: 'process',   label: 'Process',         statuses: ['process'] },
]


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


function sortNewestFirst<T extends { created?: string }>(items: T[]): T[] {
  return [...items].sort((a, b) => {
    const aTime = new Date(a.created || 0).getTime()
    const bTime = new Date(b.created || 0).getTime()
    return bTime - aTime
  })
}

function orderSessions(sessions: Session[], ranks: Record<string, string>): Session[] {
  return [...sessions].sort((a, b) => {
    const aKey = sessionKey(a)
    const bKey = sessionKey(b)
    const aRank = ranks[aKey]
    const bRank = ranks[bKey]
    const aHas = aRank !== undefined && aRank !== ''
    const bHas = bRank !== undefined && bRank !== ''
    if (aHas && bHas) {
      if (aRank !== bRank) return aRank.localeCompare(bRank)
      return aKey.localeCompare(bKey)
    }
    if (aHas) return -1
    if (bHas) return 1
    const byName = a.name.localeCompare(b.name)
    return byName !== 0 ? byName : aKey.localeCompare(bKey)
  })
}

export function Sidebar({
  sessions,
  selectedSession,
  collapsed: persistentCollapsed,
  selfUpdateAvailable,
  collapseMode,
  width = 288,
  onWidthChange,
  hasMultipleHosts,
  localHostId,
  hosts,
  onSessionSelect,
  getSessionEvents,
  isSessionInActiveTurn,
  isSessionRecentlyDone,
  getSessionActivity,
  agentCount = 0,
  glance,
  onToggleCollapse,
  layoutGroups,
  sessionOrderRanks,
  setSessionOrderRank,
  onSwitchGroup,
  onRenameGroup,
  forceAiName,
  namingGroupId,
  onPairSessions,
  onSessionKilled,
  sessionAttrs,
  setSessionAttr,
  backgroundSession,
  filterProtectionActive,

  onQuickShell,
}: SidebarProps) {
  const [hoverExpanded, setHoverExpanded] = useState(false)
  const hoverLeaveTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  // Guards the resize drag: while resizing, the pointer routinely leaves the
  // aside bounds, which must NOT collapse a hover-expanded overlay mid-drag.
  const resizingRef = useRef(false)
  // Hover-expand is a desktop affordance. On touch devices a tap fires a
  // synthetic mouseenter, which would silently hover-expand a collapsed rail
  // and desync the collapse toggle (button then appears to do nothing).
  const canHoverExpand = useMemo(() =>
    typeof window !== 'undefined' &&
    window.matchMedia('(hover: hover) and (pointer: fine)').matches, [])
  const collapsed = persistentCollapsed && !(collapseMode === 'small' && hoverExpanded)
  const handleSidebarMouseEnter = useCallback(() => {
    if (!canHoverExpand) return
    if (hoverLeaveTimer.current !== null) {
      clearTimeout(hoverLeaveTimer.current)
      hoverLeaveTimer.current = null
    }
    if (persistentCollapsed && collapseMode === 'small') setHoverExpanded(true)
  }, [canHoverExpand, persistentCollapsed, collapseMode])
  const handleSidebarMouseLeave = useCallback((event: ReactMouseEvent<HTMLElement>) => {
    if (resizingRef.current) return
    if (hoverLeaveTimer.current !== null) clearTimeout(hoverLeaveTimer.current)
    hoverLeaveTimer.current = setTimeout(() => {
      hoverLeaveTimer.current = null
      setHoverExpanded(false)
    }, event.clientX <= 4 ? 600 : 220)
  }, [])
  useEffect(() => () => {
    if (hoverLeaveTimer.current !== null) clearTimeout(hoverLeaveTimer.current)
  }, [])
  const glancePreview = useGlance(!!hasMultipleHosts)
  const { schedules } = useSchedules()
  // background/hidden are SERVER-AUTHORITATIVE and arrive via props. They are
  // NOT cached in localStorage — the server owns the truth and broadcasts
  // session-attrs-updated, which App refetches and passes back down here.
  const hiddenSet = sessionAttrs.hidden
  const backgroundSet = sessionAttrs.background
  const scheduleIDs = sessionAttrs.scheduleIDs
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
  const [renamingSession, setRenamingSession] = useState<RenameState | null>(null)
  const [renameValue, setRenameValue] = useState('')
  const [contextMenu, setContextMenu] = useState<{ key: string; id: string; name: string; label: string; host?: string; isWorktree?: boolean; x: number; y: number } | null>(null)
  const menuRef = useRef<HTMLDivElement | null>(null)
  const [confirmKillKey, setConfirmKillKey] = useState<string | null>(null)
  const [confirmWorktreeKillKey, setConfirmWorktreeKillKey] = useState<string | null>(null)
  const [filterOpen, setFilterOpen] = useState(false)
  const [searchQuery, setSearchQuery] = useState('')
  const [viewMode, setViewMode] = useState<'project' | 'status'>(() =>
    localStorage.getItem('termyard:view-mode') === 'status' ? 'status' : 'project')
  useEffect(() => { localStorage.setItem('termyard:view-mode', viewMode) }, [viewMode])
  const [collapsedProjects, setCollapsedProjects] = useState<Set<string>>(() => {
    try {
      const stored = localStorage.getItem('termyard:collapsed-projects')
      if (stored) return new Set(JSON.parse(stored))
    } catch {}
    return new Set()
  })
  const toggleProjectCollapsed = useCallback((path: string) => {
    setCollapsedProjects(prev => {
      const next = new Set(prev)
      if (next.has(path)) next.delete(path)
      else next.add(path)
      try { localStorage.setItem('termyard:collapsed-projects', JSON.stringify([...next])) } catch {}
      return next
    })
  }, [])
  const [resizing, setResizing] = useState(false)
  const startResize = useCallback((e: React.MouseEvent) => {
    e.preventDefault()
    resizingRef.current = true
    setResizing(true)
    // A drag started from the hover overlay must stay expanded until release.
    if (hoverLeaveTimer.current !== null) {
      clearTimeout(hoverLeaveTimer.current)
      hoverLeaveTimer.current = null
    }
    const startX = e.clientX
    const startW = width
    const onMove = (ev: MouseEvent) => {
      const next = Math.min(560, Math.max(260, startW + (ev.clientX - startX)))
      onWidthChange?.(next)
    }
    const onUp = () => {
      resizingRef.current = false
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
  const [draggingKey, setDraggingKey] = useState<string | null>(null)
  const [pairTarget, setPairTarget] = useState<string | null>(null)
  const [dropIndicator, setDropIndicator] = useState<{ key: string; position: 'above' | 'below' } | null>(null)
  // Section (cwd group) reordering in project view — persisted order of section keys.
  const [draggingSectionKey, setDraggingSectionKey] = useState<string | null>(null)
  const [sectionDrop, setSectionDrop] = useState<{ key: string; position: 'above' | 'below' } | null>(null)
  const [projectOrder, setProjectOrder] = useState<string[]>(() => {
    try {
      const stored = localStorage.getItem('termyard:project-order')
      if (stored) return JSON.parse(stored)
    } catch {}
    return []
  })
  useEffect(() => {
    try { localStorage.setItem('termyard:project-order', JSON.stringify(projectOrder)) } catch {}
  }, [projectOrder])
  const [collapsedGroups, setCollapsedGroups] = useState<Set<string>>(() => {
    try {
      const stored = localStorage.getItem('termyard:collapsed-groups')
      if (stored) return new Set(JSON.parse(stored))
    } catch {}
    return new Set()
  })
  const [expandedScheduleGroups, setExpandedScheduleGroups] = useState<Set<string>>(() => {
    try {
      const stored = localStorage.getItem('termyard:expanded-schedule-groups')
      if (stored) return new Set(JSON.parse(stored))
    } catch {}
    return new Set()
  })
  const toggleGroupCollapsed = useCallback((id: string) => {
    setCollapsedGroups(prev => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      try { localStorage.setItem('termyard:collapsed-groups', JSON.stringify([...next])) } catch {}
      return next
    })
  }, [])
  const toggleScheduleExpanded = useCallback((id: string) => {
    setExpandedScheduleGroups(prev => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      try { localStorage.setItem('termyard:expanded-schedule-groups', JSON.stringify([...next])) } catch {}
      return next
    })
  }, [])
  const [, setUptimeTick] = useState(0)
  const [renamingGroupId, setRenamingGroupId] = useState<string | null>(null)
  const [groupRenameValue, setGroupRenameValue] = useState('')
  const [legacyAiGroupId, setLegacyAiGroupId] = useState<string | null>(null)
  const aiPendingGroupId = namingGroupId ?? legacyAiGroupId
  // Session keys (host/name) currently being AI-named, for the inline spinner.
  const [namingSessions, setNamingSessions] = useState<Set<string>>(new Set())
  const setNaming = (key: string, on: boolean) => setNamingSessions(prev => {
    const next = new Set(prev)
    if (on) next.add(key); else next.delete(key)
    return next
  })
  const renameInputRef = useRef<HTMLInputElement>(null)
  const groupRenameInputRef = useRef<HTMLInputElement>(null)
  const filterRef = useRef<HTMLDivElement>(null)
  const touchTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  useEffect(() => {
    if (renamingSession && renameInputRef.current) {
      renameInputRef.current.focus()
      renameInputRef.current.select()
    }
  }, [renamingSession])

  useEffect(() => {
    if (renamingGroupId && groupRenameInputRef.current) {
      groupRenameInputRef.current.focus()
      groupRenameInputRef.current.select()
    }
  }, [renamingGroupId])

  useEffect(() => {
    const id = window.setInterval(() => setUptimeTick(value => value + 1), 60_000)
    return () => window.clearInterval(id)
  }, [])

  useEffect(() => {
    if (!contextMenu && !filterOpen) return
    const handler = (event: MouseEvent) => {
      const target = event.target as Node | null
      if (filterOpen && target && filterRef.current?.contains(target)) return
      setContextMenu(null)
      setConfirmKillKey(null)
      setConfirmWorktreeKillKey(null)
      setFilterOpen(false)
    }
    window.addEventListener('click', handler)
    return () => window.removeEventListener('click', handler)
  }, [contextMenu, filterOpen])

  // Clamp context menu into viewport so it never clips off-screen.
  useLayoutEffect(() => {
    const el = menuRef.current
    if (!contextMenu || !el) return
    const pad = 8
    const rect = el.getBoundingClientRect()
    let left = contextMenu.x
    let top = contextMenu.y
    if (left + rect.width + pad > window.innerWidth) left = window.innerWidth - rect.width - pad
    if (top + rect.height + pad > window.innerHeight) top = window.innerHeight - rect.height - pad
    left = Math.max(pad, left)
    top = Math.max(pad, top)
    el.style.left = `${left}px`
    el.style.top = `${top}px`
  }, [contextMenu])

  useEffect(() => {
    writeStoredList('termyard:project-filters', projectFilters)
  }, [projectFilters])

  const projects = useMemo(
    () => Array.from(new Set(sessions.map(s => s.project_path).filter((value): value is string => Boolean(value)))).sort(),
    [sessions],
  )

  useEffect(() => {
    if (projectFilters.length === 0 || filterProtectionActive) return
    const validProjects = new Set(projects)
    const nextFilters = projectFilters.filter(project => validProjects.has(project))
    if (nextFilters.length !== projectFilters.length) {
      setProjectFilters(nextFilters)
    }
  }, [projectFilters, projects, filterProtectionActive])

  const orderedSessions = useMemo(() => orderSessions(sessions, sessionOrderRanks), [sessions, sessionOrderRanks])

  const visibleSessions = useMemo(() => {
    let filtered = orderedSessions.filter(session => !hiddenSet.has(sessionKey(session)) && !backgroundSet.has(sessionKey(session)))
    if (projectFilters.length > 0) {
      const allowed = new Set(projectFilters)
      filtered = filtered.filter(session => session.project_path && allowed.has(session.project_path))
    }
    const q = searchQuery.trim().toLowerCase()
    if (q) {
      filtered = filtered.filter(session =>
        sessionLabel(session).toLowerCase().includes(q) ||
        session.name.toLowerCase().includes(q) ||
        (session.project_path ?? '').toLowerCase().includes(q),
      )
    }
    return filtered
  }, [orderedSessions, hiddenSet, backgroundSet, projectFilters, searchQuery])

  const hiddenSessions = orderedSessions.filter(session => hiddenSet.has(sessionKey(session)))
  const backgroundSessions = orderedSessions.filter(session => backgroundSet.has(sessionKey(session)))

  const toggleHide = (key: string) => {
    // Server-authoritative: POST the desired bit; the response + WS broadcast
    // reconcile local state across tabs/peers.
    setSessionAttr(key, { hidden: !hiddenSet.has(key) })
    setContextMenu(null)
  }

  const toggleBackground = (key: string) => {
    const next = !backgroundSet.has(key)
    void backgroundSession(key, next)
    setContextMenu(null)
  }

  const startRename = (session: RenameState) => {
    setRenamingSession(session)
    setRenameValue(session.label)
    setContextMenu(null)
  }

  const killSession = (id: string, name: string, host?: string) => {
    setContextMenu(null)
    setConfirmKillKey(null)
    setConfirmWorktreeKillKey(null)
    onSessionKilled?.(host ? `${host}/${name}` : name)
    killSessionApi(id, name, host)
  }

  const killSessionAndWorktree = (id: string, name: string, host?: string) => {
    setContextMenu(null)
    setConfirmKillKey(null)
    setConfirmWorktreeKillKey(null)
    onSessionKilled?.(host ? `${host}/${name}` : name)
    killSessionApi(id, name, host, true)
  }

  const submitRename = async () => {
    if (!renamingSession || !renameValue.trim() || renameValue === renamingSession.label) {
      setRenamingSession(null)
      return
    }
    await renameSession(renamingSession.name, renameValue.trim(), renamingSession.host)
    setRenamingSession(null)
  }

  // Manually (re)generate an AI name for a session. The new name arrives via
  // the websocket state update; we only own the per-session naming spinner.
  const aiNameSession = async (name: string, host?: string) => {
    setContextMenu(null)
    const key = host ? `${host}/${name}` : name
    setNaming(key, true)
    try {
      await aiNameSessionApi(name, host)
    } finally {
      setNaming(key, false)
    }
  }

  const fallbackAiNameGroup = async (groupId: string, groupSessions: Session[], current?: string) => {
    const members = groupSessions
      .map(s => ({
        label: sessionLabel(s),
        agent: s.agent_type || '',
        project: s.project_path || '',
        prompt: (s.user_prompt || s.prompt_preview || '').trim(),
      }))
      .filter(m => m.label)
    if (members.length === 0) return
    setLegacyAiGroupId(groupId)
    try {
      const res = await fetch('/api/group/name', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ members, current: current || undefined }),
      })
      if (res.ok) {
        const { name } = await res.json()
        if (name) onRenameGroup?.(groupId, name)
      } else {
        const text = await res.text().catch(() => `AI naming failed (${res.status})`)
        throw new Error(text)
      }
    } finally {
      setLegacyAiGroupId(null)
    }
  }

  const showErrorToast = (message: string) => {
    window.dispatchEvent(new CustomEvent('termyard:toast', { detail: { severity: 'error', source: 'termyard', message } }))
  }

  const handleAiNameGroup = async (groupId: string, groupSessions: Session[], current?: string) => {
    if (!forceAiName) return
    try {
      const supported = await forceAiName(groupId)
      if (!supported) {
        await fallbackAiNameGroup(groupId, groupSessions, current)
      }
    } catch (err) {
      showErrorToast(err instanceof Error ? err.message : 'Failed to AI-name group')
    }
  }

  const submitGroupRename = () => {
    if (renamingGroupId) {
      onRenameGroup?.(renamingGroupId, groupRenameValue.trim())
    }
    setRenamingGroupId(null)
  }

  const allGroupedKeys = useMemo(() =>
    new Set(layoutGroups?.flatMap(g => g.leaves) ?? []),
    [layoutGroups]
  )

  // Build interleaved list: group brackets appear at the first member's position
  type UnifiedItem =
    | { kind: 'session'; session: Session }
    | { kind: 'group'; group: NonNullable<typeof layoutGroups>[number]; sessions: Session[] }

  const unifiedItems = useMemo((): UnifiedItem[] => {
    const items: UnifiedItem[] = []
    const seenGroups = new Set<string>()
    for (const session of visibleSessions) {
      const scheduleId = scheduleIDs.get(sessionKey(session)) || scheduleIDs.get(session.name) || sessionScheduleID(session)
      // Scheduled sessions are rendered in their own pinned footer block, not inline.
      if (scheduleId) continue
      const group = layoutGroups?.find(g => g.leaves.includes(sessionKey(session)))
      if (group) {
        if (!seenGroups.has(group.id)) {
          seenGroups.add(group.id)
          const groupSessions = group.leaves
            .map(k => visibleSessions.find(s => sessionKey(s) === k))
            .filter((s): s is Session => !!s)
          items.push({ kind: 'group', group, sessions: groupSessions })
        }
      } else {
        items.push({ kind: 'session', session })
      }
    }
    for (const group of layoutGroups ?? []) {
      if (!seenGroups.has(group.id)) {
        const groupSessions = group.leaves
          .map(k => visibleSessions.find(s => sessionKey(s) === k))
          .filter((s): s is Session => !!s)
        if (groupSessions.length > 0)
          items.push({ kind: 'group', group, sessions: groupSessions })
      }
    }
    return items
  }, [visibleSessions, layoutGroups, scheduleIDs])


  // Schedule groups render in a dedicated pinned block above the Hidden section,
  // out of the scrolling session list — recurring/background work, not active.
  const scheduleGroups = useMemo(() => {
    const groups: { scheduleId: string; schedule: (typeof schedules)[number] | undefined; sessions: Session[] }[] = []
    const seen = new Set<string>()
    for (const session of visibleSessions) {
      const scheduleId = scheduleIDs.get(sessionKey(session)) || scheduleIDs.get(session.name) || sessionScheduleID(session)
      if (!scheduleId || seen.has(scheduleId)) continue
      seen.add(scheduleId)
      const scheduleSessions = sortNewestFirst(
        visibleSessions.filter(item => (scheduleIDs.get(sessionKey(item)) || scheduleIDs.get(item.name) || sessionScheduleID(item)) === scheduleId),
      )
      groups.push({ scheduleId, schedule: scheduleById.get(scheduleId), sessions: scheduleSessions })
    }
    return groups
  }, [visibleSessions, schedules, scheduleById, scheduleIDs])

  // Derive the status badge for a session. Single source of truth shared by the
  // row renderer and the status-grouped view mode.
  const projectionOf = (session: Session) => {
    const sk = sessionKey(session)
    return sessionProjection(session, getSessionEvents(sk), getSessionActivity(sk), isSessionInActiveTurn(sk))
  }

  const statusOf = useCallback((session: Session): StatusBadge => {
    return projectionOf(session).status as StatusBadge
  }, [getSessionEvents, getSessionActivity, isSessionInActiveTurn])

  const statusGroups = useMemo(() => {
    if (viewMode !== 'status') return []
    const byStatus = new Map<StatusBadge, Session[]>()
    for (const session of visibleSessions) {
      const st = statusOf(session)
      const list = byStatus.get(st)
      if (list) list.push(session); else byStatus.set(st, [session])
    }
    return STATUS_BUCKETS
      .map(bucket => ({ ...bucket, sessions: sortNewestFirst(bucket.statuses.flatMap(st => byStatus.get(st) ?? [])) }))
      .filter(bucket => bucket.sessions.length > 0)
  }, [viewMode, visibleSessions, statusOf])

  // Project (cwd) grouping — the default view. Each section is one host+path
  // combination; a tiled group inherits its primary (first-leaf) session's host
  // and project. Preserves first-seen order (rank order). Host is surfaced as a
  // header chip only when multiple hosts are connected.
  const projectGroups = useMemo(() => {
    if (viewMode === 'status') return []
    const localBucketId = localHostId ?? ''
    const sessionHostId = (s: Session) => (s.host && s.host !== localHostId ? s.host : localBucketId)
    const hostNameFor = (hostId: string, fallback?: string) => (
      hostId === localBucketId
        ? 'This machine'
        : hosts?.find(h => h.id === hostId)?.name ?? fallback ?? hostId
    )
    const primaryOf = (item: UnifiedItem) => item.kind === 'session' ? item.session : item.sessions[0]
    type Section = { key: string; path: string; label: string; hostId: string; hostName: string; online: boolean; items: UnifiedItem[]; count: number }
    const order: string[] = []
    const map = new Map<string, Section>()
    for (const item of unifiedItems) {
      const primary = primaryOf(item)
      if (!primary) continue
      const hostId = sessionHostId(primary)
      const path = primary.project_path || ''
      const key = `${hostId}\u0000${path}`
      let section = map.get(key)
      if (!section) {
        section = { key, path, label: projectLabel(path), hostId, hostName: hostNameFor(hostId, primary.host_name), online: true, items: [], count: 0 }
        map.set(key, section)
        order.push(key)
      }
      section.items.push(item)
      section.count += item.kind === 'session' ? 1 : item.sessions.length
      const members = item.kind === 'session' ? [item.session] : item.sessions
      if (members.some(s => s.host_online === false)) section.online = false
    }
    // Apply the user's persisted section order; sections never moved keep their
    // first-seen order and sort after moved ones (stable sort on Infinity ties).
    const rank = (k: string) => { const i = projectOrder.indexOf(k); return i === -1 ? Number.POSITIVE_INFINITY : i }
    return order.map(k => map.get(k)!).sort((a, b) => rank(a.key) - rank(b.key))
  }, [viewMode, unifiedItems, hosts, localHostId, projectOrder])

  const renderSessionItem = (session: Session, isHiddenSection = false, inHostGroup = false, inProjectGroup = false, extras?: { tileFlag?: number; originLabel?: string; groupActions?: { onAiName: () => void; onRename: () => void; aiPending: boolean } }) => {
    const sk = sessionKey(session)
    const isSelected = selectedSession === sk
    const proj = projectionOf(session)
    const isRenaming = renamingSession?.key === sk
    const isOffline = (session.host && session.host_online === false) || session.unreachable
    // Transient highlight right after an agent finishes its turn (working -> idle).
    const justDone = isSessionRecentlyDone(sk) && proj.status === 'idle'
    const stripeColor = hasMultipleHosts ? hostColor(session.host, localHostId) : null
    const hostLabel = stripeColor
      ? (hosts?.find(h => h.id === session.host)?.name ?? session.host_name ?? session.host ?? 'remote')
      : null
    const projectName = pathLeaf(session.project_path)
    const worktreeParent = session.is_worktree ? pathLeaf(session.worktree_parent) : ''
    const agentType = session.agent_type || proj.loudEvent?.tool || getSessionEvents(sk)[0]?.tool
    const allPanes = !collapsed
      ? (session.windows ?? []).flatMap(w => (w.panes ?? []).map(p => ({ ...p, windowIndex: w.index })))
      : []
    const showPanes = allPanes.length > 1
    const agentPresent = proj.agentPresent
    const needsAttention = proj.needsAttention
    // Single-line rows: a leading dot carries status; the row's status word is in its title.
    const statusBadge = proj.status as StatusBadge

    const handleTouchStart = (e: React.TouchEvent) => {
      if (isRenaming) return
      const touch = e.touches[0]
      const x = touch.clientX
      const y = touch.clientY
      touchTimerRef.current = setTimeout(() => {
        touchTimerRef.current = null
        setContextMenu({ key: sk, id: session.id, name: session.name, label: sessionLabel(session), host: session.host, isWorktree: session.is_worktree ?? false, x, y })
      }, 600)
    }

    const handleTouchEnd = () => {
      if (touchTimerRef.current !== null) {
        clearTimeout(touchTimerRef.current)
        touchTimerRef.current = null
      }
    }

    return (
      <li key={sk} data-session-key={sk}>
        <div
          role="button"
          tabIndex={0}
          {...(!collapsed ? glancePreview.trigger({ name: session.name, host: session.host, display_name: session.display_name, host_name: session.host_name }) : {})}
          draggable={!collapsed && !isRenaming}
          onDragStart={(e) => {
            e.dataTransfer.setData('text/plain', sk)
            setDraggingKey(sk)
          }}
          onDragEnd={() => {
            setDraggingKey(null)
            setPairTarget(null)
            setDropIndicator(null)
          }}
          onDragOver={(e) => {
            e.preventDefault()
            if (!draggingKey || draggingKey === sk) return
            const rect = e.currentTarget.getBoundingClientRect()
            const y = e.clientY - rect.top
            const ratio = y / rect.height
            if (ratio < 0.25) {
              setDropIndicator({ key: sk, position: 'above' })
              setPairTarget(null)
            } else if (ratio > 0.75) {
              setDropIndicator({ key: sk, position: 'below' })
              setPairTarget(null)
            } else {
              setPairTarget(sk)
              setDropIndicator(null)
            }
          }}
          onDragLeave={(e) => {
            if (!e.currentTarget.contains(e.relatedTarget as Node)) {
              setDropIndicator(null)
              setPairTarget(null)
            }
          }}
          onDrop={(e) => {
            e.preventDefault()
            if (!draggingKey || draggingKey === sk) return
            // Recompute zone from drop position — onDragLeave may have cleared state
            const rect = e.currentTarget.getBoundingClientRect()
            const ratio = (e.clientY - rect.top) / rect.height
            if (ratio >= 0.25 && ratio <= 0.75) {
              onPairSessions?.(draggingKey, sk)
            } else {
              const position = ratio < 0.25 ? 'above' : 'below'
              const keys = visibleSessions.map(sessionKey)
              const withoutDragged = keys.filter(k => k !== draggingKey)
              const targetIdx = withoutDragged.indexOf(sk)
              if (targetIdx !== -1) {
                const insertAt = position === 'above' ? targetIdx : targetIdx + 1
                withoutDragged.splice(Math.max(0, insertAt), 0, draggingKey)
                const idx = withoutDragged.indexOf(draggingKey)
                const before = idx > 0 ? withoutDragged[idx - 1] : null
                const after = idx < withoutDragged.length - 1 ? withoutDragged[idx + 1] : null
                const newRank = generateKeyBetween(before ? sessionOrderRanks[before] ?? null : null, after ? sessionOrderRanks[after] ?? null : null)
                setSessionOrderRank(draggingKey, newRank)
              }
            }
            setDraggingKey(null)
            setPairTarget(null)
            setDropIndicator(null)
          }}
          onClick={() => !isRenaming && onSessionSelect(session)}
          onKeyDown={(e) => {
            if (!isRenaming && (e.key === 'Enter' || e.key === ' ')) {
              e.preventDefault()
              onSessionSelect(session)
            }
          }}
          onContextMenu={(e) => {
            e.preventDefault()
            setContextMenu({ key: sk, id: session.id, name: session.name, label: sessionLabel(session), host: session.host, isWorktree: session.is_worktree ?? false, x: e.clientX, y: e.clientY })
          }}
          onTouchStart={handleTouchStart}
          onTouchEnd={handleTouchEnd}
          onTouchMove={handleTouchEnd}
          className={cn(
            'group relative flex flex-col w-full min-w-0 rounded-sm transition-all duration-200 text-ink',
            collapsed ? 'px-0 py-2' : 'px-2 py-1.5',
            'hover:bg-white/[0.05]',
            isSelected && !collapsed && 'bg-white/[0.08] !text-primary border border-white/20',
            isSelected && collapsed && 'bg-white/[0.08] !text-primary',
            !isSelected && !justDone && 'border border-transparent',
            !isSelected && justDone && (collapsed
              ? 'bg-[rgba(89,212,153,0.08)]'
              : 'border border-[var(--accent-green)]/50 bg-[rgba(89,212,153,0.08)]'),
            (isHiddenSection || isOffline) && 'opacity-60',
            isRenaming && 'cursor-default',
            draggingKey === sk && 'opacity-75 cursor-grab',
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
          <div className={cn('flex min-w-0 items-center gap-2 w-full', collapsed && 'justify-center')}>
              {!collapsed && (() => {
                // Attention outranks status for the dot color so a loud event stays
                // visible even while the underlying status reads working/idle.
                const cfg = needsAttention
                  ? { color: 'var(--warning)', pulse: true }
                  : justDone
                    ? { color: 'var(--accent-green)', pulse: false }
                    : statusBadgeConfig[statusBadge]
                return (
                  <span
                    className={cn('w-2 h-2 rounded-full shrink-0 pointer-events-none', cfg.pulse && 'animate-[pulse_1.5s_ease-in-out_infinite]')}
                    style={{ background: cfg.color }}
                    title={needsAttention ? 'Needs attention' : statusBadge}
                    aria-label={needsAttention ? 'Needs attention' : statusBadge}
                  />
                )
              })()}
              {!collapsed && <AgentMark agentType={agentPresent ? agentType : undefined} className="h-3.5 w-3.5 shrink-0" />}
              {!collapsed && stripeColor && !inHostGroup && (
                <span
                  className="w-2 h-2 rounded-full shrink-0 pointer-events-none"
                  style={{ backgroundColor: stripeColor }}
                  title={hostLabel ? `Host: ${hostLabel}` : undefined}
                  aria-label={hostLabel ? `Host: ${hostLabel}` : 'remote host'}
                />
              )}
              {isRenaming ? (
                <input
                  ref={renameInputRef}
                  value={renameValue}
                  onChange={(e) => setRenameValue(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter') submitRename()
                    if (e.key === 'Escape') setRenamingSession(null)
                  }}
                  onBlur={submitRename}
                  onClick={(e) => e.stopPropagation()}
                  className="min-w-0 flex-1 text-sm text-ink bg-surface-elevated border border-primary rounded-sm px-1.5 py-0.5 outline-none font-sans font-medium"
                />
              ) : (
                <span className={cn('flex-1 flex items-baseline gap-1 min-w-0 overflow-hidden text-left', collapsed && 'justify-center text-center')}>
                  {!collapsed && worktreeParent && (
                    <span
                      className="shrink min-w-0 truncate text-[12px] font-medium tracking-tight text-mute/40"
                      title={session.worktree_parent}
                    >
                      {worktreeParent}
                    </span>
                  )}
                  {!collapsed && worktreeParent && (
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
                  {!collapsed && !inProjectGroup && projectName && (
                    <span
                      className="shrink min-w-0 truncate text-[12px] font-medium tracking-tight text-mute/60"
                      title={session.project_path}
                    >
                      {projectName}<span className="text-mute/30">/</span>
                    </span>
                  )}
                  <span
                    className={cn(
                      'shrink-0 max-w-full text-[12px] font-medium tracking-tight overflow-hidden text-ellipsis whitespace-nowrap',
                      isSelected && '!text-primary',
                    )}
                    title={session.agent_session_id ? `${sessionLabel(session)} · ${session.agent_session_id}` : (sessionLabel(session) !== session.name ? `${sessionLabel(session)} (${session.name})` : session.name)}
                  >
                    {collapsed ? sessionLabel(session).charAt(0).toUpperCase() : sessionLabel(session)}
                  </span>
                </span>
              )}
              {!collapsed && extras?.originLabel && (
                <span
                  className="shrink-0 text-[9px] font-mono text-mute/80 bg-surface border border-hairline rounded-xs px-1 py-0.5 leading-none"
                  title={session.project_path}
                >
                  {extras.originLabel}
                </span>
              )}
              {!collapsed && extras?.tileFlag && extras.tileFlag > 1 && (
                <span
                  className="shrink-0 text-[9px] font-mono text-primary bg-primary/15 border border-primary/30 rounded-xs px-1 py-0.5 leading-none"
                  title="Tiled group"
                >
                  ⊞ {extras.tileFlag}
                </span>
              )}
              {!collapsed && extras?.groupActions && (
                <>
                  <button
                    type="button"
                    title="AI name this group"
                    disabled={extras.groupActions.aiPending}
                    onClick={(e) => { e.stopPropagation(); extras?.groupActions?.onAiName() }}
                    className="opacity-0 group-hover:opacity-100 transition-opacity text-mute/40 hover:text-primary shrink-0 flex items-center disabled:opacity-100"
                  >
                    <SparkleIcon spinning={extras.groupActions.aiPending} size={10} />
                  </button>
                  <button
                    type="button"
                    title="Rename group"
                    onClick={(e) => { e.stopPropagation(); extras?.groupActions?.onRename() }}
                    className="opacity-0 group-hover:opacity-100 transition-opacity text-mute/40 hover:text-ink shrink-0 flex items-center"
                  >
                    <svg width="9" height="9" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                      <path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7" />
                      <path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z" />
                    </svg>
                  </button>
                </>
              )}
              {!collapsed && namingSessions.has(sessionKey(session)) && (
                <span className="shrink-0" title="AI naming…">
                  <SparkleIcon spinning size={11} />
                </span>
              )}
              {!collapsed && (
                <span className="shrink-0 text-[10px] text-mute/50 font-medium tabular-nums" title={`Uptime: ${formatUptime(session.created)}`}>
                  {formatUptime(session.created)}
                </span>
              )}
          </div>

          {pairTarget === sk && (
            <div className="absolute inset-0 rounded-sm bg-canvas/80 backdrop-blur-sm border-2 border-primary flex items-center justify-center pointer-events-none z-10">
              <span className="text-[10px] font-bold text-primary uppercase tracking-widest">⊞ Split</span>
            </div>
          )}
          {dropIndicator?.key === sk && dropIndicator.position === 'above' && (
            <div className="absolute top-0 left-0 right-0 h-1 bg-accent-green rounded-full pointer-events-none z-10 shadow-[0_0_8px_rgba(89,212,153,0.4)]" />
          )}
          {showPanes && (
            <div className="mt-2 pt-1.5 border-t border-hairline">
              <ul className="space-y-px">
                {allPanes.map(pane => {

                  const pathParts = pane.current_path?.split('/').filter(Boolean) ?? []
                  const pathBase = pathParts[pathParts.length - 1] ?? ''
                  return (
                    <li key={pane.id}>
                      <button
                        type="button"
                        onClick={(e) => {
                          e.stopPropagation()
                          onSessionSelect(session)
                          fetch('/api/session/select-window', {
                            method: 'POST',
                            headers: { 'Content-Type': 'application/json' },
                            body: JSON.stringify({ host: session.host || undefined, session: session.name, window: pane.windowIndex, pane: pane.id }),
                          }).catch(err => console.error('Failed to select pane:', err))
                        }}
                        className={cn(
                          'w-full flex items-center gap-1.5 px-1 py-0.5 rounded-xs text-[11px] font-mono text-left transition-colors',
                          pane.active ? 'text-primary' : 'text-mute hover:text-ink hover:bg-surface-elevated',
                        )}
                      >
                        <span className={cn('w-1.5 h-1.5 rounded-full shrink-0', pane.active ? 'bg-primary' : 'bg-mute/30')} />
                        <span className="truncate">{pane.current_command || 'shell'}</span>
                        {pathBase && <span className="text-mute/50 ml-auto pl-2 shrink-0">{pathBase}</span>}
                      </button>
                    </li>
                  )
                })}
              </ul>
            </div>
          )}
          {dropIndicator?.key === sk && dropIndicator.position === 'below' && (
            <div className="absolute bottom-0 left-0 right-0 h-1 bg-accent-green rounded-full pointer-events-none z-10 shadow-[0_0_8px_rgba(89,212,153,0.4)]" />
          )}
        </div>
      </li>
    )
  }


  // Compact tiled-group rendering for project (cwd) view: the primary
  // (active/first) session as a normal row flagged with the tile count, then
  // its companions indented under a connector, each showing an origin chip when
  // their own project differs from the primary's.
  const renderProjectTiledGroup = (group: NonNullable<typeof layoutGroups>[number], groupSessions: Session[]) => {
    const primary = groupSessions.find(s => sessionKey(s) === group.activeKey) ?? groupSessions[0]
    if (!primary) return null
    const primaryKey = sessionKey(primary)
    const primaryProject = primary.project_path || ''
    const companions = groupSessions.filter(s => sessionKey(s) !== primaryKey)
    return (
      <Fragment key={group.id}>
        {!collapsed && renamingGroupId === group.id && (
          <li className="flex items-center gap-1.5 px-2 pt-1 pb-0 min-h-[16px]">
            <input
              ref={groupRenameInputRef}
              value={groupRenameValue}
              onChange={(e) => setGroupRenameValue(e.target.value)}
              onKeyDown={(e) => { if (e.key === 'Enter') submitGroupRename(); if (e.key === 'Escape') setRenamingGroupId(null) }}
              onBlur={submitGroupRename}
              onClick={(e) => e.stopPropagation()}
              placeholder="Group name…"
              className="flex-1 text-[11px] text-ink bg-surface-elevated border border-primary rounded-xs px-1.5 py-0 outline-none font-sans font-medium"
            />
          </li>
        )}
        {renderSessionItem(primary, false, false, true, {
          tileFlag: groupSessions.length,
          groupActions: {
            onAiName: () => handleAiNameGroup(group.id, groupSessions, group.name),
            onRename: () => { setRenamingGroupId(group.id); setGroupRenameValue(group.name || '') },
            aiPending: aiPendingGroupId === group.id,
          },
        })}
        {companions.length > 0 && (
          <li className="ml-3 pl-2 border-l border-hairline/60">
            <ul className="space-y-0.5">
              {companions.map(s => renderSessionItem(
                s, false, false, true,
                (s.project_path || '') !== primaryProject ? { originLabel: projectLabel(s.project_path) } : undefined,
              ))}
            </ul>
          </li>
        )}
      </Fragment>
    )
  }

  const renderGroupItem = (group: NonNullable<typeof layoutGroups>[number], groupSessions: Session[], inHostGroup = false) => {
    const firstLeaf = group.leaves[0]
    const isGroupCollapsed = !collapsed && collapsedGroups.has(group.id)
    const collapsedAgentTypes = (() => {
      const seen = new Set<string>()
      return groupSessions
        .map(s => s.agent_type)
        .filter((t): t is string => !!t && (seen.has(t) ? false : (seen.add(t), true)))
        .slice(0, 3)
    })()
    const collapsedNewest = groupSessions.reduce((a, b) =>
      (a.created || '') > (b.created || '') ? a : b
    , groupSessions[0])
    const collapsedProject = pathLeaf(groupSessions.find(s => s.project_path)?.project_path)
    return (
      <li key={group.id} className="flex items-stretch">
        {/* Bracket: drag to reorder, click to collapse/expand */}
        {!collapsed && (
          <div
            draggable={!!firstLeaf}
            role="button"
            tabIndex={0}
            onClick={() => toggleGroupCollapsed(group.id)}
            onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); toggleGroupCollapsed(group.id) } }}
            title={`${isGroupCollapsed ? 'Expand' : 'Collapse'} group — drag to reorder`}
            className={cn(
              'w-5 shrink-0 flex flex-col items-center py-0.5 rounded-l-sm cursor-pointer active:cursor-grabbing transition-colors hover:bg-surface-elevated group/bracket',
              !group.isActive && 'opacity-60'
            )}
            onDragStart={(e) => {
              if (!firstLeaf) return
              e.dataTransfer.setData('text/plain', firstLeaf)
              e.dataTransfer.effectAllowed = 'move'
              setDraggingKey(firstLeaf)
            }}
            onDragEnd={() => {
              setDraggingKey(null)
              setPairTarget(null)
              setDropIndicator(null)
            }}
            onDragOver={(e) => {
              e.preventDefault()
              if (!draggingKey || draggingKey === firstLeaf) return
              setDropIndicator({ key: firstLeaf, position: 'above' })
              setPairTarget(null)
            }}
            onDragLeave={(e) => {
              if (!e.currentTarget.contains(e.relatedTarget as Node)) {
                setDropIndicator(null)
              }
            }}
            onDrop={(e) => {
              e.preventDefault()
              if (!draggingKey || !firstLeaf || draggingKey === firstLeaf) return
              const group = layoutGroups?.find(g => g.leaves.includes(firstLeaf)) ?? null
              const keys = visibleSessions.map(sessionKey)
              const withoutDragged = keys.filter(k => k !== draggingKey)
              const targetIdxs = group ? group.leaves.map(k => withoutDragged.indexOf(k)).filter(i => i !== -1) : []
              if (targetIdxs.length > 0) {
                const insertAt = Math.min(...targetIdxs)
                withoutDragged.splice(Math.max(0, insertAt), 0, draggingKey)
                const idx = withoutDragged.indexOf(draggingKey)
                const before = idx > 0 ? withoutDragged[idx - 1] : null
                const after = idx < withoutDragged.length - 1 ? withoutDragged[idx + 1] : null
                const newRank = generateKeyBetween(before ? sessionOrderRanks[before] ?? null : null, after ? sessionOrderRanks[after] ?? null : null)
                setSessionOrderRank(draggingKey, newRank)
              }
              setDraggingKey(null); setDropIndicator(null)
            }}
          >
            {/* Collapse indicator arrow */}
            <span
              aria-hidden
              className="text-mute/70 leading-none mt-0.5 shrink-0 select-none transition-transform duration-200"
              style={{ fontSize: '9px' }}
            >
              {isGroupCollapsed ? '▸' : '▾'}
            </span>
            <div className={cn(
              'w-0.5 rounded-full flex-1 min-h-[0.5rem] transition-colors mt-0.5',
              group.isActive ? 'bg-primary/40 group-hover/bracket:bg-primary/70' : 'bg-primary/20'
            )} />
          </div>
        )}
        {/* Sessions or collapsed summary */}
        <div className={cn('flex-1 min-w-0', !group.isActive && 'opacity-70')}>
          {isGroupCollapsed ? (
            /* Collapsed: compact summary row — click always selects, chevron toggles */
            <div
              role="button"
              tabIndex={0}
              onClick={() => {
                if (renamingGroupId === group.id) return
                if (group.isActive) {
                  const activeSession = groupSessions.find(s => sessionKey(s) === group.activeKey) ?? groupSessions[0]
                  if (activeSession) onSessionSelect(activeSession)
                } else {
                  onSwitchGroup?.(group.id)
                }
              }}
              className={cn(
                'relative flex flex-col w-full p-2.5 rounded-sm transition-all duration-200 text-ink cursor-pointer select-none',
                'hover:bg-white/[0.05]',
                group.isActive ? 'bg-white/[0.08] border border-white/20' : 'border border-transparent',
              )}
            >
              <div className="flex items-center gap-2 w-full group/collname">
                <span className="text-[10px] font-mono text-mute/50 shrink-0 w-3">{groupSessions.length}</span>
                {collapsedAgentTypes.map((t, i) => (
                  <AgentMark key={i} agentType={t} className="h-3.5 min-w-5 px-0.5 shrink-0" />
                ))}
                {renamingGroupId === group.id ? (
                  <input
                    ref={groupRenameInputRef}
                    value={groupRenameValue}
                    onChange={(e) => setGroupRenameValue(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter') submitGroupRename()
                      if (e.key === 'Escape') setRenamingGroupId(null)
                    }}
                    onBlur={submitGroupRename}
                    onClick={(e) => e.stopPropagation()}
                    placeholder={groupSessions.map(s => s.name).join(' · ')}
                    className="flex-1 text-sm text-ink bg-surface-elevated border border-primary rounded-xs px-1.5 py-0 outline-none font-sans font-medium"
                  />
                ) : (
                  <span className="flex-1 text-sm font-medium text-ink truncate">
                    {group.name || groupSessions.map(s => s.name).join(' · ')}
                  </span>
                )}
                {formatUptime(collapsedNewest?.created) && !renamingGroupId && (
                  <span className="shrink-0 rounded-xs border border-hairline px-1.5 py-0.5 text-xs text-mute font-medium">
                    {formatUptime(collapsedNewest.created)}
                  </span>
                )}
                {/* AI name */}
                {!renamingGroupId && (
                  <button
                    type="button"
                    title="AI name this group"
                    disabled={aiPendingGroupId === group.id}
                    onClick={(e) => { e.stopPropagation(); handleAiNameGroup(group.id, groupSessions, group.name) }}
                    className="opacity-0 group-hover/collname:opacity-100 transition-opacity text-mute/40 hover:text-primary shrink-0 flex items-center disabled:opacity-100"
                  >
                    <SparkleIcon spinning={aiPendingGroupId === group.id} size={11} />
                  </button>
                )}
                {/* Rename pencil */}
                {!renamingGroupId && (
                  <button
                    type="button"
                    title="Rename group"
                    onClick={(e) => { e.stopPropagation(); setRenamingGroupId(group.id); setGroupRenameValue(group.name || '') }}
                    className="opacity-0 group-hover/collname:opacity-100 transition-opacity text-mute/40 hover:text-ink shrink-0 flex items-center"
                  >
                    <svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                      <path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7" />
                      <path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z" />
                    </svg>
                  </button>
                )}
                {group.isActive && !renamingGroupId && (
                  <span className="w-1.5 h-1.5 rounded-full bg-primary shrink-0" />
                )}
              </div>
              {collapsedProject && (
                <div className="mt-1 flex items-center gap-2 text-xs text-mute font-medium">
                  <span className="truncate">{collapsedProject}</span>
                </div>
              )}
            </div>
          ) : (
            <>
              {/* Group name label row (expanded) */}
              {!collapsed && (
                <div className="group/gname flex items-center gap-1.5 px-2 pt-1 pb-0.5 min-h-[20px]">
                  {renamingGroupId === group.id ? (
                    <input
                      ref={groupRenameInputRef}
                      value={groupRenameValue}
                      onChange={(e) => setGroupRenameValue(e.target.value)}
                      onKeyDown={(e) => {
                        if (e.key === 'Enter') submitGroupRename()
                        if (e.key === 'Escape') setRenamingGroupId(null)
                      }}
                      onBlur={submitGroupRename}
                      onClick={(e) => e.stopPropagation()}
                      placeholder="Group name…"
                      className="flex-1 text-[11px] text-ink bg-surface-elevated border border-primary rounded-xs px-1.5 py-0 outline-none font-sans font-medium"
                    />
                  ) : (
                    <>
                      <span className={cn(
                        'text-[10px] font-semibold tracking-wider uppercase truncate flex-1 select-none',
                        group.name ? 'text-mute/70' : 'text-mute/25'
                      )}>
                        {group.name || 'unnamed'}
                      </span>
                      <button
                        type="button"
                        title="AI name this group"
                        disabled={aiPendingGroupId === group.id}
                        onClick={(e) => { e.stopPropagation(); handleAiNameGroup(group.id, groupSessions, group.name) }}
                        className="opacity-0 group-hover/gname:opacity-100 transition-opacity text-mute/40 hover:text-primary shrink-0 flex items-center disabled:opacity-100"
                      >
                        <SparkleIcon spinning={aiPendingGroupId === group.id} size={10} />
                      </button>
                      <button
                        type="button"
                        title="Rename group"
                        onClick={(e) => { e.stopPropagation(); setRenamingGroupId(group.id); setGroupRenameValue(group.name || '') }}
                        className="opacity-0 group-hover/gname:opacity-100 transition-opacity text-mute/40 hover:text-ink shrink-0 flex items-center"
                      >
                        <svg width="9" height="9" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                          <path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7" />
                          <path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z" />
                        </svg>
                      </button>
                    </>
                  )}
                </div>
              )}
              {(() => {
                const tools = groupSessions.filter(s => isToolSession(s, getSessionEvents(sessionKey(s))))
                const agents = groupSessions.filter(s => !tools.includes(s))
                const fold = agents.length > 0 && tools.length > 0
                const primaryKey = fold
                  ? (group.activeKey && agents.some(s => sessionKey(s) === group.activeKey)
                    ? group.activeKey
                    : sessionKey(agents[0]))
                  : null
                const renderSessions = fold ? agents : groupSessions
                return (
                  <ul className="space-y-0.5">
                    {renderSessions.map((session) => {
                      const sk = sessionKey(session)
                      return (
                        <div key={sk} className="relative" onClick={() =>
                          group.isActive ? onSessionSelect(session) : onSwitchGroup?.(group.id, sk)
                        }>
                          {renderSessionItem(session, false, inHostGroup)}
                          {!collapsed && fold && sk === primaryKey && tools.length > 0 && (
                            <div className="flex flex-wrap gap-1 pl-3 pr-2 pb-1">
                              {tools.map((t) => (
                                <button
                                  key={sessionKey(t)}
                                  type="button"
                                  {...glancePreview.trigger({ name: t.name, host: t.host, display_name: t.display_name, host_name: t.host_name })}
                                  onClick={(e) => {
                                    e.stopPropagation()
                                    group.isActive ? onSessionSelect(t) : onSwitchGroup?.(group.id, sessionKey(t))
                                  }}
                                  className="inline-flex items-center gap-1 max-w-[12rem] px-1 py-0.5 text-xs text-mute/70 hover:text-ink truncate"
                                >
                                  <span className="text-mute/40 shrink-0">↳</span>
                                  <span className="truncate font-mono text-[11px]">{t.display_name || t.name}</span>
                                </button>
                              ))}
                            </div>
                          )}
                        </div>
                      )
                    })}
                  </ul>
                )
              })()}
            </>
          )}
        </div>
      </li>
    )
  }

  const renderScheduleItem = (scheduleId: string, sessions: Session[], schedule?: (typeof schedules)[number]) => {
    const latest = sessions[0]
    const isExpanded = expandedScheduleGroups.has(scheduleId)
    const maxExpandedChildren = 6
    // Collapsed groups hide ALL runs; the indicator below surfaces count + state.
    const childSessions = isExpanded ? sessions.slice(0, maxExpandedChildren) : []
    const overflow = isExpanded && sessions.length > maxExpandedChildren ? sessions.length - maxExpandedChildren : 0
    const latestStatus = latest ? statusOf(latest) : 'idle'
    const latestColor = statusBadgeConfig[latestStatus].color
    const attentionCount = sessions.filter(s => { const st = statusOf(s); return LOUD_STATUSES.has(st) }).length
    const sessionHost = latest?.host || ''
    // Definition unknown locally: the schedule lives on another peer (its runs may
    // be local here or on another host) or was removed. Without registry sync we
    // cannot tell deleted from defined-elsewhere, so never claim 'deleted' — render
    // neutrally by host reachability. ponytail: upgrade to real enabled/paused
    // state when schedule defs sync peer-to-peer (2b).
    const known = !!schedule
    const enabled = schedule?.enabled ?? true
    const host = schedule?.host || sessionHost
    const hostOnline = !host || hosts?.some(item => item.id === host && item.online)
    const stateLabel = known ? (!enabled ? 'paused' : !hostOnline ? 'peer offline' : 'active') : (!hostOnline ? 'peer offline' : 'scheduled')
    const neutralColor = 'text-mute/70 border-hairline bg-surface-elevated/70'
    const stateColor = !known ? neutralColor : !enabled ? 'text-amber-400 border-amber-400/30 bg-amber-400/10' : !hostOnline ? neutralColor : 'text-emerald-400 border-emerald-400/30 bg-emerald-400/10'
    const scheduleName = schedule?.name || (latest?.name ? latest.name.replace(/-\d+$/, '') : '') || scheduleId
    return (
      <li key={`schedule:${scheduleId}`} data-schedule-id={scheduleId}>
        <div className={cn('rounded-sm border border-hairline bg-surface/70 overflow-hidden', !enabled && 'opacity-75')}>
          <button
            type="button"
            onClick={() => toggleScheduleExpanded(scheduleId)}
            className="w-full text-left px-2.5 py-2 flex items-start gap-2 transition-colors hover:bg-white/[0.05]"
          >
            <span className="text-[11px] text-mute/70 font-mono pt-0.5 shrink-0">{isExpanded ? '▾' : '▸'}</span>
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2 min-w-0">
                <span className="text-[12px] font-semibold text-ink truncate">{scheduleName}</span>
                <span className={cn('shrink-0 text-[10px] font-bold px-1.5 py-0.5 rounded-xs border uppercase tracking-widest', stateColor)}>
                  {stateLabel}
                </span>
              </div>
              <div className="mt-0.5 text-[10px] text-mute/60 flex items-center gap-1.5">
                <span className="truncate" title={schedule?.cronSpec}>
                  {(schedule?.cronSpec ? describeCron(schedule.cronSpec) : null) ?? schedule?.cronSpec ?? '—'}  ·  next {formatRelativeTime(schedule?.nextRun)}  ·  {schedule?.runCount ?? sessions.length} runs
                </span>
                <span className="shrink-0 inline-flex items-center gap-1" title={`${sessions.length} session${sessions.length === 1 ? '' : 's'}, latest ${latestStatus}`}>
                  <span className="w-1.5 h-1.5 rounded-full" style={{ background: latestColor }} />
                  {sessions.length}
                  {attentionCount > 0 && <span className="text-accent-red font-semibold">{attentionCount}⚠</span>}
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
  const contextTargetSession = contextMenu
    ? orderedSessions.find(session => sessionKey(session) === contextMenu.key) || null
    : null
  const canRenameContextTarget = Boolean(contextTargetSession)

  const normalWidth = Math.max(width, 260)
  const hoverOverlay = persistentCollapsed && collapseMode === 'small' && hoverExpanded

  return (
    <div
      data-sidebar
      className={cn(
        'relative h-full min-w-0 shrink-0',
        persistentCollapsed
          ? collapseMode === 'hidden' ? 'w-0' : 'w-11 max-w-11'
          : '',
      )}
      style={!persistentCollapsed ? { width: normalWidth, minWidth: 260 } : undefined}
    >
      <aside
        onMouseEnter={handleSidebarMouseEnter}
        onMouseLeave={handleSidebarMouseLeave}
        style={!collapsed ? { width: normalWidth, minWidth: 260 } : undefined}
        className={cn(
        'relative flex flex-col h-full min-w-0 overflow-hidden bg-canvas font-sans text-sm font-medium',
        !resizing && 'transition-[width,box-shadow] motion-reduce:transition-none',
        hoverExpanded
          ? 'duration-[220ms] ease-[cubic-bezier(0.25,1,0.5,1)]'
          : 'duration-[180ms] ease-[cubic-bezier(0.5,0,0.75,0)]',
        hoverOverlay && 'absolute left-0 top-0 z-40 shadow-[8px_0_32px_rgba(0,0,0,0.5)]',
        collapsed
          ? collapseMode === 'hidden' ? 'w-0' : 'w-11'
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
          <div className="relative mb-1.5">
            <svg
              className="absolute left-2.5 top-1/2 -translate-y-1/2 text-mute/60 pointer-events-none"
              width="13" height="13" viewBox="0 0 24 24" fill="none"
              stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden
            >
              <circle cx="11" cy="11" r="8" /><path d="m21 21-4.3-4.3" />
            </svg>
            <input
              type="text"
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              onKeyDown={(e) => { if (e.key === 'Escape') { e.stopPropagation(); setSearchQuery('') } }}
              placeholder="Search sessions"
              aria-label="Search sessions"
              className="w-full rounded-md border border-hairline bg-surface-elevated pl-8 pr-7 py-2 text-xs text-ink placeholder:text-mute/50 outline-none focus:border-primary/60 transition-colors font-sans"
            />
            {searchQuery && (
              <button
                type="button"
                onClick={() => setSearchQuery('')}
                title="Clear search"
                className="absolute right-1.5 top-1/2 -translate-y-1/2 text-mute/60 hover:text-ink px-1"
              >
                ×
              </button>
            )}
          </div>
          <div className="flex items-center gap-1.5">
            <button
              type="button"
              onClick={() => setFilterOpen(value => !value)}
              className="flex-1 min-w-0 rounded-md border border-hairline bg-surface-elevated px-3 py-2 text-left text-xs text-mute hover:text-ink font-medium transition-colors truncate"
            >
              {filterLabel}
            </button>
            <button
              type="button"
              onClick={() => setViewMode(m => (m === 'status' ? 'project' : 'status'))}
              title={viewMode === 'status' ? 'Grouping by status — click to group by project' : 'Group sessions by status'}
              aria-pressed={viewMode === 'status'}
              className={cn(
                'shrink-0 rounded-md border px-2 py-2 transition-colors',
                viewMode === 'status'
                  ? 'border-primary/50 bg-primary/15 text-primary'
                  : 'border-hairline bg-surface-elevated text-mute hover:text-ink',
              )}
            >
              <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                <line x1="8" y1="6" x2="21" y2="6" />
                <line x1="8" y1="12" x2="21" y2="12" />
                <line x1="8" y1="18" x2="21" y2="18" />
                <circle cx="3.5" cy="6" r="1.5" fill="currentColor" stroke="none" />
                <circle cx="3.5" cy="12" r="1.5" fill="currentColor" stroke="none" />
                <circle cx="3.5" cy="18" r="1.5" fill="currentColor" stroke="none" />
              </svg>
            </button>
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

      <nav className={cn('flex-1 overflow-y-auto px-2 pb-2', collapsed ? 'pt-10' : 'pt-2')}>
        <ul className="space-y-0.5">
          {visibleSessions.length === 0 && (
            <li className="p-3 text-mute text-sm">
              {collapsed ? '—' : 'No sessions'}
            </li>
          )}

          {/* Status-grouped view: flatten all sessions into status sections */}
          {viewMode === 'status' && statusGroups.map(bucket => {
            const cfg = statusBadgeConfig[bucket.statuses[0]]
            return (
              <li key={`status:${bucket.id}`}>
                <div className="flex items-center gap-2 px-1 pt-3 pb-1">
                  <span className="h-1.5 w-1.5 rounded-full shrink-0" style={{ background: cfg.color }} />
                  <span className="text-[10px] font-bold uppercase tracking-widest text-mute/70">{bucket.label}</span>
                  <span className="text-[10px] font-mono text-mute/40">{bucket.sessions.length}</span>
                  <div className="flex-1 h-px bg-hairline/60" />
                </div>
                <ul className="space-y-0.5">
                  {bucket.sessions.map(session => renderSessionItem(session, false))}
                </ul>
              </li>
            )
          })}

          {/* Drop target at start of list */}
          {viewMode === 'project' && draggingKey && visibleSessions.length > 0 && (
            <li
              className="h-4 relative"
              onDragOver={(e) => {
                e.preventDefault()
                setDropIndicator({ key: '__list-start__', position: 'above' })
              }}
              onDragLeave={(e) => {
                if (!e.currentTarget.contains(e.relatedTarget as Node)) {
                  setDropIndicator(null)
                }
              }}
              onDrop={(e) => {
                e.preventDefault()
                if (!draggingKey) return
                const keys = visibleSessions.map(sessionKey)
                const withoutDragged = keys.filter(k => k !== draggingKey)
                withoutDragged.unshift(draggingKey)
                const idx = withoutDragged.indexOf(draggingKey)
                const before = idx > 0 ? withoutDragged[idx - 1] : null
                const after = idx < withoutDragged.length - 1 ? withoutDragged[idx + 1] : null
                const newRank = generateKeyBetween(before ? sessionOrderRanks[before] ?? null : null, after ? sessionOrderRanks[after] ?? null : null)
                setSessionOrderRank(draggingKey, newRank)
                setDraggingKey(null)
                setDropIndicator(null)
              }}
            >
              {dropIndicator?.key === '__list-start__' && (
                <div className="absolute top-0 left-0 right-0 h-1 bg-accent-green rounded-full pointer-events-none z-10 shadow-[0_0_8px_rgba(89,212,153,0.4)]" />
              )}
            </li>
          )}

          {/* Unified ordered list — groups appear at their natural position */}
          {viewMode === 'project' && (collapsed ? (
            unifiedItems.map(item => {
              if (item.kind === 'session') {
                return renderSessionItem(item.session, false)
              }
              return renderGroupItem(item.group, item.sessions, false)
            })
          ) : (
            projectGroups.map(section => {
              const open = !collapsedProjects.has(section.key)
              const sectionSessions = section.items.flatMap(it => it.kind === 'session' ? [it.session] : it.sessions)
              const dotColor = sectionSessions.some(s => projectionOf(s).needsAttention)
                ? 'var(--warning)'
                : sectionSessions.some(s => statusOf(s) === 'working')
                  ? 'var(--accent-green)'
                  : 'var(--mute)'
              return (
                <li key={`proj:${section.key}`} className={cn('flex flex-col rounded-sm mt-1.5 first:mt-0', !section.online && 'opacity-75')}>
                  {sectionDrop?.key === section.key && sectionDrop.position === 'above' && (
                    <div className="h-1 mb-0.5 bg-accent-green rounded-full pointer-events-none shadow-[0_0_8px_rgba(89,212,153,0.4)]" />
                  )}
                  <button
                    type="button"
                    draggable={!collapsed}
                    onClick={() => toggleProjectCollapsed(section.key)}
                    onDragStart={(e) => { e.stopPropagation(); e.dataTransfer.setData('text/plain', section.key); setDraggingSectionKey(section.key) }}
                    onDragEnd={() => { setDraggingSectionKey(null); setSectionDrop(null) }}
                    onDragOver={(e) => {
                      if (!draggingSectionKey || draggingSectionKey === section.key) return
                      e.preventDefault(); e.stopPropagation()
                      const rect = e.currentTarget.getBoundingClientRect()
                      setSectionDrop({ key: section.key, position: (e.clientY - rect.top) / rect.height < 0.5 ? 'above' : 'below' })
                    }}
                    onDragLeave={(e) => { if (!e.currentTarget.contains(e.relatedTarget as Node)) setSectionDrop(null) }}
                    onDrop={(e) => {
                      if (!draggingSectionKey || draggingSectionKey === section.key) return
                      e.preventDefault(); e.stopPropagation()
                      const rect = e.currentTarget.getBoundingClientRect()
                      const position = (e.clientY - rect.top) / rect.height < 0.5 ? 'above' : 'below'
                      const cur = projectGroups.map(s => s.key)
                      const without = cur.filter(k => k !== draggingSectionKey)
                      const ti = without.indexOf(section.key)
                      if (ti !== -1) {
                        without.splice(position === 'above' ? ti : ti + 1, 0, draggingSectionKey)
                        setProjectOrder(without)
                      }
                      setDraggingSectionKey(null); setSectionDrop(null)
                    }}
                    title={section.path || undefined}
                    className="w-full flex items-center gap-2 px-2.5 py-2 text-left rounded-sm bg-white/[0.04] transition-colors hover:bg-white/[0.07]"
                  >
                    <span className="text-[10px] font-mono text-mute/60 shrink-0 w-3">{open ? '▾' : '▸'}</span>
                    <span className="text-[11px] font-medium truncate flex-1 text-left">{section.label}</span>
                    {section.hostId !== (localHostId ?? '') && (
                      <span
                        className="text-[9px] font-mono text-mute/60 rounded-xs border border-hairline bg-white/[0.03] px-1 py-0.5 shrink-0 truncate max-w-[100px]"
                        title={section.hostName}
                      >
                        {section.hostName}{!section.online && ' · offline'}
                      </span>
                    )}
                    <span className="w-1.5 h-1.5 rounded-full shrink-0" style={{ background: dotColor }} />
                    <span className="text-[10px] font-mono text-mute/40 shrink-0">{section.count}</span>
                  </button>
                  {sectionDrop?.key === section.key && sectionDrop.position === 'below' && (
                    <div className="h-1 mt-0.5 bg-accent-green rounded-full pointer-events-none shadow-[0_0_8px_rgba(89,212,153,0.4)]" />
                  )}
                  {open && (
                    <ul className="space-y-0.5">
                      {section.items.map(item => item.kind === 'session'
                        ? renderSessionItem(item.session, false, false, true)
                        : renderProjectTiledGroup(item.group, item.sessions))}
                    </ul>
                  )}
                </li>
              )
            })
          ))}

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
              ▶
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
              ▶
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
              const sk = sessionKey(session)
              const isSelected = selectedSession === sk
              const proj = projectionOf(session)
              const cmdLabel = proj.runningCommands.join(' · ')
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
                      setContextMenu({ key: sk, id: session.id, name: session.name, label: sessionLabel(session), host: session.host, isWorktree: session.is_worktree ?? false, x: e.clientX, y: e.clientY })
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
                        proj.signal.state === 'working'
                          ? 'bg-success animate-[pulse_1.5s_ease-in-out_infinite]'
                          : 'bg-muted-foreground/40',
                      )}
                      title={proj.signal.state === 'working' ? 'running' : 'idle'}
                    />
                    {renamingSession?.key === sk ? (
                      <input
                        ref={renameInputRef}
                        value={renameValue}
                        onChange={(e) => setRenameValue(e.target.value)}
                        onKeyDown={(e) => {
                          e.stopPropagation()
                          if (e.key === 'Enter') submitRename()
                          if (e.key === 'Escape') setRenamingSession(null)
                        }}
                        onBlur={submitRename}
                        onClick={(e) => e.stopPropagation()}
                        className="flex-1 text-sm text-ink bg-surface-elevated border border-primary rounded-sm px-1.5 py-0.5 outline-none font-sans font-medium"
                      />
                    ) : (
                      <span className="text-[12px] font-medium tracking-tight shrink-0 text-mute">
                        {sessionLabel(session)}
                      </span>
                    )}
                    {cmdLabel && (
                      <span
                        className="min-w-0 flex-1 truncate text-[11px] text-mute/50 font-mono"
                        title={cmdLabel}
                      >
                        {cmdLabel}
                      </span>
                    )}
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
          {glance && <><span>·</span><span>{glance.working} working</span></>}
          {glance && glance.waiting > 0 && <><span>·</span><span className="text-warning font-bold">{glance.waiting} waiting</span></>}
          <span>·</span>
          <span>{agentCount} agent{agentCount === 1 ? '' : 's'}</span>
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

      {contextMenu && (
        <div
          ref={menuRef}
          className="fixed bg-surface-elevated border border-hairline rounded-md py-1 z-[1000] min-w-[140px]"
          style={{ left: contextMenu.x, top: contextMenu.y }}
          onClick={(e) => e.stopPropagation()}
        >
          <div
            onClick={() => canRenameContextTarget && startRename({
              key: contextMenu.key,
              name: contextMenu.name,
              label: contextMenu.label,
              host: contextMenu.host,
            })}
            className={cn(
              'px-3 py-1.5 text-sm text-ink hover:bg-surface-card hover:text-ink',
              canRenameContextTarget ? 'cursor-pointer' : 'cursor-not-allowed opacity-50',
            )}
          >
            Rename
          </div>
          <div
            onClick={() => canRenameContextTarget && aiNameSession(contextMenu.name, contextMenu.host)}
            className={cn(
              'px-3 py-1.5 text-sm text-ink hover:bg-surface-card hover:text-ink',
              canRenameContextTarget ? 'cursor-pointer' : 'cursor-not-allowed opacity-50',
            )}
          >
            AI rename
          </div>
          <div
            onClick={() => toggleHide(contextMenu.key)}
            className="px-3 py-1.5 text-sm text-ink cursor-pointer hover:bg-surface-card hover:text-ink"
          >
            {hiddenSet.has(contextMenu.key) ? 'Unhide' : 'Hide'}
          </div>
          <div
            onClick={() => toggleBackground(contextMenu.key)}
            className="px-3 py-1.5 text-sm text-ink cursor-pointer hover:bg-surface-card hover:text-ink"
          >
            {backgroundSet.has(contextMenu.key) ? 'Foreground' : 'Background'}
          </div>
          <div className="my-1 border-t border-hairline" />
          <div
            onClick={() => {
              if (confirmKillKey === contextMenu.key) {
                killSession(contextMenu.id, contextMenu.name, contextMenu.host)
              } else {
                setConfirmKillKey(contextMenu.key)
              }
            }}
            className="px-3 py-1.5 text-sm cursor-pointer text-red-400 hover:bg-red-500/10"
          >
            {confirmKillKey === contextMenu.key ? 'Confirm kill?' : 'Kill'}
          </div>
          {contextMenu.isWorktree && (
            <div
              onClick={() => {
                if (confirmWorktreeKillKey === contextMenu.key) {
                  killSessionAndWorktree(contextMenu.id, contextMenu.name, contextMenu.host)
                } else {
                  setConfirmWorktreeKillKey(contextMenu.key)
                }
              }}
              className="px-3 py-1.5 text-sm cursor-pointer text-red-400 hover:bg-red-500/10"
            >
              {confirmWorktreeKillKey === contextMenu.key ? 'Confirm remove worktree?' : 'Kill + remove worktree'}
            </div>
          )}
        </div>
      )}
      </aside>
    </div>
  )
}
