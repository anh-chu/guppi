import { useCallback, useEffect, useMemo, useState, type MouseEvent as ReactMouseEvent, type DOMAttributes } from 'react'
import { Session, sessionKey, sessionScheduleID, sessionCwd } from '../hooks/useSessions'
import { Host } from '../hooks/useHosts'
import { ToolEvent } from '../hooks/useToolEvents'
import { ActivitySnapshot } from '../hooks/useActivity'
import { toolColors, statusConfig, signalTreatment } from '../theme'
import { stateRank, sessionSignal, isSessionActive, isToolSession, SessionState } from '../lib/sessionState'
import { formatSessionUptime, formatSystemUptime } from '../lib/time'
import { sessionLabel } from '../hooks/useSessions'
import { SessionActionsMenu, SessionMenuTarget } from './SessionActionsMenu'
import { Terminal } from './Terminal'
import { useGlance, GlanceTarget } from './GlancePopover'
import { pathLeaf } from '../lib/path'
import { useSchedules } from '../hooks/useSchedules'

interface OverviewProps {
  sessions: Session[]
  hosts: Host[]
  hiddenSet: Set<string>
  backgroundSet: Set<string>
  scheduleIDs: Map<string, string>
  onSessionSelect: (session: Session) => void
  getSessionEvents: (session: string) => ToolEvent[]
  getSessionActivity: (session: string) => ActivitySnapshot | undefined
  isSessionInActiveTurn: (key: string) => boolean
  onJumpToSession: (session: string, windowIndex?: number, pane?: string) => void
  onDismissAlert: (evt: ToolEvent) => void
  setSessionAttr: (key: string, next: { background?: boolean; hidden?: boolean }) => void
  onSessionKilled?: (key: string) => void
  onOpenFile?: (path: string, cwd?: string, hostId?: string, sessionName?: string) => boolean
  // Tiled layout groups (sessionKeys per group). Used to fold non-agent
  // "tool" panes (build/dev terminals) into the agent card they were tiled with.
  layoutGroups?: { leaves: string[]; activeKey: string | null }[]
}

interface SystemStats {
  os: string
  arch: string
  cpus: number
  goroutines: number
  termyard_mem_mb: number
  load?: { '1m': number; '5m': number; '15m': number }
  uptime_seconds?: number
  memory?: { total_mb: number; used_mb: number; available_mb: number; percent: number }
  cpu_percent?: number
  processes?: { name: string; count: number }[]
}

interface Stats {
  sessions: { total: number; attached: number; detached: number }
  windows: number
  panes: number
  agent_panes: number
  agents: { active: number; waiting: number; stuck: number; error: number }
  processes: { name: string; count: number }[]
  system?: SystemStats
}

const agentCommands = new Set(['claude', 'codex', 'copilot', 'opencode'])


function ProcessBar({ processes, totalPanes }: { processes: { name: string; count: number }[]; totalPanes: number }) {
  if (processes.length === 0) return null
  const max = processes[0]?.count || 1
  return (
    <div className="flex flex-col gap-2">
      {processes.slice(0, 10).map(p => {
        const isAgent = agentCommands.has(p.name)
        return (
          <div key={p.name} className="flex items-center gap-3">
            <span className="w-[92px] text-xs font-semibold text-right overflow-hidden text-ellipsis whitespace-nowrap shrink-0" style={{ color: isAgent ? 'var(--ink)' : 'var(--mute)' }}>
              {p.name}
            </span>
            <div className="flex-1 h-1.5 bg-surface-elevated rounded-full overflow-hidden">
              <div
                className="h-full rounded-full min-w-[2px]"
                style={{
                  width: `${(p.count / max) * 100}%`,
                  background: isAgent ? (toolColors[p.name] || 'var(--chart-secondary)') : 'var(--border)',
                }}
              />
            </div>
            <span className="text-xs font-bold text-mute/50 w-[48px] shrink-0 text-right">{p.count}</span>
          </div>
        )
      })}
      <div className="text-[10px] text-mute/50">{totalPanes} panes total</div>
    </div>
  )
}

