import { useCallback, useEffect, useRef, useState } from 'react'
import type { WorkspaceAction } from '../state/workspaceReducer'
import type { PaneTree } from '../lib/paneTree'

export type GroupRecord = {
  tree: PaneTree
  name?: string
  rank?: string
  deleted_at?: string | null
}

export type GroupRecordMap = Record<string, GroupRecord>

function toGroups(body: unknown): GroupRecordMap {
  return body && typeof body === 'object' ? (body as GroupRecordMap) : {}
}

export function useGroupSync(
  authenticated: boolean,
  dispatch: React.Dispatch<WorkspaceAction>,
) {
  const abortRef = useRef<AbortController | null>(null)
  const [namingGroupId, setNamingGroupId] = useState<string | null>(null)

  const refresh = useCallback(async () => {
    if (!authenticated) return
    abortRef.current?.abort()
    const controller = new AbortController()
    abortRef.current = controller
    try {
      const res = await fetch('/api/groups', { signal: controller.signal })
      if (!res.ok) return
      const body = await res.json()
      if (controller.signal.aborted) return
      dispatch({ type: 'groups/snapshot', groups: toGroups(body) })
    } catch {
      // Ignore network errors; groups will be refreshed on reconnect.
    }
  }, [authenticated, dispatch])

  useEffect(() => {
    if (!authenticated) return
    refresh()
  }, [authenticated, refresh])

  const mutate = useCallback(async (body: Record<string, unknown>) => {
    try {
      const res = await fetch('/api/groups', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      })
      if (!res.ok) return
      const next = await res.json()
      dispatch({ type: 'groups/snapshot', groups: toGroups(next) })
    } catch {
      refresh()
    }
  }, [dispatch, refresh])

  const setTree = useCallback((id: string, tree: PaneTree) => mutate({ id, op: 'tree', tree }), [mutate])
  const setName = useCallback((id: string, name: string) => mutate({ id, op: 'name', name, mode: name.trim() === '' ? 'auto' : 'manual' }), [mutate])
  const setRank = useCallback((id: string, rank: string) => mutate({ id, op: 'rank', rank }), [mutate])
  const deleteGroup = useCallback((id: string) => mutate({ id, op: 'delete' }), [mutate])

  const forceAiName = useCallback(async (id: string): Promise<boolean> => {
    setNamingGroupId(id)
    try {
      const res = await fetch('/api/groups', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ id, op: 'ai-name' }),
      })
      if (!res.ok) {
        // A legacy server that does not recognise the ai-name op is not a
        // hard failure; the caller can fall back to the stateless endpoint.
        if (res.status === 400) {
          const text = await res.text().catch(() => '')
          if (/invalid op|unknown op/i.test(text)) return false
        }
        throw new Error(`Failed to force AI name: ${res.status}`)
      }
      const body = await res.json()
      dispatch({ type: 'groups/snapshot', groups: toGroups(body) })
      return true
    } finally {
      setNamingGroupId(null)
    }
  }, [dispatch])

  return { refresh, setTree, setName, setRank, deleteGroup, forceAiName, namingGroupId }
}
