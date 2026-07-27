import { useState, useEffect, useCallback, useRef } from 'react'

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

export function useSessions() {
  const [sessions, setSessions] = useState<Session[]>([])
  const [loading, setLoading] = useState(true)
  // Names inserted optimistically before the server confirms them, mapped to
  // their insertion timestamp. Tracked so a real /api/sessions refresh does
  // not flicker the stub out before the daemon appears in discovery. Cleared
  // once the server's list contains the name, on explicit removeSession, or
  // after a TTL safety net in case the create silently failed and no
  // session-removed broadcast ever arrives.
  const STUB_TTL_MS = 6000
  const pendingOptimistic = useRef<Map<string, number>>(new Map())

  const refresh = useCallback(async () => {
    try {
      const res = await fetch('/api/sessions')
      if (res.ok) {
        const data = (await res.json()) as Session[] || []
        const confirmed = new Set(data.map(s => s.name))
        const now = performance.now()
        // Drop optimistic stubs that the server confirmed (real record
        // replaces them) or that have outlived their TTL (create failed
        // without a removal broadcast). Surviving pending stubs within TTL
        // are preserved so the cold-start gap does not flicker them out.
        for (const name of [...pendingOptimistic.current.keys()]) {
          const insertedAt = pendingOptimistic.current.get(name)!
          if (confirmed.has(name) || now - insertedAt > STUB_TTL_MS) {
            pendingOptimistic.current.delete(name)
          }
        }
        setSessions(prev => {
          if (pendingOptimistic.current.size === 0) return data
          const kept = prev.filter(s => pendingOptimistic.current.has(s.name))
          return kept.length ? [...kept, ...data] : data
        })
      }
    } catch (err) {
      console.error('Failed to fetch sessions:', err)
    } finally {
      setLoading(false)
    }
  }, [])

  // Insert (or replace) an optimistic session stub by name so the sidebar and
  // terminal view render instantly while the backend daemon cold-starts. The
  // next successful refresh reconciles it with the real server record.
  const upsertSession = useCallback((session: Session) => {
    pendingOptimistic.current.set(session.name, performance.now())
    setSessions(prev => {
      const idx = prev.findIndex(s => s.name === session.name)
      if (idx === -1) return [session, ...prev]
      const copy = prev.slice()
      copy[idx] = session
      return copy
    })
  }, [])

  // Remove an optimistic stub (e.g. create failed, or the server just
  // confirmed the session is gone via session-removed). In multi-host setups
  // drop by sessionKey (host/name) so a same-named session on another host
  // is not nuked. Leaves any real server record untouched.
  const removeSession = useCallback((name: string, host?: string) => {
    pendingOptimistic.current.delete(name)
    setSessions(prev => prev.filter(s =>
      host ? !(s.name === name && (s.host || '') === host) : s.name !== name
    ))
  }, [])

  useEffect(() => {
    refresh()
    const interval = setInterval(refresh, 5000)
    return () => clearInterval(interval)
  }, [refresh])

  return { sessions, loading, refresh, upsertSession, removeSession }
}
