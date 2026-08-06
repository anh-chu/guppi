import { useState, useEffect, useCallback } from 'react'

export interface Schedule {
  id: string
  name: string
  cronSpec: string
  command: string
  path: string
  agentType: string
  host: string
  sessionNamePrefix?: string
  worktreeBranch?: string
  maxConcurrency: number
  enabled: boolean
  lastRun?: string
  nextRun?: string
  runCount: number
  createdAt?: string
}

export interface ScheduleForm {
  name: string
  cronSpec: string
  command: string
  path: string
  agentType: string
  host: string
  worktreeBranch: string
  maxConcurrency: number
  enabled: boolean
}

type ScheduleWire = Record<string, unknown>

function text(value: unknown): string {
  return value === null || value === undefined ? '' : String(value)
}

function bool(value: unknown): boolean {
  return typeof value === 'boolean' ? value : Boolean(value)
}

function num(value: unknown): number {
  if (typeof value === 'number' && Number.isFinite(value)) return value
  if (typeof value === 'string' && value.trim() !== '') {
    const parsed = Number(value)
    return Number.isFinite(parsed) ? parsed : 0
  }
  return 0
}

function normalizeSchedule(raw: ScheduleWire): Schedule {
  return {
    id: text(raw.id ?? raw.ID),
    name: text(raw.name ?? raw.Name),
    cronSpec: text(raw.cronSpec ?? raw.cron_spec ?? raw.CronSpec),
    command: text(raw.command ?? raw.Command),
    path: text(raw.path ?? raw.Path),
    agentType: text(raw.agentType ?? raw.agent_type ?? raw.AgentType),
    // Wire key is target_owner (pkg/scheduler.Job.TargetOwner); it carries a
    // canonical catalog OwnerID or, on a legacy-only node, a raw peer fingerprint.
    // `host` is accepted too for backward compatibility with any
    // still-cached response shape, but the server no longer emits it.
    host: text(raw.target_owner ?? raw.TargetOwner ?? raw.host ?? raw.Host),
    sessionNamePrefix: text(raw.sessionNamePrefix ?? raw.session_name_prefix ?? raw.SessionNamePrefix) || undefined,
    worktreeBranch: text(raw.worktreeBranch ?? raw.worktree_branch ?? raw.WorktreeBranch) || undefined,
    maxConcurrency: num(raw.maxConcurrency ?? raw.max_concurrency ?? raw.MaxConcurrency),
    enabled: bool(raw.enabled ?? raw.Enabled),
    lastRun: text(raw.lastRun ?? raw.last_run ?? raw.LastRun) || undefined,
    nextRun: text(raw.nextRun ?? raw.next_run ?? raw.NextRun) || undefined,
    runCount: num(raw.runCount ?? raw.run_count ?? raw.RunCount),
    createdAt: text(raw.createdAt ?? raw.created_at ?? raw.CreatedAt) || undefined,
  }
}

function normalizeSchedules(value: unknown): Schedule[] {
  if (!Array.isArray(value)) return []
  return value
    .filter((item): item is ScheduleWire => !!item && typeof item === 'object')
    .map(normalizeSchedule)
}

function toWire(form: ScheduleForm) {
  return {
    name: form.name,
    cron_spec: form.cronSpec,
    command: form.command,
    path: form.path,
    agent_type: form.agentType,
    target_owner: form.host,
    worktree_branch: form.worktreeBranch,
    max_concurrency: form.maxConcurrency,
    enabled: form.enabled,
  }
}

async function readError(res: Response): Promise<string> {
  try {
    const text = await res.text()
    return text || `Request failed with status ${res.status}`
  } catch {
    return `Request failed with status ${res.status}`
  }
}

export function useSchedules() {
  const [schedules, setSchedules] = useState<Schedule[]>([])
  const [loading, setLoading] = useState(true)

  const refresh = useCallback(async () => {
    try {
      const res = await fetch('/api/schedules')
      if (res.ok) {
        const data = await res.json()
        setSchedules(normalizeSchedules(data))
      }
    } catch {
      // silent
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    refresh()
    const timer = window.setInterval(refresh, 15_000)
    return () => window.clearInterval(timer)
  }, [refresh])

  const create = useCallback(async (form: ScheduleForm): Promise<string | null> => {
    try {
      const res = await fetch('/api/schedules', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(toWire(form)),
      })
      if (!res.ok) return await readError(res)
      const data = await res.json().catch(() => null)
      if (Array.isArray(data)) {
        setSchedules(normalizeSchedules(data))
      } else if (data && typeof data === 'object') {
        const next = normalizeSchedule(data as ScheduleWire)
        setSchedules(prev => {
          const idx = prev.findIndex(item => item.id === next.id)
          if (idx === -1) return [next, ...prev]
          const copy = [...prev]
          copy[idx] = next
          return copy
        })
      } else {
        await refresh()
      }
      return null
    } catch (e) {
      return String(e)
    }
  }, [refresh])

  const update = useCallback(async (id: string, form: ScheduleForm): Promise<string | null> => {
    try {
      const res = await fetch(`/api/schedules/${encodeURIComponent(id)}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(toWire(form)),
      })
      if (!res.ok) return await readError(res)
      const data = await res.json().catch(() => null)
      if (Array.isArray(data)) {
        setSchedules(normalizeSchedules(data))
      } else if (data && typeof data === 'object') {
        const next = normalizeSchedule(data as ScheduleWire)
        setSchedules(prev => prev.map(item => item.id === id ? next : item))
      } else {
        await refresh()
      }
      return null
    } catch (e) {
      return String(e)
    }
  }, [refresh])

  const remove = useCallback(async (id: string): Promise<string | null> => {
    try {
      const res = await fetch(`/api/schedules/${encodeURIComponent(id)}`, { method: 'DELETE' })
      if (!res.ok) return await readError(res)
      setSchedules(prev => prev.filter(item => item.id !== id))
      return null
    } catch (e) {
      return String(e)
    }
  }, [])

  const runNow = useCallback(async (id: string): Promise<string | null> => {
    try {
      const res = await fetch(`/api/schedules/${encodeURIComponent(id)}/run`, { method: 'POST' })
      if (!res.ok) return await readError(res)
      const data = await res.json().catch(() => null)
      if (Array.isArray(data)) {
        setSchedules(normalizeSchedules(data))
      } else if (data && typeof data === 'object') {
        const next = normalizeSchedule(data as ScheduleWire)
        setSchedules(prev => prev.map(item => item.id === id ? next : item))
      } else {
        await refresh()
      }
      return null
    } catch (e) {
      return String(e)
    }
  }, [refresh])

  return { schedules, loading, refresh, create, update, remove, runNow }
}
