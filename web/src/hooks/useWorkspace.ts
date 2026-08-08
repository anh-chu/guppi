import { useCallback, useEffect, useMemo, useReducer, useRef } from 'react'
import {
  workspaceReducer,
  createInitialWorkspaceState,
  type WorkspaceStateWithStreaks,
  type WorkspaceView,
  type WikiTarget,
  type View,
  parseSessionKey,
} from '../state/workspaceReducer'
import { useSessions } from './useSessions'
import { findLeaf } from '../lib/paneTree'
import { useGroupSync } from './useGroupSync'
import type { Session } from './useSessions'
import type { PaneTree } from '../lib/paneTree'

const WORKSPACE_KEY = 'termyard:workspace'
const WORKSPACE_VERSION = 1

function getUrlSessionKey(): string | null {
  if (typeof window === 'undefined') return null
  const hostMatch = window.location.pathname.match(/^\/session\/([^/]+)\/(.+)$/)
  if (hostMatch) {
    const host = decodeURIComponent(hostMatch[1])
    const name = decodeURIComponent(hostMatch[2])
    return `${host}/${name}`
  }
  const match = window.location.pathname.match(/^\/session\/(.+)$/)
  if (match) {
    return decodeURIComponent(match[1])
  }
  return null
}

function loadInitialView(urlKey: string | null, activeGroupId: string): Partial<WorkspaceView> {
  if (typeof window === 'undefined') return { activeGroupId }
  const settingsOpen = window.location.pathname === '/settings'
  if (settingsOpen) {
    return { currentView: 'overview', settingsOpen, activeGroupId }
  }
  if (!urlKey) {
    return { currentView: 'overview', activeGroupId }
  }
  try {
    const stored = window.localStorage.getItem('termyard:pane-tree')
    const storedActiveKey = window.localStorage.getItem('termyard:active-key')
    if (stored) {
      const tree = JSON.parse(stored) as { type: string; sessionKey?: string }
      if (tree.type && (tree.type === 'leaf' ? tree.sessionKey === urlKey : findLeaf(tree as any, urlKey))) {
        return {
          currentView: 'session',
          settingsOpen: false,
          paneTree: tree as any,
          activeKey: storedActiveKey || urlKey,
          singleView: null,
          activeGroupId,
        }
      }
    }
  } catch {}
  return {
    currentView: 'session',
    settingsOpen: false,
    paneTree: null,
    activeKey: null,
    singleView: urlKey,
    activeGroupId,
  }
}

function initState(): WorkspaceStateWithStreaks {
  let activeGroupId = ''
  try {
    activeGroupId = window.localStorage.getItem('termyard:active-group-id') || ''
  } catch {}
  if (!activeGroupId) {
    activeGroupId = Math.random().toString(36).slice(2)
    // Persist synchronously, before any render or effect runs. Without this,
    // a second tab opened moments later (fresh profile, storage cleared,
    // session-restore reopening several tabs at once) would also see an
    // empty key here and mint its OWN random id, then both tabs seed a group
    // from the same current session layout under two different ids -- a
    // duplicate group with identical sessions. Writing it back immediately
    // shrinks that race window from "until the next effect flush" to
    // "this synchronous statement".
    try {
      window.localStorage.setItem('termyard:active-group-id', activeGroupId)
    } catch {}
  }
  const urlKey = getUrlSessionKey()
  const view = loadInitialView(urlKey, activeGroupId)
  let wikiPersisted: { target?: WikiTarget | null; history?: WikiTarget[] } | null = null
  try {
    const raw = window.localStorage.getItem(WORKSPACE_KEY)
    if (raw) {
      const parsed = JSON.parse(raw) as { v?: number; wiki?: { target?: WikiTarget | null; history?: WikiTarget[] } }
      if (parsed.v === WORKSPACE_VERSION && parsed.wiki) wikiPersisted = parsed.wiki
    }
  } catch {}
  return {
    ...createInitialWorkspaceState({ view }),
    wiki: {
      target: wikiPersisted?.target ?? null,
      history: wikiPersisted?.history ?? [],
    },
  }
}

