import { useCallback, useEffect, useRef } from 'react'
import type { WorkspaceAction } from '../state/workspaceReducer'
import type { PaneTree } from '../lib/paneTree'
import { getLeaves } from '../lib/paneTree'

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
  // Per-id counter: incremented when POST starts, decremented when it finishes.
  // id is in-flight while count > 0. Used to skip tree adoption during pending POSTs.
  const inFlightCounterRef = useRef<Map<string, number>>(new Map())

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
      const inFlightIds = Array.from(inFlightCounterRef.current.entries())
        .filter(([, count]) => count > 0)
        .map(([id]) => id)
      dispatch({
        type: 'groups/snapshot',
        groups: toGroups(body),
        skipTreeAdoptFor: inFlightIds,
      })
    } catch {
      // Ignore network errors; groups will be refreshed on reconnect.
    }
  }, [authenticated, dispatch])

  useEffect(() => {
    if (!authenticated) return
    refresh()
  }, [authenticated, refresh])

  const mutate = useCallback(async (body: Record<string, unknown>): Promise<boolean> => {
    try {
      const res = await fetch('/api/groups', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      })
      if (!res.ok) return false
      const next = await res.json()
      const inFlightIds = Array.from(inFlightCounterRef.current.entries())
        .filter(([, count]) => count > 0)
        .map(([id]) => id)
      dispatch({
        type: 'groups/snapshot',
        groups: toGroups(next),
        skipTreeAdoptFor: inFlightIds,
      })
      return true
    } catch {
      refresh()
      return false
    }
  }, [dispatch, refresh])

  const setTree = useCallback(
    (id: string, tree: PaneTree, rev?: number) => {
      // Never POST one-leaf trees; server tombstones them and rejects follow-up writes.
      // Tree becomes persistent only once it has 2+ leaves. The effect in App.tsx
      // will push again after tree grows.
      const leaves = getLeaves(tree)
      if (leaves.length < 2) return

      // Increment in-flight counter for this id
      const count = inFlightCounterRef.current.get(id) ?? 0
      inFlightCounterRef.current.set(id, count + 1)

      void mutate({ id, op: 'tree', tree }).then((success) => {
        // Dispatch treeSaved only on successful POST to unblock tree adoption.
        if (success && rev !== undefined) {
          dispatch({ type: 'groups/treeSaved', id, rev })
        } else if (!success) {
          // On failure, let server state win by refreshing.
          refresh()
        }
      }).finally(() => {
        // Decrement in-flight counter
        const count = inFlightCounterRef.current.get(id) ?? 0
        if (count > 1) {
          inFlightCounterRef.current.set(id, count - 1)
        } else {
          inFlightCounterRef.current.delete(id)
        }
      })
    },
    [mutate, dispatch, refresh],
  )
  const setName = useCallback((id: string, name: string) => mutate({ id, op: 'name', name, mode: name.trim() === '' ? 'auto' : 'manual' }), [mutate])
  const setRank = useCallback((id: string, rank: string) => mutate({ id, op: 'rank', rank }), [mutate])
  const deleteGroup = useCallback((id: string) => mutate({ id, op: 'delete' }), [mutate])

  // Mark a group id as pending so groups/snapshot dispatches include it in
  // skipTreeAdoptFor while its first tree POST has not been sent yet (e.g. a
  // new group whose session-create POST must resolve before the tree push).
  const markGroupPending = useCallback((id: string) => {
    const count = inFlightCounterRef.current.get(id) ?? 0
    inFlightCounterRef.current.set(id, count + 1)
  }, [])
  const clearGroupPending = useCallback((id: string) => {
    const count = inFlightCounterRef.current.get(id) ?? 0
    if (count > 1) inFlightCounterRef.current.set(id, count - 1)
    else inFlightCounterRef.current.delete(id)
  }, [])

  return { refresh, setTree, setName, setRank, deleteGroup, markGroupPending, clearGroupPending }
}
