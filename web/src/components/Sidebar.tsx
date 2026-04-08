import { useState, useEffect, useMemo, useRef } from 'react'
import { Session, sessionKey } from '../hooks/useSessions'
import { ToolEvent } from '../hooks/useToolEvents'
import { ActivitySnapshot } from '../hooks/useActivity'
import { usePreferences } from '../hooks/usePreferences'
import { statusConfig, toolColors } from '../theme'
import { cn } from '../lib/utils'
import { AgentMark } from './AgentMark'

interface SidebarProps {
  sessions: Session[]
  selectedSession: string | null
  collapsed: boolean
  collapseMode: 'small' | 'hidden'
  hasMultipleHosts?: boolean
  onSessionSelect: (session: Session) => void
  onSessionRenamed?: (oldName: string, newName: string) => void
  getSessionEvents: (session: string) => ToolEvent[]
  sessionNeedsAttention: (session: string) => boolean
  getSessionActivity: (session: string) => ActivitySnapshot | undefined
}

interface RenameState {
  key: string
  name: string
  host?: string
}

const shellCommands = new Set(['bash', 'zsh', 'fish', 'sh', 'dash', 'ksh', 'csh', 'tcsh', 'tmux', 'login'])

function isSessionActive(session: Session): boolean {
  if (!session.windows) return false
  return session.windows.some(w =>
    w.panes?.some(p => p.current_command && !shellCommands.has(p.current_command))
  )
}

function ToolBadge({ event }: { event: ToolEvent }) {
  const indicator = statusConfig[event.status]
  if (!indicator) return null
  const toolColor = toolColors[event.tool] || 'var(--muted-foreground)'

  return (
    <div
      title={event.message || `${event.tool}: ${indicator.label}`}
      className="inline-flex items-center gap-[3px] px-[5px] py-[1px] rounded-lg text-[10px]"
      style={{
        background: `${toolColor}18`,
        border: `1px solid ${toolColor}40`,
        color: toolColor,
      }}
    >
      <span
        className={cn('w-[5px] h-[5px] rounded-full inline-block', event.status === 'waiting' && 'animate-[pulse_1.5s_ease-in-out_infinite]')}
        style={{ background: indicator.color }}
      />
      {event.tool}
    </div>
  )
}