export function useWorkspace(authenticated: boolean) {
  const [state, dispatch] = useReducer(workspaceReducer, undefined, initState)
  const stateRef = useRef(state)
  stateRef.current = state

  const connection = state.connection

  const setConnection = useCallback((live: boolean) => {
    const wasLive = stateRef.current.connection.live
    // A fresh connect must be considered liveness-unknown until a non-empty
    // snapshot confirms the server state; an empty snapshot during restart is
    // legitimate and must not prune live groups.
    const livenessUnknown = live ? (wasLive ? stateRef.current.connection.livenessUnknown : true) : true
    dispatch({ type: 'connection', live, livenessUnknown })
  }, [dispatch])

  const { refresh } = useSessions(dispatch, connection, authenticated)
  const groupSync = useGroupSync(authenticated, dispatch)

  const makeKey = (name: string, host?: string) => (host ? `${host}/${name}` : name)

  const onEvent = useCallback((evt: any) => {
    const type = evt?.type
    if (type === 'session-removed') {
      const name = evt.session || ''
      const host = evt.host || ''
      dispatch({ type: 'sessions/remove', key: makeKey(name, host) })
      refresh()
      return true
    }
    if (type === 'session-renamed') {
      const oldName = evt.session || ''
      const newName = evt.data?.new_name || ''
      const host = evt.host || ''
      if (oldName && newName) {
        dispatch({ type: 'rename', oldKey: makeKey(oldName, host), newKey: makeKey(newName, host) })
      }
      refresh()
      return true
    }
    if (type === 'session-added' || type === 'sessions-changed') {
      refresh()
      return true
    }
    return false
  }, [dispatch, refresh])

  const actions = useMemo(
    () => ({
      setConnection,
      onEvent,
      refresh,
      dispatch,
      selectSession: (key: string) => dispatch({ type: 'view/select', key }),
      navigate: (view?: View, sessionKey?: string | null) =>
        dispatch({ type: 'view/navigate', view, sessionKey }),
      setCurrentView: (view: View) => dispatch({ type: 'view/setCurrentView', view }),
      openSettings: (open: boolean) => dispatch({ type: 'view/openSettings', open }),
      closePane: (sessionKey: string) => dispatch({ type: 'view/close', sessionKey }),
      removeFromLayout: (sessionKey: string) => dispatch({ type: 'view/removeFromLayout', sessionKey }),
      splitPane: (targetKey: string, direction: 'h' | 'v', newKey: string, newFirst?: boolean) =>
        dispatch({ type: 'view/split', targetKey, direction, newKey, newFirst }),
      movePane: (sourceKey: string, targetKey: string, edge: 'left' | 'right' | 'top' | 'bottom') =>
        dispatch({ type: 'view/move', sourceKey, targetKey, edge }),
      swapPanes: (a: string, b: string) => dispatch({ type: 'view/swap', a, b }),
      setPaneTree: (tree: PaneTree | null) => dispatch({ type: 'view/setPaneTree', tree }),
      setActiveGroup: (groupId: string, tree?: PaneTree | null, focusKey?: string | null) =>
        dispatch({ type: 'view/setActiveGroup', groupId, tree, focusKey }),
      setActiveKey: (key: string | null) => dispatch({ type: 'view/setActiveKey', key }),
      setSingleView: (key: string | null) => dispatch({ type: 'view/setSingleView', sessionKey: key }),
      dissolveToSingle: () => dispatch({ type: 'view/dissolveToSingle' }),
      promoteNextGroup: () => dispatch({ type: 'view/promoteNextGroup' }),
      pruneMissing: (validKeys: string[], now: number) =>
        dispatch({ type: 'view/pruneMissing', validKeys, now }),
      openWiki: (target: WikiTarget) => dispatch({ type: 'wiki/open', target }),
      closeWiki: () => dispatch({ type: 'wiki/close' }),
      renameSession: (oldKey: string, newKey: string) => dispatch({ type: 'rename', oldKey, newKey }),
      addOptimistic: (session: Session, now?: number) => dispatch({ type: 'optimistic/add', session, now }),
      removeOptimistic: (name: string, host?: string) => dispatch({ type: 'optimistic/remove', name, host }),
      restore: (snapshot: Partial<WorkspaceStateWithStreaks>) => dispatch({ type: 'restore', snapshot }),
    }),
    [dispatch],
  )

  // Persist view and wiki history across reloads.
  useEffect(() => {
    try {
      localStorage.setItem('termyard:active-group-id', state.view.activeGroupId)
      if (state.view.paneTree) {
        localStorage.setItem('termyard:pane-tree', JSON.stringify(state.view.paneTree))
        localStorage.setItem('termyard:active-key', state.view.activeKey || '')
      } else {
        localStorage.removeItem('termyard:pane-tree')
        localStorage.removeItem('termyard:active-key')
      }
      localStorage.setItem(
        WORKSPACE_KEY,
        JSON.stringify({ v: WORKSPACE_VERSION, wiki: state.wiki }),
      )
    } catch {}
  }, [state.view.activeGroupId, state.view.paneTree, state.view.activeKey, state.wiki])

  return { state, actions, groupSync }
}

export { parseSessionKey }