function SystemStatsCard({ system }: { system: SystemStats }) {
  return (
    <div className="bg-surface border border-hairline rounded-lg p-5 flex flex-col gap-4">
      {system.cpu_percent !== undefined && <div title={`CPU ${system.cpu_percent}%`}><div className="h-1.5 rounded-full bg-surface-elevated overflow-hidden"><div className="h-full rounded-full" style={{ width: `${Math.min(system.cpu_percent, 100)}%`, background: system.cpu_percent > 90 ? 'var(--destructive)' : system.cpu_percent > 70 ? 'var(--warning)' : 'var(--chart-primary)' }} /></div></div>}
      {system.memory && <div title={`MEM ${system.memory.percent}%`}><div className="h-1.5 rounded-full bg-surface-elevated overflow-hidden"><div className="h-full rounded-full" style={{ width: `${Math.min(system.memory.percent, 100)}%`, background: system.memory.percent > 90 ? 'var(--destructive)' : system.memory.percent > 70 ? 'var(--warning)' : 'var(--chart-secondary)' }} /></div></div>}
      <div className="grid grid-cols-2 gap-4 mt-2">
        {system.load && <div><div className="text-xs font-bold text-mute/50 mb-1.5">Load average</div><div className="text-[14px] text-ink font-mono font-medium">{system.load['1m'].toFixed(2)} <span className="text-mute">{system.load['5m'].toFixed(2)}</span> <span className="text-mute/60">{system.load['15m'].toFixed(2)}</span></div></div>}
        {system.memory && <div><div className="text-xs font-bold text-mute/50 mb-1.5">Memory</div><div className="text-[14px] text-ink font-medium">{(system.memory.used_mb / 1024).toFixed(1)}<span className="text-mute/60 font-normal"> / {(system.memory.total_mb / 1024).toFixed(1)} GB</span></div></div>}
        {system.uptime_seconds !== undefined && <div><div className="text-xs font-bold text-mute/50 mb-1.5">System uptime</div><div className="text-[14px] text-ink font-medium">{formatSystemUptime(system.uptime_seconds)}</div></div>}
        <div><div className="text-xs font-bold text-mute/50 mb-1.5">CPUs</div><div className="text-[14px] text-ink font-medium">{system.cpus} <span className="text-mute/60 font-normal">{system.arch}</span></div></div>
      </div>
    </div>
  )
}

function HostStatsSection({ host, totalPanes }: { host: Host; totalPanes: number }) {
  const hostStats = host.stats as SystemStats | undefined
  if (!hostStats) return null
  const processes = hostStats.processes || []
  return (
    <div className="mb-8">
      <h3 className="font-display text-[13px] font-bold text-ink mb-4 flex items-center gap-2">
        <span className={`w-1.5 h-1.5 rounded-full ${host.online ? 'bg-success' : 'bg-stone'}`} />
        {host.name}
      </h3>
      <div className="grid grid-cols-[repeat(auto-fit,minmax(320px,1fr))] gap-4">
        {processes.length > 0 && <div><div className="text-xs font-bold text-mute/50 mb-2.5 ml-1">Processes</div><div className="bg-surface border border-hairline rounded-lg p-5"><ProcessBar processes={processes} totalPanes={totalPanes} /></div></div>}
        <div><div className="text-xs font-bold text-mute/50 mb-2.5 ml-1">System</div><SystemStatsCard system={hostStats} /></div>
      </div>
    </div>
  )
}

type CardItem = {
  session: Session
  key: string
  signal: ReturnType<typeof sessionSignal>
  event: ToolEvent | undefined
  events: ToolEvent[]
  activity: ActivitySnapshot | undefined
  scheduleRunCount?: number
}

const COLUMN_ORDER: SessionState[] = ['needs_you', 'working', 'idle', 'offline']
const COLUMN_META: Record<SessionState, { label: string; color: string }> = {
  needs_you: { label: 'Needs you', color: 'var(--warning)' },
  working: { label: 'Working', color: 'var(--success)' },
  idle: { label: 'Idle', color: 'var(--mute)' },
  offline: { label: 'Offline', color: 'var(--mute)' },
}