function Sparkline({ data, height = 16 }: { data: number[]; height?: number }) {
  if (!data || data.length === 0) return null
  const max = Math.max(...data, 1)
  const viewWidth = data.length
  const barWidth = 1
  return (
    <svg viewBox={`0 0 ${viewWidth} ${height}`} preserveAspectRatio="none" width="100%" height={height} className="block">
      {data.map((val, i) => {
        const barHeight = (val / max) * height
        return (
          <rect
            key={i}
            x={i * barWidth}
            y={height - barHeight}
            width={Math.max(barWidth - 0.05, 0.05)}
            height={barHeight}
            style={{ fill: val > 0 ? 'var(--chart-primary)' : 'var(--muted)' }}
            opacity={val > 0 ? 0.7 : 0.3}
          />
        )
      })}
    </svg>
  )
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

function pathLeaf(path?: string): string {
  if (!path) return ''
  const trimmed = path.replace(/[\\/]+$/, '')
  const parts = trimmed.split(/[\\/]/)
  return parts[parts.length - 1] || trimmed
}

function shortSessionId(value?: string): string {
  if (!value) return ''
  return value.length > 8 ? value.slice(0, 8) : value
}

function formatUptime(created?: string): string {
  if (!created) return ''
  const ms = Date.now() - new Date(created).getTime()
  if (!Number.isFinite(ms) || ms < 0) return ''
  const minutes = Math.floor(ms / 60000)
  if (minutes < 1) return 'now'
  if (minutes < 60) return `${minutes}m`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h`
  const days = Math.floor(hours / 24)
  return `${days}d`
}

function orderSessions(sessions: Session[], order: string[]): Session[] {
  const rank = new Map(order.map((value, index) => [value, index]))
  return [...sessions].sort((a, b) => {
    const aRank = rank.get(sessionKey(a))
    const bRank = rank.get(sessionKey(b))
    if (aRank !== undefined && bRank !== undefined) return aRank - bRank
    if (aRank !== undefined) return -1
    if (bRank !== undefined) return 1
    return a.name.localeCompare(b.name)
  })
}

export function Sidebar({
  sessions,
  selectedSession,
  collapsed,
  collapseMode,
  hasMultipleHosts,
  onSessionSelect,
  onSessionRenamed,
  getSessionEvents,
  sessionNeedsAttention,
  getSessionActivity,
}: SidebarProps) {
  const { prefs } = usePreferences()
  const [hiddenSet, setHiddenSet] = useState<Set<string>>(() => new Set(readStoredList('guppi:hidden-sessions')))
  const [manualOrder, setManualOrder] = useState<string[]>(() => readStoredList('guppi:session-order'))
  const [projectFilters, setProjectFilters] = useState<string[]>(() => readStoredList('guppi:project-filters'))
  const [hiddenExpanded, setHiddenExpanded] = useState(false)
  const [renamingSession, setRenamingSession] = useState<RenameState | null>(null)
  const [renameValue, setRenameValue] = useState('')
  const [contextMenu, setContextMenu] = useState<{ key: string; name: string; host?: string; x: number; y: number } | null>(null)
  const [filterOpen, setFilterOpen] = useState(false)
  const [draggingKey, setDraggingKey] = useState<string | null>(null)
  const [dropTargetKey, setDropTargetKey] = useState<string | null>(null)
  const [, setUptimeTick] = useState(0)
  const renameInputRef = useRef<HTMLInputElement>(null)
  const filterRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (renamingSession && renameInputRef.current) {
      renameInputRef.current.focus()
      renameInputRef.current.select()
    }
  }, [renamingSession])

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
      setFilterOpen(false)
    }
    window.addEventListener('click', handler)
    return () => window.removeEventListener('click', handler)
  }, [contextMenu, filterOpen])

  useEffect(() => {
    writeStoredList('guppi:hidden-sessions', [...hiddenSet])
  }, [hiddenSet])

  useEffect(() => {
    writeStoredList('guppi:session-order', manualOrder)
  }, [manualOrder])

  useEffect(() => {
    writeStoredList('guppi:project-filters', projectFilters)
  }, [projectFilters])

  useEffect(() => {
    // Don't clear persisted ordering/hidden state during the initial empty
    // session snapshot before the first /api/sessions refresh completes.
    if (sessions.length === 0) return

    const validKeys = new Set(sessions.map(sessionKey))
    const nextOrder = manualOrder.filter(key => validKeys.has(key))
    if (nextOrder.length !== manualOrder.length) {
      setManualOrder(nextOrder)
    }
    const nextHidden = [...hiddenSet].filter(key => validKeys.has(key))
    if (nextHidden.length !== hiddenSet.size) {
      setHiddenSet(new Set(nextHidden))
    }
  }, [sessions, manualOrder, hiddenSet])

  const projects = useMemo(
    () => Array.from(new Set(sessions.map(s => s.project_path).filter((value): value is string => Boolean(value)))).sort(),
    [sessions],
  )

  useEffect(() => {
    if (projectFilters.length === 0) return
    const validProjects = new Set(projects)
    const nextFilters = projectFilters.filter(project => validProjects.has(project))
    if (nextFilters.length !== projectFilters.length) {
      setProjectFilters(nextFilters)
    }
  }, [projectFilters, projects])

  const orderedSessions = useMemo(() => orderSessions(sessions, manualOrder), [sessions, manualOrder])

  const visibleSessions = useMemo(() => {
    const filtered = orderedSessions.filter(session => !hiddenSet.has(sessionKey(session)))
    if (projectFilters.length === 0) return filtered
    const allowed = new Set(projectFilters)
    return filtered.filter(session => session.project_path && allowed.has(session.project_path))
  }, [orderedSessions, hiddenSet, projectFilters])

  const hiddenSessions = orderedSessions.filter(session => hiddenSet.has(sessionKey(session)))

  const toggleHide = (key: string) => {
    const next = new Set(hiddenSet)
    if (next.has(key)) next.delete(key)
    else next.add(key)
    setHiddenSet(next)
    setContextMenu(null)
  }

  const startRename = (session: RenameState) => {
    setRenamingSession(session)
    setRenameValue(session.name)
    setContextMenu(null)
  }

  const submitRename = async () => {
    if (!renamingSession || !renameValue.trim() || renameValue === renamingSession.name) {
      setRenamingSession(null)
      return
    }
    const nextName = renameValue.trim()
    const oldKey = renamingSession.key
    const newKey = renamingSession.host ? `${renamingSession.host}/${nextName}` : nextName
    try {
      const res = await fetch('/api/session/rename', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          old_name: renamingSession.name,
          new_name: nextName,
          host: renamingSession.host || undefined,
        }),
      })
      if (res.ok) {
        if (hiddenSet.has(oldKey)) {
          const nextHidden = new Set(hiddenSet)
          nextHidden.delete(oldKey)
          nextHidden.add(newKey)
          setHiddenSet(nextHidden)
        }
        if (manualOrder.includes(oldKey)) {
          setManualOrder(current => current.map(key => key === oldKey ? newKey : key))
        }
        onSessionRenamed?.(oldKey, newKey)
      }
    } catch (err) {
      console.error('Failed to rename session:', err)
    }
    setRenamingSession(null)
  }

  const handleDrop = (targetKey: string) => {
    if (!draggingKey || draggingKey === targetKey) {
      setDraggingKey(null)
      setDropTargetKey(null)
      return
    }

    const visibleKeys = visibleSessions.map(sessionKey)
    const oldIndex = visibleKeys.indexOf(draggingKey)
    const newIndex = visibleKeys.indexOf(targetKey)
    if (oldIndex === -1 || newIndex === -1) {
      setDraggingKey(null)
      setDropTargetKey(null)
      return
    }

    const reorderedVisible = [...visibleKeys]
    const [removed] = reorderedVisible.splice(oldIndex, 1)
    reorderedVisible.splice(newIndex, 0, removed)

    const fullOrder = orderedSessions.map(sessionKey)
    const visibleSet = new Set(reorderedVisible)
    let visibleIndex = 0
    const nextOrder = fullOrder.map(key => {
      if (!visibleSet.has(key)) return key
      const next = reorderedVisible[visibleIndex]
      visibleIndex += 1
      return next
    })

    setManualOrder(nextOrder)
    setDraggingKey(null)
    setDropTargetKey(null)
  }

  const groupedVisibleSessions = useMemo(() => {
    if (!hasMultipleHosts) return []
    const groups: Array<{ label: string; sessions: Session[] }> = []
    for (const session of visibleSessions) {
      const label = session.host_name || 'Local'
      const existing = groups.find(group => group.label === label)
      if (existing) existing.sessions.push(session)
      else groups.push({ label, sessions: [session] })
    }
    return groups
  }, [hasMultipleHosts, visibleSessions])

  const renderSessionItem = (session: Session, isHiddenSection = false) => {
    const sk = sessionKey(session)
    const isSelected = selectedSession === sk
    const needsAttention = sessionNeedsAttention(sk)
    const events = getSessionEvents(sk)
    const act = getSessionActivity(sk)
    const active = isSessionActive(session)
    const isRenaming = renamingSession?.key === sk
    const isOffline = session.host && session.host_online === false
    const promptPreview = session.prompt_preview?.trim()
    const projectName = pathLeaf(session.project_path)
    const agentType = session.agent_type || events[0]?.tool

    return (
      <li key={sk}>
        <div
          role="button"
          tabIndex={0}
          draggable={!collapsed && !isRenaming}
          onDragStart={() => setDraggingKey(sk)}
          onDragEnd={() => {
            setDraggingKey(null)
            setDropTargetKey(null)
          }}
          onDragOver={(event) => {
            event.preventDefault()
            if (draggingKey && draggingKey !== sk) setDropTargetKey(sk)
          }}
          onDrop={(event) => {
            event.preventDefault()
            handleDrop(sk)
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
            setContextMenu({ key: sk, name: session.name, host: session.host, x: e.clientX, y: e.clientY })
          }}
          className={cn(
            'flex flex-col w-full p-3 rounded transition-all duration-200 text-sidebar-foreground',
            'hover:bg-sidebar-accent',
            isSelected && 'bg-sidebar-accent text-sidebar-primary border-l-2 border-primary',
            needsAttention && !isSelected && 'border-l-2 border-warning bg-warning/5',
            !isSelected && !needsAttention && 'border-l-2 border-transparent',
            (isHiddenSection || isOffline) && 'opacity-60',
            isRenaming && 'cursor-default',
            draggingKey === sk && 'opacity-75 cursor-grabbing',
            dropTargetKey === sk && 'ring-1 ring-primary/60',
          )}
        >
          <div className="flex items-center gap-2 w-full">
            {!collapsed && <AgentMark agentType={agentType} className="h-5 min-w-8 px-1.5 shrink-0" />}
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
                className="flex-1 text-sm text-foreground bg-input border border-primary rounded px-1 py-0.5 outline-none font-mono"
              />
            ) : (
              <span className={cn(
                'tracking-wide flex-1 overflow-hidden text-ellipsis whitespace-nowrap text-left',
                isSelected && 'text-primary',
              )}>
                {collapsed ? session.name.charAt(0).toUpperCase() : session.name}
              </span>
            )}
            {!collapsed && session.agent_session_id && (
              <span className="shrink-0 rounded border border-border px-1.5 py-0.5 text-[10px] text-muted-foreground font-mono" title={session.agent_session_id}>
                {shortSessionId(session.agent_session_id)}
              </span>
            )}
            {!collapsed && (
              <span className="shrink-0 rounded border border-border px-1.5 py-0.5 text-[10px] text-muted-foreground" title={`Uptime: ${formatUptime(session.created)}`}>
                {formatUptime(session.created)}
              </span>
            )}
            {!collapsed && session.attached && (
              <span className="w-2 h-2 rounded-full bg-success shrink-0" title="attached" />
            )}
            {!collapsed && active && (
              <span className="text-[10px] text-success shrink-0">active</span>
            )}
          </div>

          {!collapsed && (projectName || promptPreview) && (
            <div className="mt-1 flex items-center gap-2 text-[11px] text-muted-foreground">
              {projectName && (
                <span className="shrink-0 rounded border border-border px-1.5 py-0.5" title={session.project_path}>
                  {projectName}
                </span>
              )}
              {promptPreview && (
                <span className="min-w-0 truncate italic" title={promptPreview}>
                  "{promptPreview}"
                </span>
              )}
            </div>
          )}

          {!collapsed && prefs.sparklines_visible && act?.sparkline && (
            <div className={cn('mt-1.5 w-full', events.filter(e => e.status === 'waiting' || e.status === 'error').length > 0 && 'mb-1')}>
              <Sparkline data={act.sparkline} height={14} />
            </div>
          )}

          {!collapsed && events.filter(e => e.status === 'waiting' || e.status === 'error').length > 0 && (
            <div className="flex gap-1 flex-wrap mt-1">
              {events
                .filter(e => e.status === 'waiting' || e.status === 'error')
                .map((evt, i) => (
                  <ToolBadge key={`${evt.tool}-${evt.pane}-${i}`} event={evt} />
                ))}
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

  return (
    <aside className={cn(
      'flex flex-col h-full bg-sidebar transition-all duration-300 font-mono text-sm font-bold',
      collapsed
        ? collapseMode === 'hidden' ? 'w-0 overflow-hidden' : 'w-16'
        : 'w-72',
      !isHidden && 'border-r border-sidebar-border',
    )}>
      {!collapsed && (
        <div className="px-2 pt-2" ref={filterRef}>
          <button
            type="button"
            onClick={() => setFilterOpen(value => !value)}
            className="w-full rounded border border-sidebar-border bg-sidebar-accent px-3 py-2 text-left text-[11px] text-muted-foreground hover:text-sidebar-foreground"
          >
            {filterLabel}
          </button>
          {filterOpen && (
            <div className="mt-1 rounded border border-sidebar-border bg-sidebar p-2 shadow-lg">
              <label className="flex items-center gap-2 px-1 py-1 text-[11px] text-sidebar-foreground">
                <input
                  type="checkbox"
                  checked={projectFilters.length === 0}
                  onChange={() => setProjectFilters([])}
                />
                All projects
              </label>
              <div className="max-h-48 overflow-y-auto">
                {projects.map(project => (
                  <label key={project} className="flex items-center gap-2 px-1 py-1 text-[11px] text-sidebar-foreground" title={project}>
                    <input
                      type="checkbox"
                      checked={projectFilters.includes(project)}
                      onChange={() => {
                        setProjectFilters(current => current.includes(project)
                          ? current.filter(value => value !== project)
                          : [...current, project])
                      }}
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
        <ul className="space-y-1">
          {visibleSessions.length === 0 && (
            <li className="p-3 text-muted-foreground text-sm">
              {collapsed ? '—' : 'No sessions'}
            </li>
          )}

          {hasMultipleHosts ? (
            groupedVisibleSessions.map(group => (
              <li key={group.label}>
                {!collapsed && (
                  <div className="px-3 pt-2 pb-1 text-[10px] uppercase tracking-wider text-muted-foreground font-semibold flex items-center gap-1.5">
                    <span className={cn(
                      'w-1.5 h-1.5 rounded-full',
                      group.sessions[0]?.host_online !== false ? 'bg-success' : 'bg-muted-foreground',
                    )} />
                    {group.label}
                  </div>
                )}
                <ul className="space-y-0.5">
                  {group.sessions.map(session => renderSessionItem(session))}
                </ul>
              </li>
            ))
          ) : (
            visibleSessions.map(session => renderSessionItem(session))
          )}

          {hiddenSessions.length > 0 && !collapsed && (
            <>
              <li
                onClick={() => setHiddenExpanded(!hiddenExpanded)}
                className="px-3 mt-2 text-[11px] text-muted-foreground cursor-pointer select-none flex items-center gap-1"
              >
                <span
                  className="inline-block text-[10px] transition-transform duration-150"
                  style={{ transform: hiddenExpanded ? 'rotate(90deg)' : 'rotate(0deg)' }}
                >
                  ▶
                </span>
                Hidden ({hiddenSessions.length})
              </li>
              {hiddenExpanded && hiddenSessions.map(session => renderSessionItem(session, true))}
            </>
          )}
        </ul>
      </nav>

      {contextMenu && (
        <div
          className="fixed bg-popover border border-border rounded-md py-1 z-[1000] shadow-lg min-w-[140px]"
          style={{ left: contextMenu.x, top: contextMenu.y }}
          onClick={(e) => e.stopPropagation()}
        >
          <div
            onClick={() => canRenameContextTarget && startRename({
              key: contextMenu.key,
              name: contextMenu.name,
              host: contextMenu.host,
            })}
            className={cn(
              'px-3 py-1.5 text-sm text-popover-foreground hover:bg-accent hover:text-accent-foreground',
              canRenameContextTarget ? 'cursor-pointer' : 'cursor-not-allowed opacity-50',
            )}
          >
            Rename
          </div>
          <div
            onClick={() => toggleHide(contextMenu.key)}
            className="px-3 py-1.5 text-sm text-popover-foreground cursor-pointer hover:bg-accent hover:text-accent-foreground"
          >
            {hiddenSet.has(contextMenu.key) ? 'Unhide' : 'Hide'}
          </div>
        </div>
      )}
    </aside>
  )
}
