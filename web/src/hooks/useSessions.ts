import { useCallback, useEffect, useRef } from 'react'
import type { WorkspaceAction } from '../state/workspaceReducer'

export interface Pane {
  id: string
  window_id: string
  session_id: string
  index: number
  active: boolean
  width: number
  height: number
  current_command: string
  current_path?: string
  pid: number
}

export interface Window {
  id: string
  session_id: string
  name: string
  index: number
  active: boolean
  layout: string
  panes: Pane[]
}

export interface Session {
  id: string
  name: string
  host?: string        // peer fingerprint (empty = local)
  host_name?: string   // peer display name
  host_online?: boolean
  backend?: string      // "daemon" for session-daemon sessions
  windows: Window[]
  created: string
  attached: boolean
  last_activity: string
  project_path?: string
  is_worktree?: boolean
  worktree_parent?: string  // main worktree root path (linked worktrees only)
  agent_type?: string
  prompt_preview?: string
  agent_session_id?: string
  user_prompt?: string
  last_agent_message?: string
  display_name?: string   // AI-generated or user-set friendly label
  user_set_name?: boolean // user manually set display_name
  scheduleID?: string
  schedule_id?: string
}

// Label to show for a session: friendly display name if present, else session name.
export function sessionLabel(session: Session): string {
  return session.display_name && session.display_name.trim() !== ''
    ? session.display_name
    : session.name
}

// Unique key for a session across hosts
export function sessionKey(session: Session): string {
  return session.host ? `${session.host}/${session.name}` : session.name
}

export function sessionScheduleID(session: Session): string {
  return session.scheduleID || session.schedule_id || ''
}

// Effective cwd of a session: the active pane's path, else the project root.
export function sessionCwd(session: Session): string | undefined {
  return session.windows.flatMap(w => w.panes).find(p => p.active)?.current_path
    ?? session.project_path
}

// Parse a session key back into host + name
export function parseSessionKey(key: string): { host: string; name: string } {
  const idx = key.indexOf('/')
  if (idx === -1) return { host: '', name: key }
  return { host: key.substring(0, idx), name: key.substring(idx + 1) }
}

// Build an optimistic session stub for instant sidebar/terminal rendering
// while the backend daemon cold-starts. Fields are minimal but valid so the
// sidebar and pool identity checks do not crash before /api/sessions confirms.
export function optimisticSession(name: string, hostId?: string, hostName?: string, cwd = ''): Session {
  const now = new Date().toISOString()
  return {
    id: name,
    name,
    host: hostId || undefined,
    host_name: hostName,
    host_online: true,
    backend: 'daemon',
    created: now,
    attached: false,
    last_activity: now,
    project_path: cwd || undefined,
    windows: [{
      id: `daemon-${name}`,
      session_id: name,
      name: 'shell',
      index: 0,
      active: true,
      layout: 'tiled',
      panes: [{
        id: name + ':0.0',
        window_id: `daemon-${name}`,
        session_id: name,
        index: 0,
        active: true,
        width: 120,
        height: 40,
        current_command: '',
        pid: 0,
      }],
    }],
  }
}

export interface ConnectionState {
  live: boolean
  livenessUnknown: boolean
}

export function useSessions(
  dispatch: React.Dispatch<WorkspaceAction>,
  connection: ConnectionState,
  authenticated: boolean,
) {
  const generationRef = useRef(0)
  const abortRef = useRef<AbortController | null>(null)

  const refresh = useCallback(async () => {
    if (!authenticated) return
    generationRef.current += 1
    const generation = generationRef.current
    abortRef.current?.abort()
    const controller = new AbortController()
    abortRef.current = controller
    try {
      const res = await fetch('/api/sessions', { signal: controller.signal })
      if (!res.ok) return
      const data = (await res.json()) as Session[] || []
      if (controller.signal.aborted) return
      dispatch({
        type: 'sessions/snapshot',
        sessions: data,
        generation,
        now: performance.now(),
      })
    } catch {
      // Network errors are expected during disconnect; the connection state
      // tells us when to fall back to polling.
    }
  }, [authenticated, dispatch])

  // Live events are primary. Reconcile on initial load and whenever the
  // WebSocket reconnects. Use slow fallback polling only when liveness is
  // unknown, and pause it in hidden tabs.
  useEffect(() => {
    if (!authenticated) return
    refresh()
    if (connection.live) return

    const tick = () => {
      if (!document.hidden) refresh()
    }
    const id = window.setInterval(tick, 5000)
    return () => window.clearInterval(id)
  }, [authenticated, connection.live, refresh])

  // Refresh immediately when the tab becomes visible while liveness is unknown.
  useEffect(() => {
    if (!authenticated) return
    const onVisibility = () => {
      if (!document.hidden && !connection.live) refresh()
    }
    document.addEventListener('visibilitychange', onVisibility)
    return () => document.removeEventListener('visibilitychange', onVisibility)
  }, [authenticated, connection.live, refresh])

  return { refresh }
}