function SessionCard({
  item,
  hasMultipleHosts,
  getSessionEvents,
  onOpen,
  onJumpToSession,
  onDismissAlert,
  onContextMenu,
  selected,
  glanceTrigger,
  mates,
}: {
  item: CardItem
  hasMultipleHosts: boolean
  getSessionEvents: (session: string) => ToolEvent[]
  onOpen: (session: Session) => void
  onJumpToSession: (session: string, windowIndex?: number, pane?: string) => void
  onDismissAlert: (evt: ToolEvent) => void
  onContextMenu: (e: ReactMouseEvent, item: CardItem) => void
  selected: boolean
  glanceTrigger: (t: GlanceTarget) => DOMAttributes<HTMLElement>
  mates?: CardItem[]
}) {
  const { session, key, signal, event, events, activity, scheduleRunCount } = item
  const isWaiting = signal.state === 'needs_you'
  const loudEvent = event || getSessionEvents(key).find(e => e.status === 'waiting' || e.status === 'stuck' || e.status === 'error')
  // Mirror the sidebar: the user prompt is the task (the "what"), the live
  // activity label / last agent message is the status shown beneath it.
  const userPrompt = session.user_prompt?.trim() || ''
  const activityText = (events.find(e => e.status === 'active' && !e.auto_detected)?.message || session.last_agent_message?.trim() || session.prompt_preview?.trim() || '')
  const taskPrimary = userPrompt || activityText
  const taskSecondary = userPrompt && activityText && activityText !== userPrompt ? activityText : ''
  return (
    <button
      key={key}
      {...glanceTrigger({ name: session.name, host: session.host, display_name: session.display_name, host_name: session.host_name })}
      onClick={() => {
        if (signal.state === 'needs_you' && loudEvent) onJumpToSession(loudEvent.host ? `${loudEvent.host}/${loudEvent.session}` : loudEvent.session, loudEvent.window, loudEvent.pane)
        else onOpen(session)
      }}
      onContextMenu={(e) => onContextMenu(e, item)}
      className={`text-left border bg-surface rounded-sm p-4 min-h-36 flex flex-col gap-2 transition-all ${signal.state === 'offline' ? 'opacity-60' : ''} ${selected ? 'ring-1 ring-primary' : ''}`}
      style={{
        borderColor: isWaiting ? 'var(--warning)' : 'var(--hairline)',
        boxShadow: isWaiting ? '0 0 0 1px var(--warning), 0 0 12px color-mix(in oklch, var(--warning) 25%, transparent)' : undefined,
      }}
    >
      <div className="flex items-start gap-2">
        <div className="min-w-0 flex-1">
          <div className="flex items-start gap-2">
            <span className={`font-display text-[13px] font-bold truncate ${signal.state === 'idle' ? 'text-mute' : 'text-ink'}`}>{session.display_name || session.name}</span>
            {scheduleRunCount && scheduleRunCount > 1 && <span className="text-[10px] font-bold text-mute/50 shrink-0" title={`${scheduleRunCount} runs`}>×{scheduleRunCount}</span>}
          </div>
          <div className="text-[10px] text-mute/60">
            {hasMultipleHosts && <span className="text-mute/50">{session.host_name || 'Local'} · </span>}
            {session.project_path && <span className="text-mute/70" title={session.project_path}>{pathLeaf(session.project_path)} · </span>}
            {formatSessionUptime(session.created)}
          </div>
        </div>
        {signal.state === 'needs_you' && loudEvent && (
          <button
            onClick={(e) => { e.stopPropagation(); onDismissAlert(loudEvent) }}
            className="text-mute/30 hover:text-ink transition-colors leading-none"
            title="Dismiss alert"
          >
            ×
          </button>
        )}
      </div>

      <div className="flex items-center gap-3 text-xs font-bold text-mute/70">
        <span className="flex items-center gap-1.5"><svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="3" strokeLinecap="round" strokeLinejoin="round"><rect x="3" y="3" width="18" height="18" rx="2" ry="2"/></svg>{session.windows?.length || 0}</span>
        {signal.agentCount > 0 && <span className="flex items-center gap-1.5" style={{ color: toolColors.claude }}><svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="3" strokeLinecap="round" strokeLinejoin="round"><path d="M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z" /></svg>{signal.agentCount}</span>}
        {signal.state === 'working' ? <span className="text-success">working</span> : signal.state === 'idle' ? <span className="text-mute/40">idle</span> : signal.state === 'offline' ? <span className="text-mute/40">offline</span> : signal.state === 'needs_you' ? <span className="text-warning font-bold">needs you</span> : null}
        {mates && mates.length > 0 && <span className="flex items-center gap-1 text-mute/50" title={`${mates.length + 1} tiled panes`}>⧉{mates.length + 1}</span>}
      </div>

      {signal.state === 'needs_you' && loudEvent ? (
        <div className="text-[12px] text-ink leading-snug flex-1">
          <span style={{ color: toolColors[loudEvent.tool] || 'var(--mute)' }} className="font-bold">{loudEvent.tool.toUpperCase()}</span>{' '}
          <span style={{ color: signalTreatment[signal.state].text }} className="font-medium">{(statusConfig[loudEvent.status] || { label: loudEvent.status }).label}</span>{' '}
          <span className="text-mute/70">{loudEvent.host_name ? `${loudEvent.host_name}: ` : ''}{loudEvent.message || 'Needs attention'}</span>
        </div>
      ) : (
        <>
          {taskPrimary && (
            <div className="flex flex-col gap-0.5">
              <div className="text-[12px] text-ink/85 leading-snug line-clamp-2">{taskPrimary}</div>
              {taskSecondary && <div className="text-[11px] text-mute/60 leading-snug line-clamp-2">{taskSecondary}</div>}
            </div>
          )}
          {!taskPrimary && <div className="mt-auto text-[12px] text-mute/60">{isSessionActive(session) ? 'active' : 'calm'}</div>}
        </>
      )}
      {mates && mates.length > 0 && (
        <div className="pt-2 mt-1 border-t border-hairline/40 flex flex-col gap-1">
          {mates.map(m => (
            <div
              key={m.key}
              {...glanceTrigger({ name: m.session.name, host: m.session.host, display_name: m.session.display_name, host_name: m.session.host_name })}
              onClick={(e) => { e.stopPropagation(); onOpen(m.session) }}
              className="flex items-center gap-1.5 text-[11px] text-mute/60 hover:text-ink cursor-pointer truncate"
              title={`Open ${m.session.display_name || m.session.name}`}
            >
              <span className="text-mute/30">↳</span>
              <span className="truncate font-mono">{m.session.display_name || m.session.name}</span>
            </div>
          ))}
        </div>
      )}
    </button>
  )
}

export function Overview({ sessions, hosts, hiddenSet, backgroundSet, scheduleIDs, onSessionSelect, getSessionEvents, getSessionActivity, isSessionInActiveTurn, onJumpToSession, onDismissAlert, setSessionAttr, onSessionKilled, onOpenFile, layoutGroups }: OverviewProps) {
  const { schedules } = useSchedules()
  const scheduleById = useMemo(() => new Map(schedules.map(s => [s.id, s])), [schedules])
  const scheduleIdFor = useCallback((session: Session) => (
    scheduleIDs.get(sessionKey(session)) || scheduleIDs.get(session.name) || sessionScheduleID(session)
  ), [scheduleIDs])
  const [stats, setStats] = useState<Stats | null>(null)
  const [menu, setMenu] = useState<{ target: SessionMenuTarget; x: number; y: number } | null>(null)
  const openMenu = useCallback((e: ReactMouseEvent, item: CardItem) => {
    e.preventDefault()
    const s = item.session
    setMenu({
      target: { key: item.key, id: s.id, name: s.name, label: sessionLabel(s), host: s.host, isWorktree: s.is_worktree ?? false },
      x: e.clientX,
      y: e.clientY,
    })
  }, [])
  const hasMultipleHosts = hosts.length > 1
  const localHostId = useMemo(() => hosts.find(h => h.local)?.id, [hosts])
  const glance = useGlance(hasMultipleHosts)
  // Docked live-terminal split. Engages only on wide, fine-pointer viewports;
  // otherwise a card click falls back to full-view nav.
  const [selected, setSelected] = useState<Session | null>(null)
  const [canDock, setCanDock] = useState(() => typeof window !== 'undefined' && window.matchMedia('(min-width: 900px) and (pointer: fine)').matches)
  useEffect(() => {
    const mq = window.matchMedia('(min-width: 900px) and (pointer: fine)')
    const sync = () => { setCanDock(mq.matches); if (!mq.matches) setSelected(null) }
    mq.addEventListener('change', sync)
    return () => mq.removeEventListener('change', sync)
  }, [])
  const handleCardOpen = useCallback((session: Session) => {
    if (canDock) setSelected(session)
    else onSessionSelect(session)
  }, [canDock, onSessionSelect])
  // Drop the dock if its session disappears (e.g. killed).
  useEffect(() => {
    if (selected && !sessions.some(s => sessionKey(s) === sessionKey(selected))) setSelected(null)
  }, [sessions, selected])
  const split = !!selected
  const selectedKey = selected ? sessionKey(selected) : null
  const [splitWidth, setSplitWidth] = useState(() => {
    const v = parseInt(localStorage.getItem('overview_split_width') || '', 10)
    return Number.isFinite(v) && v >= 360 ? Math.min(v, 1200) : 520
  })
  useEffect(() => { localStorage.setItem('overview_split_width', String(Math.round(splitWidth))) }, [splitWidth])
  const startResize = useCallback((e: React.PointerEvent) => {
    e.preventDefault()
    const onMove = (ev: PointerEvent) => {
      const max = Math.max(window.innerWidth - 320, 360) // keep >=320px for the board
      setSplitWidth(Math.min(Math.max(window.innerWidth - ev.clientX, 360), max))
    }
    const onUp = () => {
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onUp)
      document.body.style.userSelect = ''
      document.body.style.cursor = ''
    }
    document.body.style.userSelect = 'none'
    document.body.style.cursor = 'col-resize'
    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
  }, [])
  const [layout, setLayout] = useState<'grid' | 'board'>(() => (localStorage.getItem('overview_layout') === 'grid' ? 'grid' : 'board'))
  useEffect(() => { localStorage.setItem('overview_layout', layout) }, [layout])
  const [hiddenRailOpen, setHiddenRailOpen] = useState(() => localStorage.getItem('overview_rail_hidden') === 'open')
  const [bgRailOpen, setBgRailOpen] = useState(() => localStorage.getItem('overview_rail_bg') === 'open')
  const [scheduledRailOpen, setScheduledRailOpen] = useState(() => localStorage.getItem('overview_rail_scheduled') === 'open')
  useEffect(() => { localStorage.setItem('overview_rail_hidden', hiddenRailOpen ? 'open' : 'closed') }, [hiddenRailOpen])
  useEffect(() => { localStorage.setItem('overview_rail_bg', bgRailOpen ? 'open' : 'closed') }, [bgRailOpen])
  useEffect(() => { localStorage.setItem('overview_rail_scheduled', scheduledRailOpen ? 'open' : 'closed') }, [scheduledRailOpen])

  // Sessions spawned by a schedule are recurring/background noise: they pile
  // up run after run and would otherwise flood the board columns. Pull them
  // out into their own collapsed rail (newest run per schedule, badge = total).
  const scheduledSet = useMemo(() => {
    const set = new Set<string>()
    for (const s of sessions) {
      if (scheduleIdFor(s)) set.add(sessionKey(s))
    }
    return set
  }, [sessions, scheduleIdFor])

  const foregroundSessions = useMemo(() => sessions.filter(s => !hiddenSet.has(sessionKey(s)) && !backgroundSet.has(sessionKey(s)) && !scheduledSet.has(sessionKey(s))), [sessions, hiddenSet, backgroundSet, scheduledSet])
  const hiddenCount = sessions.length - foregroundSessions.length - scheduledSet.size

  useEffect(() => {
    const fetchStats = async () => {
      try {
        const res = await fetch('/api/stats')
        if (res.ok) setStats(await res.json())
      } catch {}
    }
    fetchStats()
    const interval = setInterval(fetchStats, 5000)
    return () => clearInterval(interval)
  }, [])

  const buildItem = useCallback((session: Session): CardItem => {
    const key = sessionKey(session)
    const events = getSessionEvents(key)
    const activity = getSessionActivity(key)
    const signal = sessionSignal(session, events, activity, isSessionInActiveTurn(key))
    const event = events.find(e => e.status === 'waiting' || e.status === 'stuck' || e.status === 'error')
    return { session, key, signal, event, events, activity }
  }, [getSessionEvents, getSessionActivity, isSessionInActiveTurn])

  const items = useMemo<CardItem[]>(() => foregroundSessions.map(buildItem), [foregroundSessions, buildItem])

  // Fold tiled "tool" panes (no agent) into the agent card they were tiled with.
  // Per tile group: agent sessions keep their own card; non-agent sessions become
  // mate rows under the group's primary agent card and drop out of the board columns.
  const { matesByCard, hiddenMateKeys } = useMemo(() => {
    const itemByKey = new Map(items.map(it => [it.key, it]))
    const matesByCard = new Map<string, CardItem[]>()
    const hiddenMateKeys = new Set<string>()
    for (const g of layoutGroups ?? []) {
      if (g.leaves.length < 2) continue
      const leaf = g.leaves.map(k => itemByKey.get(k)).filter((x): x is CardItem => !!x)
      const tools = leaf.filter(i => isToolSession(i.session, i.events))
      const agents = leaf.filter(i => !tools.includes(i))
      if (agents.length === 0 || tools.length === 0) continue
      const primary = agents.find(i => i.key === g.activeKey) ?? agents[0]
      matesByCard.set(primary.key, [...(matesByCard.get(primary.key) ?? []), ...tools])
      for (const t of tools) hiddenMateKeys.add(t.key)
    }
    return { matesByCard, hiddenMateKeys }
  }, [items, layoutGroups])
  const hiddenItems = useMemo<CardItem[]>(() => sessions.filter(s => hiddenSet.has(sessionKey(s)) && !scheduledSet.has(sessionKey(s))).map(buildItem), [sessions, hiddenSet, scheduledSet, buildItem])
  const bgItems = useMemo<CardItem[]>(() => sessions.filter(s => !hiddenSet.has(sessionKey(s)) && backgroundSet.has(sessionKey(s)) && !scheduledSet.has(sessionKey(s))).map(buildItem), [sessions, hiddenSet, backgroundSet, scheduledSet, buildItem])

  // Collapse every run of a schedule down to its newest session, with a badge
  // for the total run count, so repeated fires don't multiply cards in the rail.
  const scheduledItems = useMemo<CardItem[]>(() => {
    const groups = new Map<string, Session[]>()
    for (const s of sessions) {
      const sid = scheduleIdFor(s)
      if (!sid) continue
      if (!groups.has(sid)) groups.set(sid, [])
      groups.get(sid)!.push(s)
    }
    const out: CardItem[] = []
    for (const [sid, group] of groups) {
      const newest = group.slice().sort((a, b) => (b.created || '').localeCompare(a.created || ''))[0]
      if (!newest) continue
      const item = buildItem(newest)
      out.push({ ...item, scheduleRunCount: group.length })
    }
    return out
  }, [sessions, scheduleIdFor, buildItem])

  const grouped = useMemo(() => {
    const groups = new Map<string, CardItem[]>()
    for (const item of items) {
      const groupLabel = hasMultipleHosts ? (item.session.host_name || 'Local') : 'Sessions'
      if (!groups.has(groupLabel)) groups.set(groupLabel, [])
      groups.get(groupLabel)!.push(item)
    }
    return Array.from(groups.entries())
      .sort(([a], [b]) => (a === 'Local' || a === 'Sessions' ? -1 : b === 'Local' || b === 'Sessions' ? 1 : a.localeCompare(b)))
      .map(([groupLabel, groupItems]) => ({
        groupLabel,
        items: groupItems.sort((a, b) => stateRank[a.signal.state] - stateRank[b.signal.state] || a.session.name.localeCompare(b.session.name)),
      }))
  }, [items, hasMultipleHosts])

  const byState = useMemo(() => COLUMN_ORDER.map(state => ({
    state,
    items: items
      .filter(i => i.signal.state === state && !hiddenMateKeys.has(i.key))
      .sort((a, b) => {
        // needs_you: longest-blocked first (oldest loud event); else group by host, then name
        if (state === 'needs_you') return (a.event?.timestamp || '').localeCompare(b.event?.timestamp || '')
        const aLocal = !a.session.host || a.session.host === localHostId
        const bLocal = !b.session.host || b.session.host === localHostId
        const ah = a.session.host_name || ''
        const bh = b.session.host_name || ''
        return (aLocal && !bLocal ? -1 : bLocal && !aLocal ? 1 : ah.localeCompare(bh)) || a.session.name.localeCompare(b.session.name)
      }),
  })), [items, hiddenMateKeys, localHostId])

  const activeHostSections = hosts.filter(h => h.stats)

  const renderRail = (railKey: string, label: string, railItems: CardItem[], open: boolean, setOpen: (v: boolean) => void) => {
    if (railItems.length === 0) return null
    if (!open) return (
      <button key={railKey} onClick={() => setOpen(true)} title={`Show ${label.toLowerCase()}`}
        className="shrink-0 w-9 self-stretch min-h-[120px] flex flex-col items-center gap-3 py-3 rounded-sm border border-hairline bg-surface text-mute/50 hover:text-ink hover:border-hairline transition-colors">
        <span className="text-[12px] font-bold">{railItems.length}</span>
        <span className="text-[11px] font-bold tracking-wide [writing-mode:vertical-rl] rotate-180">{label}</span>
      </button>
    )
    return (
      <div key={railKey} className="min-w-[260px] flex flex-col gap-2" style={{ flexGrow: railItems.length, flexBasis: 0 }}>
        <h3 className="font-display text-[13px] font-bold text-mute mb-1 flex items-center gap-2">
          <span className="w-1.5 h-1.5 rounded-full bg-mute/40" />
          {label}
          <span className="text-mute/40 font-bold text-xs">({railItems.length})</span>
          <button onClick={() => setOpen(false)} className="ml-auto text-mute/40 hover:text-ink leading-none" title="Collapse">×</button>
        </h3>
        <div className="grid grid-cols-[repeat(auto-fill,minmax(240px,1fr))] gap-2 items-start">
          {railItems.map(item => (
            <SessionCard key={item.key} item={item} hasMultipleHosts={hasMultipleHosts} getSessionEvents={getSessionEvents} onOpen={handleCardOpen} onJumpToSession={onJumpToSession} onDismissAlert={onDismissAlert} onContextMenu={openMenu} selected={selectedKey === item.key} glanceTrigger={glance.trigger} />
          ))}
        </div>
      </div>
    )
  }

  return (
    <div className="flex-1 flex min-h-0">
    <div className="flex-1 min-w-0 p-8 overflow-y-auto font-sans text-sm font-medium bg-canvas">
      <div className="flex items-center justify-end mb-4">
        <div className="inline-flex rounded-sm border border-hairline overflow-hidden text-[11px] font-bold">
          {(['grid', 'board'] as const).map(l => (
            <button
              key={l}
              onClick={() => setLayout(l)}
              className={`px-3 py-1.5 transition-colors ${layout === l ? 'bg-surface-elevated text-ink' : 'text-mute/60 hover:text-ink'}`}
            >
              {l === 'grid' ? 'Grid' : 'Board'}
            </button>
          ))}
        </div>
      </div>

      {foregroundSessions.length === 0 && (
        <div className="mb-10">
          <div className="text-mute text-[13px] font-medium mb-4 ml-1">
            {sessions.length === 0 ? 'No sessions found. Create a session to get started.' : 'All sessions are hidden or backgrounded.'}
            {hiddenCount > 0 && <span className="text-mute/50"> {' '}({hiddenCount} hidden/backgrounded)</span>}
          </div>
          <div className="grid grid-cols-3 gap-2 max-w-3xl">
            {Array.from({ length: 6 }).map((_, i) => <div key={i} className="tex-yardgrid border border-hairline/40 rounded-sm aspect-[4/3] opacity-50" />)}
          </div>
        </div>
      )}

      {layout === 'board' ? (
        <div className="flex gap-3 mb-10 items-start overflow-x-auto tex-yardgrid rounded-sm p-3">
          {byState.filter(({ items: colItems }) => colItems.length > 0).map(({ state, items: colItems }) => (
            <div key={state} className={`flex flex-col gap-2 ${colItems.length === 0 ? 'min-w-[160px]' : split ? 'min-w-[220px]' : 'min-w-[260px]'}`} style={{ flexGrow: Math.max(colItems.length, 0.5), flexBasis: 0 }}>
              <h3 className="font-display text-[13px] font-bold text-ink mb-1 flex items-center gap-2">
                <span className="w-1.5 h-1.5 rounded-full" style={{ background: COLUMN_META[state].color }} />
                {COLUMN_META[state].label}
                <span className="text-mute/40 font-bold text-xs">({colItems.length})</span>
              </h3>
              <div className={split ? 'grid grid-cols-[repeat(auto-fill,minmax(200px,1fr))] gap-2 items-start' : 'grid grid-cols-[repeat(auto-fill,minmax(240px,1fr))] gap-2 items-start'}>
                {colItems.map(item => (
                  <SessionCard key={item.key} item={item} hasMultipleHosts={hasMultipleHosts} getSessionEvents={getSessionEvents} onOpen={handleCardOpen} onJumpToSession={onJumpToSession} onDismissAlert={onDismissAlert} onContextMenu={openMenu} selected={selectedKey === item.key} glanceTrigger={glance.trigger} mates={matesByCard.get(item.key)} />
                ))}
              </div>
            </div>
          ))}
          {renderRail('scheduled', 'Scheduled', scheduledItems, scheduledRailOpen, setScheduledRailOpen)}
          {renderRail('hidden', 'Hidden', hiddenItems, hiddenRailOpen, setHiddenRailOpen)}
          {renderRail('backgrounded', 'Backgrounded', bgItems, bgRailOpen, setBgRailOpen)}
        </div>
      ) : (
        grouped.map(({ groupLabel, items: groupItems }) => (
          <div key={groupLabel} className="mb-10">
            <h3 className="font-display text-[13px] font-bold text-ink mb-4 flex items-center gap-2">
              {groupLabel}
              {hasMultipleHosts && <span className={`text-[10px] font-medium ${groupItems[0]?.session.host_online !== false ? 'text-success' : 'text-mute/50'}`}>{groupItems[0]?.session.host_online !== false ? 'online' : 'offline'}</span>}
              <span className="text-mute/40 font-bold text-xs ml-1">({groupItems.length})</span>
            </h3>
            <div className={split ? 'grid grid-cols-[repeat(auto-fill,minmax(200px,1fr))] gap-2' : 'grid grid-cols-[repeat(auto-fill,minmax(220px,1fr))] gap-2'}>
              {groupItems.map(item => (
                <SessionCard key={item.key} item={item} hasMultipleHosts={hasMultipleHosts} getSessionEvents={getSessionEvents} onOpen={handleCardOpen} onJumpToSession={onJumpToSession} onDismissAlert={onDismissAlert} onContextMenu={openMenu} selected={selectedKey === item.key} glanceTrigger={glance.trigger} />
              ))}
            </div>
          </div>
        ))
      )}

      <details open className="mt-4 rounded-md border border-hairline bg-surface tex-yardgrid overflow-hidden">
        <summary className="cursor-pointer list-none px-4 py-3 flex items-center justify-between font-display text-[13px] text-ink">
          <span>Yard health</span>
          <span className="text-mute/60 text-xs">System stats</span>
        </summary>
        <div className="px-4 pb-4 pt-1">
          {hasMultipleHosts ? (
            activeHostSections.map(host => {
              const paneCount = (host.sessions || []).reduce((n, s: any) => n + (s.windows || []).reduce((wn: number, w: any) => wn + (w.panes || []).length, 0), 0)
              return <HostStatsSection key={host.id} host={host} totalPanes={paneCount} />
            })
          ) : (
            <div className="grid grid-cols-[repeat(auto-fit,minmax(320px,1fr))] gap-4">
              {stats?.processes && stats.processes.length > 0 && <div><h3 className="font-display text-[13px] font-bold text-ink mb-4 ml-1">Processes</h3><div className="bg-surface border border-hairline rounded-lg p-5"><ProcessBar processes={stats.processes} totalPanes={stats.panes} /></div></div>}
              {stats?.system && <div><h3 className="font-display text-[13px] font-bold text-ink mb-4 ml-1">System</h3><SystemStatsCard system={stats.system} /></div>}
            </div>
          )}
        </div>
      </details>

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
        />
      )}
    </div>
    {selected && (
      <>
      <div onPointerDown={startResize} title="Drag to resize" className="shrink-0 w-1.5 cursor-col-resize bg-hairline/40 hover:bg-primary/50 transition-colors" />
      <div className="shrink-0 border-l border-hairline flex flex-col" style={{ width: splitWidth }}>
        <div className="shrink-0 h-9 flex items-center gap-2 px-3 border-b border-hairline bg-surface-elevated/40">
          <span className="font-display text-[13px] font-bold text-ink truncate min-w-0 flex-1">{selected.display_name || selected.name}</span>
          <button title="Open full view" onClick={() => onSessionSelect(selected)} className="p-1.5 rounded-sm bg-surface border border-hairline text-mute hover:text-primary transition-all">
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round">
              <polyline points="15 3 21 3 21 9" /><polyline points="9 21 3 21 3 15" /><line x1="21" y1="3" x2="14" y2="10" /><line x1="3" y1="21" x2="10" y2="14" />
            </svg>
          </button>
          <button title="Close" onClick={() => setSelected(null)} className="p-1.5 rounded-sm bg-surface border border-hairline text-mute hover:text-primary transition-all">
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round">
              <line x1="18" y1="6" x2="6" y2="18" /><line x1="6" y1="6" x2="18" y2="18" />
            </svg>
          </button>
        </div>
        <div className="min-h-0 flex-1 flex flex-col overflow-hidden"><Terminal sessionName={selected.name} hostId={selected.host} backend={selected.backend} onOpenFile={(path) => onOpenFile?.(path, sessionCwd(selected), selected.host, selected.name) ?? false} /></div>
      </div>
      </>
    )}
    {glance.popover}
    </div>
  )
}
