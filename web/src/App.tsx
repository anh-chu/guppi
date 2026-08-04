import { useState, useEffect, useCallback, useRef, useMemo } from 'react'
import { tinykeys } from 'tinykeys'
import { Sidebar } from './components/Sidebar'
import { Terminal } from './components/Terminal'
import { Overview } from './components/Overview'
import { NewSessionModal } from './components/NewSessionModal'
import { PortForwardModal } from './components/PortForwardModal'
import { ScheduleModal } from './components/ScheduleModal'
import { TopBar } from './components/TopBar'
import { TiledView } from './components/TiledView'
import { WikiPanel } from './components/WikiPanel'
import { PaneTree, getLeaves, findLeaf, splitLeaf, insertBesideLeaf, removeLeaf, replaceLeaf, updateRatio, popOut, swapLeaves, movePane } from './lib/paneTree'
import { SettingsDrawer } from './components/SettingsDrawer'
import { HelpModal } from './components/HelpModal'
import { QuickSwitcher } from './components/QuickSwitcher'
import { Login } from './components/Login'
import { Setup } from './components/Setup'
import { Session, sessionKey, parseSessionKey, optimisticSession, sessionCwd } from './hooks/useSessions'
import { useWorkspace } from './hooks/useWorkspace'
import { useHosts } from './hooks/useHosts'
import { useToolEvents } from './hooks/useToolEvents'
import { useActivity } from './hooks/useActivity'
import { useNotifications } from './hooks/useNotifications'
import { useWebSocket } from './hooks/useWebSocket'
import { usePushNotifications } from './hooks/usePushNotifications'
import { usePreferencesProvider, usePreferences, PreferencesContext } from './hooks/usePreferences'
import { useAuth } from './hooks/useAuth'
import { useSessionAttrs } from './hooks/useSessionAttrs'
import { useSessionOrder } from './hooks/useSessionOrder'
import { useWikiController } from './hooks/useWikiController'
import { Toasts, Toast } from './components/Toasts'
import { RecoveryPanel } from './components/RecoveryPanel'
import { useCrashedSessions } from './hooks/useCrashedSessions'
import { useSelfUpdate, type UpdateStatus } from './hooks/useSelfUpdate'
import { applyTheme } from './theme'
import { sessionSignal } from './lib/sessionState'
import { generateKeyBetween } from 'fractional-indexing'
import { terminalPool, keyFor as poolKeyFor } from './lib/terminalPool'
import { keyToSessionRef, splitIdAtPath } from './state/v2/paneTreeAdapter'
import type { SessionRef } from './state/v2/types'
import { sessionRefToKey } from './state/v2/paneTreeAdapter'
import { selectSessionByRef } from './state/v2/projections'
import { useV2State } from './hooks/useV2State'
import { isV2StateEnabled } from './lib/featureFlags'

type View = 'overview' | 'session' | 'settings' | 'setup'


type LayoutGroup = {
  id: string
  leaves: string[]
  isActive: boolean
  activeKey: string | null
  name: string | undefined
}

function getViewFromPath(): { view: View; sessionKey: string | null } {
  if (window.location.pathname === '/settings') {
    return { view: 'settings', sessionKey: null }
  }
  if (window.location.pathname === '/setup') {
    return { view: 'setup', sessionKey: null }
  }
  // /session/<host>/<name> or /session/<name> (backward compat)
  const hostMatch = window.location.pathname.match(/^\/session\/([^/]+)\/(.+)$/)
  if (hostMatch) {
    const host = decodeURIComponent(hostMatch[1])
    const name = decodeURIComponent(hostMatch[2])
    return { view: 'session', sessionKey: `${host}/${name}` }
  }
  const match = window.location.pathname.match(/^\/session\/(.+)$/)
  if (match) {
    return { view: 'session', sessionKey: decodeURIComponent(match[1]) }
  }
  return { view: 'overview', sessionKey: null }
}

function AppLegacy({ onLogout, authenticated }: { onLogout?: () => void; authenticated: boolean }) {
  // Legacy v1 mode: workspace/groups/session ordering all via legacy hooks.
  // This component is used when v2State is disabled.
  const workspace = useWorkspace(authenticated)
  const { state: workspaceState, actions: workspaceActions, groupSync } = workspace
  const { sessions, loading: sessionsLoading, groups: syncedGroups, groupsLoaded, view } = workspaceState
  const { currentView, settingsOpen, paneTree, activeKey, singleView, activeGroupId } = view

  // Thin wrappers keep the rest of the file using the old variable names.
  const setPaneTree = useCallback((tree: PaneTree | null | ((prev: PaneTree | null) => PaneTree | null)) => {
    const next = typeof tree === 'function' ? tree(workspaceState.view.paneTree) : tree
    workspaceActions.setPaneTree(next)
  }, [workspaceActions, workspaceState.view.paneTree])
  const setActiveKey = useCallback((key: string | null) => workspaceActions.setActiveKey(key), [workspaceActions])
  const setSingleView = useCallback((key: string | null) => workspaceActions.setSingleView(key), [workspaceActions])
  const setCurrentView = useCallback((viewArg: View) => workspaceActions.setCurrentView(viewArg as any), [workspaceActions])
  const setSettingsOpen = useCallback((open: boolean) => workspaceActions.openSettings(open), [workspaceActions])
  const setActiveGroupId = useCallback((groupId: string) => workspaceActions.setActiveGroup(groupId), [workspaceActions])

  const refresh = workspaceActions.refresh
  const refreshGroups = groupSync.refresh
  const { setTree: setGroupTree, setName: setGroupName, setRank: setGroupRank, deleteGroup, forceAiName, namingGroupId } = groupSync

  const { events: allToolEvents, handleEvent: handleToolEvent, getSessionEvents, sessionNeedsAttention, isSessionInActiveTurn, dismissEvent, dismissAll: dismissAllEvents } = useToolEvents()
  const { getSessionActivity, handleActivityEvent } = useActivity()
  const { pushState, subscribe: pushSubscribe, unsubscribe: pushUnsubscribe } = usePushNotifications()
  const { processToolEvent } = useNotifications(pushState === 'subscribed')
  const { hosts, refresh: refreshHosts } = useHosts()
  const { ranks: sessionOrderRanks, loaded: sessionOrderLoaded, refresh: refreshSessionOrder, setRank: setSessionOrderRank } = useSessionOrder(authenticated)
  const migrationStartedRef = useRef(false)
  const selectedSession = singleView ?? activeKey
  const { prefs } = usePreferences()
  const wikiEnabled = !prefs.wiki_disabled
  // Wiki open/close/target nonce/history live in a small controller so the UI
  // components render and coordinate without owning state transitions.
  const wiki = useWikiController(workspace, wikiEnabled)
  // The tinykeys effect must not re-register on a preference change: it is
  // rebuilt wholesale and rebinding every shortcut to toggle one of them is
  // needless churn.
  const wikiEnabledRef = useRef(wikiEnabled)
  wikiEnabledRef.current = wikiEnabled
  // Turning it off closes the panel. Leaving it up would strand a surface the
  // user just said they did not want, with no menu entry left to dismiss it.
  useEffect(() => {
    if (!wikiEnabled) wiki.closePanel()
  }, [wikiEnabled, wiki.closePanel])

  const cwdForKey = useCallback((key: string) => {
    const s = sessions.find(x => sessionKey(x) === key)
    return s ? sessionCwd(s) : undefined
  }, [sessions])

  const activeGroup = syncedGroups[activeGroupId]
  const activeGroupName = activeGroup?.name ?? ''

  useEffect(() => {
    if (!authenticated || !groupsLoaded || !activeGroup || !paneTree || currentView !== 'session' || singleView) return
    const id = window.setTimeout(() => {
      if (JSON.stringify(activeGroup.tree) === JSON.stringify(paneTree)) return
      void setGroupTree(activeGroupId, paneTree)
    }, 250)
    return () => window.clearTimeout(id)
  }, [authenticated, groupsLoaded, activeGroup, activeGroupId, currentView, paneTree, singleView, setGroupTree])
  const hasMultipleHosts = hosts.length > 1
  const localHost = hosts.find(h => h.local)
  const localHostId = localHost?.id
  const localHostName = localHost?.name
  const [serverVersion, setServerVersion] = useState<string | null>(null)
  const [binaryUpdate, setBinaryUpdate] = useState<UpdateStatus | null>(null)
  const loadedVersionRef = useRef<string | null>(null)
  const updateAvailable = loadedVersionRef.current !== null && serverVersion !== null && serverVersion !== loadedVersionRef.current
  const selfUpdate = useSelfUpdate(binaryUpdate)
  const [newSessionModalOpen, setNewSessionModalOpen] = useState(false)
  const terminalContainerRef = useRef<HTMLDivElement>(null)
  const [sidebarCollapsed, setSidebarCollapsed] = useState(() => {
    try { return localStorage.getItem('termyard:sidebar-collapsed') === 'true' } catch { return false }
  })
  const [sidebarWidth, setSidebarWidth] = useState(() => {
    try {
      const v = parseInt(localStorage.getItem('termyard:sidebar-width') || '', 10)
      if (!Number.isNaN(v)) return Math.min(560, Math.max(200, v))
    } catch {}
    return 288
  })
  const handleSidebarWidth = useCallback((w: number) => {
    setSidebarWidth(w)
    try { localStorage.setItem('termyard:sidebar-width', String(w)) } catch {}
  }, [])
  const [terminalFullscreen, setTerminalFullscreen] = useState(false)
  const [helpOpen, setHelpOpen] = useState(false)
  const [quickSwitcherOpen, setQuickSwitcherOpen] = useState(false)
  const [portForwardsOpen, setPortForwardsOpen] = useState(false)
  const [schedulesOpen, setSchedulesOpen] = useState(false)
  const [mainDragOver, setMainDragOver] = useState<{ type: 'new-session' | 'sidebar'; zone: 'left' | 'right' | 'top' | 'bottom' | 'center' } | null>(null)
  const mainDragOverRef = useRef<{ type: 'new-session' | 'sidebar'; zone: 'left' | 'right' | 'top' | 'bottom' | 'center' } | null>(null)
  const pendingSessionRef = useRef<string | null>(null)
  const splitTargetRef = useRef<{ key: string; direction: 'h' | 'v'; newFirst?: boolean } | null>(null)
  const activeKeyRef = useRef(activeKey)
  activeKeyRef.current = activeKey
  // True while the server is rebuilding sessions after a crash.
  // Pruning of missing sessions is suspended until recovery finishes, so a
  // not-yet-rebuilt session is never mistaken for a deliberate kill.
  const [recovering, setRecovering] = useState(false)

  // Shared session attributes (background / hidden) — server-authoritative,
  // mirrored across the mesh. Viewport state (pane-tree, active-key,
  // active-group-id, sidebar-collapsed) stays per-device in localStorage.
  const { sets: sessionAttrs, setAttr: setSessionAttr, refresh: refreshSessionAttrs } = useSessionAttrs(authenticated)
  const crashedHook = useCrashedSessions()

  // Auto-lock: idle detection
  const lastActivityRef = useRef<number>(Date.now())
  useEffect(() => {
    if (!onLogout || !prefs.lock_timeout_minutes) return

    const idleMs = prefs.lock_timeout_minutes * 60 * 1000

    // Track user activity
    const onActivity = () => { lastActivityRef.current = Date.now() }
    const events = ['keydown', 'click', 'scroll', 'touchstart', 'mousemove'] as const
    const opts: AddEventListenerOptions = { passive: true, capture: true }
    events.forEach(e => document.addEventListener(e, onActivity, opts))

    // Check idle on an interval
    const checkInterval = setInterval(() => {
      const elapsed = Date.now() - lastActivityRef.current
      if (elapsed >= idleMs) {
        onLogout()
      }
    }, 30_000)

    return () => {
      events.forEach(e => document.removeEventListener(e, onActivity, opts as EventListenerOptions))
      clearInterval(checkInterval)
    }
  }, [onLogout, prefs.lock_timeout_minutes])

  // Persist sidebar state across reloads. Per-device — NOT synced.
  useEffect(() => {
    localStorage.setItem('termyard:sidebar-collapsed', String(sidebarCollapsed))
  }, [sidebarCollapsed])

  // Sync URL -> state on popstate (back/forward)
  useEffect(() => {
    const onPopState = () => {
      const { view, sessionKey: sk } = getViewFromPath()
      if (view === 'settings') {
        workspaceActions.openSettings(true)
      } else {
        workspaceActions.navigate(view as any, sk)
      }
    }
    window.addEventListener('popstate', onPopState)
    return () => window.removeEventListener('popstate', onPopState)
  }, [workspaceActions])

  // Navigate to a session or view (push history)
  // sessKey is either "name" (local) or "host/name" (remote)
  const navigateTo = useCallback((sessKey: string | null, view?: View) => {
    let path: string
    if (view === 'settings') {
      path = '/settings'
    } else if (view === 'setup') {
      path = '/setup'
    } else if (sessKey) {
      const { host, name } = parseSessionKey(sessKey)
      if (host) {
        path = `/session/${encodeURIComponent(host)}/${encodeURIComponent(name)}`
      } else {
        path = `/session/${encodeURIComponent(name)}`
      }
    } else {
      path = '/'
    }
    if (window.location.pathname !== path) {
      window.history.pushState(null, '', path)
    }
    workspaceActions.navigate(view as any, sessKey)
  }, [workspaceActions])

  // Remove a session leaf from every saved group EXCEPT the active one. Dropping
  // a session into the current view must not leave a duplicate leaf lingering in
  // its previous group: leaves are keyed by session name and the terminal pool
  // is keyed the same, so a cross-group duplicate mirrors one terminal into two
  // groups and the split appears to "land" in the wrong (old) group.
  const detachFromOtherGroups = useCallback((sessKey: string) => {
    const active = activeGroupIdRef.current
    for (const [id, g] of Object.entries(syncedGroupsRef.current)) {
      if (id === active || !findLeaf(g.tree, sessKey)) continue
      const pruned = removeLeaf(g.tree, sessKey)
      if (pruned === null) void deleteGroup(id)
      else void setGroupTree(id, pruned)
    }
  }, [deleteGroup, setGroupTree])

  const handleDropSession = useCallback((sessKey: string, targetKey: string, edge: 'left'|'right'|'top'|'bottom'|'center') => {
    // Dropping onto a standalone (singleView) session pairs the two into their
    // OWN new group. singleView leaves paneTree/activeGroupId pointing at the
    // previously-viewed group, so merging into the active tree would wrongly
    // dump the dragged session into that background group.
    if (singleView && (targetKey === singleView || targetKey === '')) {
      const anchor = singleView
      detachFromOtherGroups(sessKey)
      detachFromOtherGroups(anchor)
      const direction: 'h' | 'v' = (edge === 'top' || edge === 'bottom') ? 'v' : 'h'
      const newFirst = edge === 'left' || edge === 'top'
      const base = popOut(anchor)
      const newTree = newFirst
        ? insertBesideLeaf(base, anchor, direction, sessKey, true)
        : splitLeaf(base, anchor, direction, sessKey)
      const newGroupId = Math.random().toString(36).slice(2)
      const currentRank = syncedGroups[activeGroupId]?.rank ?? null
      if (paneTree) {
        void setGroupTree(activeGroupId, paneTree)
        if (!currentRank) void setGroupRank(activeGroupId, generateKeyBetween(null, generateKeyBetween(currentRank, null)))
      }
      void setGroupTree(newGroupId, newTree)
      void setGroupRank(newGroupId, generateKeyBetween(currentRank, null))
      setPaneTree(newTree)
      setActiveKey(sessKey)
      setActiveGroupId(newGroupId)
      setSingleView(null)
      return
    }
    setSingleView(null)
    detachFromOtherGroups(sessKey)
    const currentActive = activeKeyRef.current
    setPaneTree((prev: PaneTree | null) => {
      // Standalone session: target is the anchor, dragged session always second
      if (prev === null) {
        if (targetKey) {
          const direction: 'h' | 'v' = (edge === 'top' || edge === 'bottom') ? 'v' : 'h'
          const base = popOut(targetKey)
          return splitLeaf(base, targetKey, direction, sessKey)
        }
        return popOut(sessKey)
      }
      // Already in the layout — just focus, don't duplicate
      if (findLeaf(prev, sessKey)) { setActiveKey(sessKey); return prev }
      const key = (targetKey && findLeaf(prev, targetKey)) ? targetKey
        : currentActive !== null && findLeaf(prev, currentActive) ? currentActive
        : getLeaves(prev)[0] ?? null
      if (!key) return popOut(sessKey)
      const direction: 'h' | 'v' = (edge === 'top' || edge === 'bottom') ? 'v' : 'h'
      const newFirst = edge === 'left' || edge === 'top'
      return newFirst
        ? insertBesideLeaf(prev, key, direction, sessKey, true)
        : splitLeaf(prev, key, direction, sessKey)
    })
    setActiveKey(sessKey)
  }, [detachFromOtherGroups, singleView, paneTree, activeGroupId, syncedGroups, setGroupTree, setGroupRank])

  const closePane = useCallback((sessKey: string) => {
    workspaceActions.closePane(sessKey)
  }, [workspaceActions])

  // Synchronous removal for a deliberately killed session: drop its leaf from
  // the active tree, any background group, and singleView at once, so the pane
  // disappears immediately instead of on the next session refresh.
  const removeSessionFromLayout = useCallback((sessKey: string) => {
    workspaceActions.removeFromLayout(sessKey)
    // Explicit kill: dispose the pool entry so terminal/WS tear down.
    const { host, name } = parseSessionKey(sessKey)
    terminalPool.dispose(poolKeyFor(name, host || undefined))
  }, [workspaceActions])

  const popOutPane = useCallback((sessKey: string) => {
    setSingleView(null)
    setPaneTree(popOut(sessKey))
    setActiveKey(sessKey)
    const { host, name } = parseSessionKey(sessKey)
    const path = host
      ? `/session/${encodeURIComponent(host)}/${encodeURIComponent(name)}`
      : `/session/${encodeURIComponent(name)}`
    window.history.pushState(null, '', path)
    setCurrentView('session')
  }, [])

  const killPane = useCallback(async (sessKey: string) => {
    const session = sessionsRef.current.find(s => sessionKey(s) === sessKey)
    if (!session) return
    removeSessionFromLayout(sessKey)
    try {
      await fetch('/api/session/kill', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ id: session.id, name: session.name, host: session.host || undefined }),
      })
    } catch (err) {
      console.error('Failed to kill session:', err)
    }
  }, [removeSessionFromLayout])

  // Navigate back to overview when the tree becomes empty (but not if singleView is active)
  useEffect(() => {
    if (paneTree === null && !singleView && currentView === 'session') {
      // The active group just emptied (last session removed); delete its server
      // record before promoting the next group, so it does not linger.
      if (syncedGroupsRef.current[activeGroupId]) void deleteGroup(activeGroupId)
      const next = Object.entries(syncedGroups)
        .sort(([idA, a], [idB, b]) => {
          const aRank = a.rank ?? ''
          const bRank = b.rank ?? ''
          if (aRank !== bRank) {
            if (!aRank) return 1
            if (!bRank) return -1
            return aRank.localeCompare(bRank)
          }
          return idA.localeCompare(idB)
        })
        .find(([id]) => id !== activeGroupId)
      if (next) {
        const [nextId, nextGroup] = next
        const focusKey = getLeaves(nextGroup.tree)[0] ?? null
        workspaceActions.setActiveGroup(nextId, nextGroup.tree, focusKey)
        if (focusKey) {
          const { host, name } = parseSessionKey(focusKey)
          const path = host
            ? `/session/${encodeURIComponent(host)}/${encodeURIComponent(name)}`
            : `/session/${encodeURIComponent(name)}`
          if (window.location.pathname !== path) window.history.pushState(null, '', path)
        }
      } else {
        workspaceActions.navigate(undefined, null)
        if (window.location.pathname !== '/') window.history.pushState(null, '', '/')
      }
    }
  }, [paneTree, singleView, currentView, syncedGroups, activeGroupId, deleteGroup, workspaceActions])

  // Dissolve active group to standalone when only 1 session remains
  useEffect(() => {
    if (!paneTree) return
    const leaves = getLeaves(paneTree)
    if (leaves.length !== 1) return
    if (splitTargetRef.current) return // split pending — don't dissolve yet
    const lastLeaf = leaves[0]
    workspaceActions.dissolveToSingle()
    // The active layout is no longer a group; drop its server record so the
    // dissolved (broken-out) session stops re-rendering as grouped. Guard on a
    // real record to avoid tombstoning ids that were never persisted.
    if (syncedGroupsRef.current[activeGroupId]) void deleteGroup(activeGroupId)
    const { host, name } = parseSessionKey(lastLeaf)
    const path = host
      ? `/session/${encodeURIComponent(host)}/${encodeURIComponent(name)}`
      : `/session/${encodeURIComponent(name)}`
    if (window.location.pathname !== path) window.history.replaceState(null, '', path)
  }, [paneTree, activeGroupId, deleteGroup, workspaceActions])

  // Refs for latest values used in keyboard shortcuts (avoids effect churn)

  const sessionsRef = useRef(sessions)
  sessionsRef.current = sessions
  const selectedSessionRef = useRef(selectedSession)
  selectedSessionRef.current = selectedSession
  const syncedGroupsRef = useRef(syncedGroups)
  syncedGroupsRef.current = syncedGroups
  const activeGroupIdRef = useRef(activeGroupId)
  activeGroupIdRef.current = activeGroupId
  const setActiveKeyRef = useRef(setActiveKey)
  setActiveKeyRef.current = setActiveKey
  const switchToGroupRef = useRef<((id: string) => void) | null>(null)

  const openNewSessionModal = useCallback(() => {
    setNewSessionModalOpen(true)
  }, [])

  const openNewSessionPlain = useCallback(() => {
    splitTargetRef.current = null
    setNewSessionModalOpen(true)
  }, [])

  // Global keyboard shortcuts (tinykeys). $mod = Cmd on macOS, Ctrl elsewhere.
  useEffect(() => {
    const cycle = (dir: 1 | -1) => {
      const skeys: string[] = []
      document
        .querySelectorAll('[data-session-key]')
        .forEach(el => skeys.push(el.getAttribute('data-session-key')!))
      if (skeys.length === 0) return
      const current = selectedSessionRef.current
      const idx = current ? skeys.indexOf(current) : -1
      const nextIdx =
        dir === 1
          ? idx >= 0
            ? (idx + 1) % skeys.length
            : 0
          : idx > 0
            ? idx - 1
            : skeys.length - 1
      const targetKey = skeys[nextIdx]
      // If target belongs to a saved group, switch to that group first
      const group = Object.entries(syncedGroupsRef.current).find(([, g]) => findLeaf(g.tree, targetKey))
      if (group) {
        const [groupId] = group
        switchToGroupRef.current?.(groupId)
        setActiveKeyRef.current(targetKey)
        const { host, name } = parseSessionKey(targetKey)
        const path = host
          ? `/session/${encodeURIComponent(host)}/${encodeURIComponent(name)}`
          : `/session/${encodeURIComponent(name)}`
        if (window.location.pathname !== path) window.history.pushState(null, '', path)
      } else {
        selectSessionRef.current?.(targetKey)
      }
    }

    const handler =
      (fn: (e: KeyboardEvent) => void) => (e: KeyboardEvent) => {
        e.preventDefault()
        fn(e)
      }

    // The terminal owns the keyboard. useTerminal() (attachCustomKeyEventHandler)
    // already decides which $mod combos escape xterm and bubble here — that
    // narrow whitelist IS the gate. So we must NOT let tinykeys' default ignore
    // drop events originating from the xterm helper textarea, or whitelisted
    // shortcuts would silently fail while a session is focused. Other form
    // inputs (modals, settings) keep the default ignore behaviour.
    const ignore = (e: KeyboardEvent) => {
      const target = e.target as HTMLElement | null
      if (target?.closest?.('.xterm')) return false
      return (
        e.repeat ||
        e.isComposing ||
        (target !== e.currentTarget &&
          !!target?.matches?.('[contenteditable],input,select,textarea'))
      )
    }

    return tinykeys(window, {
      // Help: Cmd/Ctrl + / (Slash). Shift+Slash ('?') handled by same physical key.
      '$mod+Slash': handler(() => setHelpOpen(prev => !prev)),
      '$mod+Shift+Slash': handler(() => setHelpOpen(prev => !prev)),
      // Toggle sidebar: Cmd/Ctrl + \
      '$mod+Backslash': handler(() => setSidebarCollapsed(c => !c)),
      // Settings: Cmd/Ctrl + ,
      '$mod+Comma': handler(() => openSettings()),
      // Split pane: Cmd/Ctrl + Shift + \
      '$mod+Shift+Backslash': handler(() => {
        if (activeKey !== null) {
          splitTargetRef.current = { key: activeKey, direction: 'h' }
          openNewSessionModal()
        }
      }),
      // Quick Switcher: Cmd/Ctrl + Shift + K (K alone collides w/ Firefox search bar)
      '$mod+Shift+k': handler(() => setQuickSwitcherOpen(true)),
      // New session: Cmd/Ctrl + Shift + Enter (N collides w/ browser private window)
      '$mod+Shift+Enter': handler(() => openNewSessionPlain()),
      // Overview: Cmd/Ctrl + Shift + H (Shift+O collides w/ Firefox bookmarks)
      '$mod+Shift+h': handler(() => navigateTo(null)),
      // Wiki panel: Cmd/Ctrl + Shift + G. G is not mnemonic, but the mnemonic
      // keys are all taken by the browser: Shift+W closes the window, Shift+E
      // opens Firefox's network monitor, Shift+D bookmarks every tab, and
      // Shift+F is already terminal fullscreen here.
      '$mod+Shift+g': handler(() => { if (wikiEnabledRef.current) wiki.togglePanel() }),
      // Cycle sessions: Cmd/Ctrl + Shift + Arrow (Shift+[ / ] switches browser tabs)
      '$mod+Shift+ArrowRight': handler(() => cycle(1)),
      '$mod+Shift+ArrowLeft': handler(() => cycle(-1)),
    }, { ignore })
  }, [navigateTo, activeKey, openNewSessionModal, openNewSessionPlain, wiki.togglePanel])

  // Backend notices (silent failures surfaced to the UI as toasts)
  const [toasts, setToasts] = useState<Toast[]>([])
  const toastIdRef = useRef(0)
  const dismissToast = useCallback((id: number) => setToasts(t => t.filter(x => x.id !== id)), [])

  // Client-side notices (e.g. unsupported pop-out) dispatched as window events
  useEffect(() => {
    const onToast = (e: Event) => {
      const d = (e as CustomEvent).detail || {}
      setToasts(t => [...t, {
        id: ++toastIdRef.current,
        severity: d.severity === 'error' || d.severity === 'warn' ? d.severity : 'info',
        source: d.source || 'termyard',
        message: d.message || '',
      }].slice(-4))
    }
    window.addEventListener('termyard:toast', onToast)
    return () => window.removeEventListener('termyard:toast', onToast)
  }, [])

  const migrateSessionKey = useCallback((oldKey: string, newKey: string) => {
    if (!oldKey || !newKey || oldKey === newKey) return

    workspaceActions.renameSession(oldKey, newKey)

    // Rekey pool entry: preserve terminal/WS, update reconnect identity.
    // If destination already has an entry (collision), dispose it first.
    terminalPool.dispose(newKey)
    terminalPool.rekey(oldKey, newKey)

    const { host: oldHost, name: oldName } = parseSessionKey(oldKey)
    const oldPath = oldHost
      ? `/session/${encodeURIComponent(oldHost)}/${encodeURIComponent(oldName)}`
      : `/session/${encodeURIComponent(oldName)}`
    const { host: newHost, name: newName } = parseSessionKey(newKey)
    const newPath = newHost
      ? `/session/${encodeURIComponent(newHost)}/${encodeURIComponent(newName)}`
      : `/session/${encodeURIComponent(newName)}`
    if (window.location.pathname === oldPath) {
      window.history.replaceState(null, '', newPath)
    }
  }, [workspaceActions])

  // Listen for state events via WebSocket
  const onEvent = useCallback((evt: any) => {
    if (evt.type === 'notice') {
      const d = evt.data || {}
      setToasts(t => [
        ...t,
        {
          id: ++toastIdRef.current,
          severity: d.severity === 'error' || d.severity === 'warn' ? d.severity : 'info',
          source: d.source || 'server',
          message: d.message || '',
          session: evt.session || undefined,
        },
      ].slice(-4))
      return
    }
    if (evt.type === 'welcome') {
      const v = evt.version || null
      if (!loadedVersionRef.current) {
        loadedVersionRef.current = v
      }
      setServerVersion(v)
      return
    }
    if (evt.type === 'tool-event') {
      handleToolEvent(evt)
      processToolEvent(evt)
      window.dispatchEvent(new CustomEvent('termyard:artifacts', { detail: evt }))
      return
    }
    if (evt.type === 'artifacts') {
      window.dispatchEvent(new CustomEvent('termyard:artifacts', { detail: evt }))
      return
    }
    if (evt.type === 'activity') {
      handleActivityEvent(evt.snapshots || [])
      return
    }
    if (['session-removed', 'session-renamed', 'session-added', 'sessions-changed'].includes(evt.type)) {
      workspaceActions.onEvent(evt)
      if (evt.type === 'session-renamed') {
        refreshSessionOrder()
        refreshGroups()
      }
      return
    }
    if (evt.type === 'recovery-started') {
      setRecovering(true)
      return
    }
    if (evt.type === 'recovery-finished') {
      setRecovering(false)
      refresh()
      return
    }
    if (evt.type === 'session-order-updated') {
      refreshSessionOrder()
      return
    }
    if (evt.type === 'groups-updated') {
      refreshGroups()
      return
    }
    if (['peer-connected', 'peer-disconnected'].includes(evt.type)) {
      refresh()
      refreshHosts()
    }
    if (evt.type === 'update-status') {
      setBinaryUpdate(evt)
      return
    }
    if (evt.type === 'session-attrs-updated') {
      refreshSessionAttrs()
    }
    if (evt.type === 'sessions-crashed') {
      crashedHook.refresh()
    }
  }, [refresh, refreshHosts, handleToolEvent, processToolEvent, handleActivityEvent, refreshSessionAttrs, refreshSessionOrder, refreshGroups, workspaceActions, crashedHook.refresh])

  const { connected } = useWebSocket('/ws/events', onEvent)

  useEffect(() => {
    workspaceActions.setConnection(connected === true)
  }, [connected, workspaceActions])

  const pruningSuspended = recovering || workspaceState.connection.livenessUnknown

  // Prune leaves whose session is gone from the live list. While the server is
  // alive the list is authoritative, so a missing session is a genuine kill and
  // its pane is removed at once. Recovery (full-server rebuild) is the only time
  // a live session is transiently absent; pruning is suspended then. We do NOT
  // bail when sessions is empty: killing the last session makes the list empty,
  // and its pane must still be pruned so the terminal unmounts instead of
  // sitting on "disconnected — reconnecting" forever.
  useEffect(() => {
    // Never prune while disconnected or when liveness is still unknown.
    if (sessionsLoading || pruningSuspended || connected !== true) return
    const validKeys = new Set(sessions.map(s => sessionKey(s)))
    if (pendingSessionRef.current) validKeys.add(pendingSessionRef.current)

    // Authoritative sweep: dispose pool entries for sessions gone from the
    // server list.
    terminalPool.disposeAbsent(validKeys, pruningSuspended)

    if (paneTree) {
      workspaceActions.pruneMissing([...validKeys], Date.now())
    }
  }, [sessions, sessionsLoading, paneTree, pruningSuspended, connected, workspaceActions])

  // Release the pending-session guard once the freshly created session shows
  // up in state (remote creates arrive via a delayed peer broadcast).
  useEffect(() => {
    const pending = pendingSessionRef.current
    if (!pending) return
    if (sessions.some(s => sessionKey(s) === pending)) pendingSessionRef.current = null
  }, [sessions])

  useEffect(() => {
    if (!authenticated || !groupsLoaded || !sessionOrderLoaded) return
    if (migrationStartedRef.current) return
    try {
      if (localStorage.getItem('termyard:sync-migrated') === '1') return
    } catch {}
    migrationStartedRef.current = true
    void (async () => {
      try {
        const legacyGroupsRaw = localStorage.getItem('termyard:saved-groups')
        const legacyGroupOrderRaw = localStorage.getItem('termyard:group-order')
        const legacySessionOrderRaw = localStorage.getItem('termyard:session-order')
        const legacyGroups = legacyGroupsRaw ? (JSON.parse(legacyGroupsRaw) as Array<{ id: string; tree: PaneTree; activeKey: string | null; name?: string }>) : []
        const legacyOrder = legacyGroupOrderRaw ? (JSON.parse(legacyGroupOrderRaw) as unknown[]) : []
        const orderIds = (Array.isArray(legacyOrder) && legacyOrder.length > 0
          ? legacyOrder.filter((id): id is string => typeof id === 'string')
          : [activeGroupId, ...legacyGroups.map(g => g.id)])
          .filter((id, idx, all) => id && all.indexOf(id) === idx)
        const legacyById = new Map(legacyGroups.map(group => [group.id, group]))
        let prevRank: string | null = null
        for (const id of orderIds) {
          const serverGroup = syncedGroups[id]
          if (serverGroup) {
            if (serverGroup.rank) {
              prevRank = serverGroup.rank
              continue
            }
            const rank = generateKeyBetween(prevRank, null)
            await setGroupRank(id, rank)
            prevRank = rank
            continue
          }
          const localGroup = id === activeGroupId && paneTree
            ? { id, tree: paneTree, name: activeGroupName || undefined }
            : legacyById.get(id)
          if (!localGroup) continue
          const rank = generateKeyBetween(prevRank, null)
          await setGroupTree(id, localGroup.tree)
          if (localGroup.name) await setGroupName(id, localGroup.name)
          await setGroupRank(id, rank)
          prevRank = rank

      }

        const legacySessionOrder = legacySessionOrderRaw ? (JSON.parse(legacySessionOrderRaw) as unknown[]) : []
        const sessionIds = Array.isArray(legacySessionOrder)
          ? legacySessionOrder.filter((id): id is string => typeof id === 'string')
          : []
        let prevSessionRank: string | null = null
        for (const key of sessionIds) {
          const serverRank = sessionOrderRanks[key]
          if (serverRank) {
            prevSessionRank = serverRank
            continue
          }
          const rank = generateKeyBetween(prevSessionRank, null)
          await setSessionOrderRank(key, rank)
          prevSessionRank = rank
        }
        try { localStorage.setItem('termyard:sync-migrated', '1') } catch {}
      } catch {
        migrationStartedRef.current = false
      }
    })()
  }, [authenticated, groupsLoaded, sessionOrderLoaded, syncedGroups, sessionOrderRanks, paneTree, activeGroupId, activeGroupName, setGroupTree, setGroupName, setGroupRank, setSessionOrderRank])




  const selectSessionRef = useRef<((sk: string) => void) | null>(null)

  const refocusTerminal = useCallback(() => {
    requestAnimationFrame(() => {
      const textarea = terminalContainerRef.current?.querySelector('textarea.xterm-helper-textarea') as HTMLTextAreaElement | null
      textarea?.focus()
    })
  }, [])

  const selectSession = useCallback((sk: string) => {
    const { host, name } = parseSessionKey(sk)
    const path = host
      ? `/session/${encodeURIComponent(host)}/${encodeURIComponent(name)}`
      : `/session/${encodeURIComponent(name)}`
    if (window.location.pathname !== path) window.history.pushState(null, '', path)
    // Local navigation always drives the rendered view (Overview -> session),
    // regardless of the v2 flag. The v2 server `select` command is an
    // independent, additional signal (it keeps the server-side active key in
    // sync), not a replacement for local nav state.
    workspaceActions.selectSession(sk)
    // Refocus even when activeKey didn't change — Terminal auto-focus
    // on inactive panes may have stolen visual focus from the intended one.
    setTimeout(refocusTerminal, 150)
  }, [workspaceActions, refocusTerminal])
  selectSessionRef.current = selectSession

  const handleSessionSelect = (session: Session) => {
    selectSession(sessionKey(session))
  }

  const handlePairSessions = useCallback((draggedKey: string, targetKey: string) => {
    setSingleView(null)
    detachFromOtherGroups(draggedKey)
    detachFromOtherGroups(targetKey)
    const inCurrentTree = paneTree && (findLeaf(paneTree, draggedKey) || findLeaf(paneTree, targetKey))
    if (!inCurrentTree) {
      // Neither session is in the active group — create a new background group
      const newId = Math.random().toString(36).slice(2)
      const newTree: PaneTree = { type: 'split', direction: 'h', ratio: 0.5,
        first: { type: 'leaf', sessionKey: targetKey },
        second: { type: 'leaf', sessionKey: draggedKey } }
      const currentRank = syncedGroups[activeGroupId]?.rank ?? null
      if (paneTree) {
        void setGroupTree(activeGroupId, paneTree)
        if (!currentRank) void setGroupRank(activeGroupId, generateKeyBetween(null, generateKeyBetween(currentRank, null)))
      }
      const nextRank = generateKeyBetween(currentRank, null)
      void setGroupTree(newId, newTree)
      void setGroupRank(newId, nextRank)
      setPaneTree(newTree)
      setActiveKey(draggedKey)
      setActiveGroupId(newId)
      setSingleView(null)
      const { host, name } = parseSessionKey(draggedKey)
      const path = host ? `/session/${encodeURIComponent(host)}/${encodeURIComponent(name)}` : `/session/${encodeURIComponent(name)}`
      if (window.location.pathname !== path) window.history.pushState(null, '', path)
      setCurrentView('session')
      return
    }
    // Existing behavior: add to current group's tree
    setPaneTree((prev: PaneTree | null) => {
      if (prev && findLeaf(prev, draggedKey) && findLeaf(prev, targetKey)) return prev
      if (prev && findLeaf(prev, targetKey)) return splitLeaf(prev, targetKey, 'h', draggedKey)
      if (prev && findLeaf(prev, draggedKey)) return splitLeaf(prev, draggedKey, 'h', targetKey)
      return { type: 'split', direction: 'h', ratio: 0.5,
        first: { type: 'leaf', sessionKey: targetKey },
        second: { type: 'leaf', sessionKey: draggedKey } }
    })
    setActiveKey(draggedKey)
    const { host, name } = parseSessionKey(draggedKey)
    const path = host ? `/session/${encodeURIComponent(host)}/${encodeURIComponent(name)}` : `/session/${encodeURIComponent(name)}`
    if (window.location.pathname !== path) window.history.pushState(null, '', path)
    setCurrentView('session')
  }, [paneTree, activeGroupId, syncedGroups, setGroupRank, setGroupTree, detachFromOtherGroups])

  const switchToGroup = useCallback((groupId: string, focusKey?: string) => {
    // If re-selecting the already-active group (e.g. after navigating to a standalone session),
    // just clear singleView to restore the pane tree view.
    if (groupId === activeGroupId && paneTree) {
      setSingleView(null)
      setCurrentView('session')
      const targetKey = focusKey ?? activeKey
      if (targetKey) {
        const { host, name } = parseSessionKey(targetKey)
        const path = host
          ? `/session/${encodeURIComponent(host)}/${encodeURIComponent(name)}`
          : `/session/${encodeURIComponent(name)}`
        if (window.location.pathname !== path) window.history.pushState(null, '', path)
      }
      setTimeout(refocusTerminal, 150)
      return
    }
    const targetGroup = syncedGroups[groupId]
    if (!targetGroup) return
    if (paneTree) {
      void setGroupTree(activeGroupId, paneTree)
    }
    const targetKey = (focusKey && findLeaf(targetGroup.tree, focusKey))
      ? focusKey
      : (activeGroupId === groupId && activeKey ? activeKey : getLeaves(targetGroup.tree)[0] ?? null)
    setPaneTree(targetGroup.tree)
    setActiveKey(targetKey)
    setActiveGroupId(groupId)
    setSingleView(null)
    setCurrentView('session')
    if (targetKey) {
      const { host, name } = parseSessionKey(targetKey)
      const path = host
        ? `/session/${encodeURIComponent(host)}/${encodeURIComponent(name)}`
        : `/session/${encodeURIComponent(name)}`
      if (window.location.pathname !== path) window.history.pushState(null, '', path)
    }
    setTimeout(refocusTerminal, 150)
  }, [syncedGroups, activeGroupId, paneTree, activeKey, refocusTerminal, setGroupTree])
  switchToGroupRef.current = switchToGroup

  const renameGroup = useCallback((groupId: string, name: string) => {
    void setGroupName(groupId, name)
  }, [setGroupName])

  // Safety-net refocus when activeKey changes via paths that don't call
  // selectSession (e.g. onActivate from clicking inside TiledView).
  useEffect(() => {
    if (currentView === 'session' && paneTree && activeKey) {
      setTimeout(refocusTerminal, 150)
    }
  }, [activeKey, currentView, paneTree, refocusTerminal])

  const jumpToSession = useCallback(async (sessKey: string, windowIndex?: number, pane?: string) => {
    selectSession(sessKey)
    if (windowIndex !== undefined) {
      const { host, name } = parseSessionKey(sessKey)
      try {
        await fetch('/api/session/select-window', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ host: host || undefined, session: name, window: windowIndex, pane: pane || undefined }),
        })
      } catch (err) {
        console.error('Failed to select window:', err)
      }
    }
    setTimeout(() => refocusTerminal(), 200)
  }, [selectSession, refocusTerminal])

  const prevPathRef = useRef<string>('/')
  const openSettings = useCallback(() => {
    if (window.location.pathname !== '/settings') {
      prevPathRef.current = window.location.pathname
      window.history.pushState(null, '', '/settings')
    }
    setSettingsOpen(true)
  }, [])
  const closeSettings = useCallback(() => {
    setSettingsOpen(false)
    if (window.location.pathname === '/settings') {
      window.history.pushState(null, '', prevPathRef.current || '/')
    }
  }, [])

  const handleCreateSession = useCallback(async (
    name: string,
    path: string,
    command: string,
    hostId?: string,
    worktreeBranch?: string,
    agentType?: string,
    splitTarget?: { key: string; direction: 'h' | 'v'; newFirst?: boolean },
  ): Promise<string | null> => {
    // For worktree sessions keep the modal open until we confirm success.
    if (!worktreeBranch) setNewSessionModalOpen(false)

    // Optimistic: render sidebar stub + mount terminal instantly. Resolved
    // name may differ from the requested name if the server dedups, so we
    // migrate the session key once the POST resolves.
    const optimisticKey = hostId ? `${hostId}/${name}` : name
    if (!worktreeBranch) {
      pendingSessionRef.current = optimisticKey
      const fallbackPending = optimisticKey
      window.setTimeout(() => {
        if (pendingSessionRef.current === fallbackPending) pendingSessionRef.current = null
      }, 15000)
      workspaceActions.addOptimistic(optimisticSession(name, hostId || localHostId, localHostName, path))
    }

    // Apply the split/single layout with the optimistic key now, so the pane
    // mounts in parallel with the backend create.
    const target = splitTarget ?? splitTargetRef.current
    splitTargetRef.current = null
    if (!worktreeBranch) {
      if (target) {
        workspaceActions.splitPane(target.key, target.direction, optimisticKey, target.newFirst)
      } else {
        selectSession(optimisticKey)
      }
      setTimeout(() => refocusTerminal(), 0)
    }

    try {
      // Legacy API: create session via per-field endpoint
      let resolvedName: string
      const res = await fetch('/api/session/new', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name, path, command, host: hostId || undefined, agent_type: agentType || undefined, worktree_branch: worktreeBranch || undefined }),
      })
      if (!res.ok) {
        if (!worktreeBranch) {
          workspaceActions.removeOptimistic(name, hostId || localHostId)
          if (pendingSessionRef.current === optimisticKey) pendingSessionRef.current = null
        }
        if (worktreeBranch) {
          const msg = await res.text().catch(() => 'Failed to create worktree')
          return msg
        }
        return null
      }
      if (worktreeBranch) setNewSessionModalOpen(false)
      const payload = await res.json().catch(() => null)
      resolvedName = payload?.name || name
      if (resolvedName !== name && !worktreeBranch) {
        // Server deduped the name — migrate the optimistic key to the real one.
        // Don't upsert a fresh stub: the real record is already on its way via
        // the server's session-added broadcast; just protect it from pruning.
        const resolvedKey = hostId ? `${hostId}/${resolvedName}` : resolvedName
        workspaceActions.removeOptimistic(name, hostId || localHostId)
        workspaceActions.renameSession(optimisticKey, resolvedKey)
        pendingSessionRef.current = resolvedKey
      }
      // Reconcile with the real server record; clears the optimistic stub.
      workspaceActions.refresh()
    } catch (err) {
      console.error('Failed to create session:', err)
      if (!worktreeBranch) {
        workspaceActions.removeOptimistic(name, hostId || localHostId)
        if (pendingSessionRef.current === optimisticKey) pendingSessionRef.current = null
      }
    }
    return null
  }, [selectSession, refocusTerminal, localHostId, localHostName, workspaceActions])

  const handleQuickShell = useCallback(() => {
    const name = `shell-${Date.now()}`
    const sk = localHostId ? `${localHostId}/${name}` : name
    // Optimistic: fire the backend create first (non-awaited) so the daemon
    // starts spawning immediately, then render the sidebar stub + mount the
    // terminal. The Terminal pool's WS connect retries while NewDaemonSession
    // (server-side) retries the dial until the socket is ready.
    fetch('/api/session/new', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name, path: '', command: '', backend: 'daemon' }),
    }).then(res => {
      if (!res.ok) {
        workspaceActions.removeOptimistic(name, localHostId)
        if (pendingSessionRef.current === sk) pendingSessionRef.current = null
      } else {
        // Reconcile with the real server record as soon as the daemon is up.
        workspaceActions.refresh()
      }
    }).catch(() => {
      workspaceActions.removeOptimistic(name, localHostId)
      if (pendingSessionRef.current === sk) pendingSessionRef.current = null
    })
    pendingSessionRef.current = sk
    workspaceActions.addOptimistic(optimisticSession(name, localHostId, localHostName))
    selectSession(sk)
    setTimeout(() => refocusTerminal(), 0)
  }, [selectSession, refocusTerminal, localHostId, localHostName, workspaceActions])



  const handleDropNewSession = useCallback((targetKey: string, edge: 'left'|'right'|'top'|'bottom'|'center') => {
    // Fall back to the first pane when a split is visible but no active pane is
    // set, so a drop that lands on the container (not a pane) still splits
    // instead of silently spawning a standalone session ("refuse to split").
    let key = targetKey || activeKey || (paneTree ? getLeaves(paneTree)[0] ?? null : null)

    // Dropping onto a singleView session (standalone, not in any group):
    // save current group to background and start a new group from singleView
    if (!targetKey && singleView) {
      key = singleView
      const newGroupId = Math.random().toString(36).slice(2)
      const currentRank = syncedGroups[activeGroupId]?.rank ?? null
      if (paneTree) {
        void setGroupTree(activeGroupId, paneTree)
        if (!currentRank) void setGroupRank(activeGroupId, generateKeyBetween(null, generateKeyBetween(currentRank, null)))
      }
      const newRank = generateKeyBetween(currentRank, null)
      void setGroupTree(newGroupId, popOut(singleView))
      void setGroupRank(newGroupId, newRank)
      setPaneTree(popOut(singleView))
      setActiveKey(singleView)
      setActiveGroupId(newGroupId)
      setSingleView(null)
    }

    let splitTarget: { key: string; direction: 'h' | 'v'; newFirst?: boolean } | undefined
    if (key) {
      const direction: 'h' | 'v' = (edge === 'top' || edge === 'bottom') ? 'v' : 'h'
      const newFirst = edge === 'left' || edge === 'top'
      splitTarget = { key, direction, newFirst }
      // Also set ref for dissolve-effect guard; handleCreateSession prefers direct param
      splitTargetRef.current = splitTarget
    }
    const { host } = key ? parseSessionKey(key) : { host: undefined }
    // Inherit the target pane's cwd so drop-to-split opens "here".
    const sess = key ? sessionsRef.current.find(s => sessionKey(s) === key) : undefined
    const panes = sess?.windows.flatMap(w => w.panes) ?? []
    const cwd = panes.find(p => p.active)?.current_path || sess?.project_path || '~'
    // Unique name so the optimistic pane-tree key can't collide with an
    // existing leaf. A literal 'shell' would duplicate any live 'shell' leaf
    // (shared pool entry) and get mangled when the server dedup migrates it.
    const existingNames = new Set(sessionsRef.current.map(s => s.name))
    let name = 'shell'
    for (let n = 2; existingNames.has(name); n++) name = `shell-${n}`
    // Pass splitTarget directly — avoids ref race when event fires on both pane and container
    handleCreateSession(name, cwd, '', host || undefined, undefined, undefined, splitTarget)
  }, [singleView, activeKey, activeGroupId, paneTree, handleCreateSession])

  const toggleFullscreen = useCallback(() => {
    setTerminalFullscreen(f => !f)
  }, [])

  // Keep the browser title stable unless user attention is needed.
  useEffect(() => {
    const needsAttention = allToolEvents.some(
      evt => evt.status === 'waiting' || evt.status === 'error' || evt.status === 'stuck',
    )
    document.title = needsAttention ? 'Termyard - Attention needed' : 'Termyard'
  }, [allToolEvents])

  // Exit fullscreen when navigating away from terminal
  useEffect(() => {
    if (currentView !== 'session') {
      setTerminalFullscreen(false)
    }
  }, [currentView])

  const layoutGroups = useMemo<LayoutGroup[]>(() => {
    const ids = new Set<string>(Object.keys(syncedGroups))
    if (paneTree) ids.add(activeGroupId)
    return Array.from(ids).map(id => {
      const group = syncedGroups[id]
      const isActive = id === activeGroupId && currentView === 'session' && singleView === null
      const leaves = id === activeGroupId && paneTree ? getLeaves(paneTree) : (group ? getLeaves(group.tree) : [])
      const activeLeaf = id === activeGroupId ? activeKey : (group ? getLeaves(group.tree)[0] ?? null : null)
      return {
        id,
        leaves,
        isActive,
        activeKey: activeLeaf,
        name: group?.name ?? (id === activeGroupId ? activeGroupName || undefined : undefined),
      }
    }).sort((a, b) => {
      const ar = syncedGroups[a.id]?.rank ?? (a.id === activeGroupId ? activeGroup?.rank ?? '' : '')
      const br = syncedGroups[b.id]?.rank ?? (b.id === activeGroupId ? activeGroup?.rank ?? '' : '')
      if (!ar && br) return 1
      if (ar && !br) return -1
      if (ar !== br) return ar.localeCompare(br)
      return a.id.localeCompare(b.id)
    })
  }, [syncedGroups, paneTree, activeGroupId, activeKey, currentView, singleView, activeGroup?.rank, activeGroupName])

  const renderSessions = sessions
  const renderPaneTree = paneTree
  const renderActiveKey = activeKey
  const renderSingleView = singleView

  const showingTerminal = currentView === 'session' && !!selectedSession

  const glance = useMemo(() => {
    let parked = 0
    let working = 0
    let waiting = 0
    for (const session of sessions) {
      const key = sessionKey(session)
      const signal = sessionSignal(session, getSessionEvents(key), getSessionActivity(key), isSessionInActiveTurn(key))
      if (signal.state === 'needs_you') waiting++
      else if (signal.state === 'working') working++
      else parked++
    }
    return { parked, working, waiting }
  }, [sessions, getSessionEvents, getSessionActivity, isSessionInActiveTurn, allToolEvents])

  return (
    <div className="flex flex-col h-full w-full bg-background text-foreground relative">
      <Toasts toasts={toasts} onDismiss={dismissToast} />
      {crashedHook.crashedSessions.length > 0 && (
        <RecoveryPanel
          crashedSessions={crashedHook.crashedSessions}
          onRecover={crashedHook.recover}
          onDismiss={crashedHook.dismiss}
          onDismissAll={crashedHook.dismissAll}
        />
      )}
      {helpOpen && <HelpModal onClose={() => setHelpOpen(false)} />}
      {portForwardsOpen && (
        <PortForwardModal onClose={() => setPortForwardsOpen(false)} />
      )}
      {schedulesOpen && (
        <ScheduleModal onClose={() => setSchedulesOpen(false)} />
      )}
      {quickSwitcherOpen && (
        <QuickSwitcher
          sessions={sessions}
          waitingEvents={allToolEvents}
          onSelect={(sessionName, windowIndex) => {
            selectSession(sessionName)
            setQuickSwitcherOpen(false)
            if (windowIndex !== undefined) {
              const { host, name } = parseSessionKey(sessionName)
              fetch('/api/session/select-window', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ host: host || undefined, session: name, window: windowIndex }),
              }).catch(err => console.error('Failed to select window:', err))
            }
          }}
          onOverview={() => {
            navigateTo(null)
            setQuickSwitcherOpen(false)
          }}
          onCreateSession={() => {
            openNewSessionModal()
            setQuickSwitcherOpen(false)
          }}
          onClose={() => setQuickSwitcherOpen(false)}
        />
      )}
      {newSessionModalOpen && (
        <NewSessionModal
          hosts={hosts}
          sessions={sessions}
          onCreateSession={handleCreateSession}
          onClose={() => setNewSessionModalOpen(false)}
        />
      )}
      {/* TopBar - full width */}
      {(!terminalFullscreen || !prefs.fullscreen_hide_alerts) && (
        <TopBar
          currentView={currentView}
          settingsActive={settingsOpen}
          selfUpdateAvailable={selfUpdate.updateVisible}
          updateVersion={selfUpdate.status?.latest_version}
          onApplyUpdate={selfUpdate.apply}
          updateApplying={selfUpdate.applying}
          onDismissUpdate={selfUpdate.dismiss}
          onOverview={() => navigateTo(null)}
          onSettings={openSettings}
          onWiki={wikiEnabled ? wiki.togglePanel : undefined}
          onHelp={() => setHelpOpen(true)}
          onNewSession={openNewSessionModal}
          onPortForwards={() => setPortForwardsOpen(true)}
          onSchedules={() => setSchedulesOpen(true)}
          events={allToolEvents}
          connected={connected}
          onJumpToSession={jumpToSession}
          onDismiss={dismissEvent}
          onDismissAll={dismissAllEvents}
          panesCount={renderPaneTree ? getLeaves(renderPaneTree).length : 0}
          onSplitPane={(direction) => {
            if (activeKey !== null) {
              splitTargetRef.current = { key: activeKey, direction }
            }
            openNewSessionModal()
          }}
          glance={glance}
        />
      )}
      {/* Middle: Sidebar + Content + Wiki panel.
          Three fixed child slots. Keep them static: React reconciles by index,
          and a falsy slot still holds its index, so toggling fullscreen hides the
          sidebar without shifting the wiki panel and remounting its iframe. */}
      <div className="flex-1 flex overflow-hidden">
        {!terminalFullscreen && (
          <Sidebar
            sessions={renderSessions}
            selectedSession={selectedSession}
            collapsed={sidebarCollapsed}
            selfUpdateAvailable={selfUpdate.status?.update_available ?? false}
            collapseMode={(prefs.sidebar.collapse_mode || 'small') as 'small' | 'hidden'}
            width={sidebarWidth}
            onWidthChange={handleSidebarWidth}
            hasMultipleHosts={hasMultipleHosts}
            localHostId={localHostId}
            hosts={hosts}
            onSessionSelect={handleSessionSelect}
            getSessionEvents={getSessionEvents}
            sessionNeedsAttention={sessionNeedsAttention}
            isSessionInActiveTurn={isSessionInActiveTurn}
            getSessionActivity={getSessionActivity}
            agentCount={allToolEvents.filter(e => e.auto_detected || e.status === 'waiting' || e.status === 'error' || e.status === 'stuck').length}
            glance={glance}
            onToggleCollapse={() => setSidebarCollapsed(c => !c)}
            layoutGroups={layoutGroups}
            sessionOrderRanks={sessionOrderRanks}
            setSessionOrderRank={setSessionOrderRank}
            onSwitchGroup={switchToGroup}
            onRenameGroup={renameGroup}
            forceAiName={forceAiName}
            namingGroupId={namingGroupId}
            onPairSessions={handlePairSessions}
            onRemoveFromSplit={closePane}
            onSessionKilled={removeSessionFromLayout}
            sessionAttrs={sessionAttrs}
            setSessionAttr={setSessionAttr}
            pruningSuspended={pruningSuspended}

            onQuickShell={handleQuickShell}
            crashedCount={crashedHook.crashedSessions.length}
            onCrashedClick={() => crashedHook.refresh()}
          />
        )}
        <div
          className="flex-1 flex flex-col overflow-hidden relative"
          data-drop-zone="main"
          onDragOver={(e) => {
            const dt = e.dataTransfer
            const getZone = (): 'left'|'right'|'top'|'bottom'|'center' => {
              const rect = e.currentTarget.getBoundingClientRect()
              const x = e.clientX - rect.left
              const y = e.clientY - rect.top
              const w = rect.width
              const h = rect.height
              if (x < w * 0.25) return 'left'
              if (x > w * 0.75) return 'right'
              if (y < h * 0.25) return 'top'
              if (y > h * 0.75) return 'bottom'
              return 'center'
            }
            if (dt.types.includes('application/x-termyard-new-session')) {
              e.preventDefault()
              e.dataTransfer.dropEffect = 'copy'
              const val = { type: 'new-session' as const, zone: getZone() }
              mainDragOverRef.current = val
              setMainDragOver(val)
              return
            }
            if (dt.types.includes('text/plain') && !dt.types.includes('application/x-termyard-pane')) {
              e.preventDefault()
              const val = { type: 'sidebar' as const, zone: getZone() }
              mainDragOverRef.current = val
              setMainDragOver(val)
            }
          }}
          onDragLeave={(e) => {
            if (!e.currentTarget.contains(e.relatedTarget as Node)) {
              mainDragOverRef.current = null
              setMainDragOver(null)
            }
          }}
          onDrop={(e) => {
            e.preventDefault()
            const zone = mainDragOverRef.current?.zone ?? 'center'
            mainDragOverRef.current = null
            setMainDragOver(null)
            if (e.dataTransfer.types.includes('application/x-termyard-new-session')) {
              handleDropNewSession('', zone)
              return
            }
            const sessKey = e.dataTransfer.getData('text/plain')
            if (sessKey && !e.dataTransfer.types.includes('application/x-termyard-pane')) {
              handleDropSession(sessKey, singleView ?? '', zone)
            }
          }}
        >
          {mainDragOver && (
            <div className="absolute inset-0 z-50 pointer-events-none">
              {/* Edge strip */}
              <div
                className="absolute bg-primary"
                style={{
                  ...(mainDragOver.zone === 'left' && { left: 0, top: 0, bottom: 0, width: 1 }),
                  ...(mainDragOver.zone === 'right' && { right: 0, top: 0, bottom: 0, width: 1 }),
                  ...(mainDragOver.zone === 'top' && { top: 0, left: 0, right: 0, height: 1 }),
                  ...(mainDragOver.zone === 'bottom' && { bottom: 0, left: 0, right: 0, height: 1 }),
                }}
              />
              {mainDragOver.zone === 'center' ? (
                <div className="absolute inset-0 bg-primary/10 border-2 border-dashed border-primary rounded-lg flex items-center justify-center">
                  <span className="text-sm font-medium text-primary">+ Split</span>
                </div>
              ) : (
                <div
                  className="absolute bg-primary/10"
                  style={{
                    ...(mainDragOver.zone === 'left' && { left: 0, top: 0, bottom: 0, width: '50%' }),
                    ...(mainDragOver.zone === 'right' && { right: 0, top: 0, bottom: 0, width: '50%' }),
                    ...(mainDragOver.zone === 'top' && { top: 0, left: 0, right: 0, height: '50%' }),
                    ...(mainDragOver.zone === 'bottom' && { bottom: 0, left: 0, right: 0, height: '50%' }),
                  }}
                />
              )}
            </div>
          )}
          {(currentView as View) === 'setup' ? (
            <Setup onComplete={() => navigateTo(null)} />
          ) : currentView === 'session' && renderSingleView ? (
            <div ref={terminalContainerRef} className="flex-1 flex flex-col overflow-hidden">
              <Terminal
                sessionName={parseSessionKey(renderSingleView).name}
                hostId={parseSessionKey(renderSingleView).host || undefined}
                backend={sessions.find(s => sessionKey(s) === renderSingleView)?.backend}
                fullscreen={terminalFullscreen}
                onToggleFullscreen={toggleFullscreen}
                onOpenFile={(path) => wiki.openFile(
                  path,
                  renderSingleView ? cwdForKey(renderSingleView) : undefined,
                  sessions.find(s => sessionKey(s) === renderSingleView)?.host,
                  renderSingleView ? parseSessionKey(renderSingleView).name : undefined,
                )}
              />
            </div>
          ) : currentView === 'session' && renderPaneTree ? (
            <TiledView
              tree={renderPaneTree}
              activeKey={renderActiveKey}
              onActivate={(key) => {
                setActiveKey(key)
                refocusTerminal()
              }}
              onClose={closePane}
              onKill={killPane}
              onPopOut={(key) => {
                popOutPane(key)
              }}
              onSplit={(key, direction) => {
                splitTargetRef.current = { key, direction }
                openNewSessionModal()
              }}
              onRatioChange={(path, ratio) => {
                setPaneTree((prev: PaneTree | null) => {
                  if (prev === null) return null
                  return updateRatio(prev, path, ratio)
                })
              }}
              fullscreen={terminalFullscreen}
              onToggleFullscreen={toggleFullscreen}
              terminalContainerRef={terminalContainerRef}
              onDropSession={handleDropSession}
              onDropNewSession={handleDropNewSession}
              onSwapPanes={(a, b) => {
                setPaneTree((prev: PaneTree | null) => prev ? swapLeaves(prev, a, b) : prev)
              }}
              onMovePanes={(sourceKey, targetKey, edge) => {
                setPaneTree((prev: PaneTree | null) => prev ? movePane(prev, sourceKey, targetKey, edge) : prev)
              }}
              getBackend={(key) => sessions.find(s => sessionKey(s) === key)?.backend}
              getCwd={cwdForKey}
              onOpenFile={wiki.openFile}
            />
          ) : (
            <Overview
              sessions={sessions}
              onOpenFile={wiki.openFile}
              hosts={hosts}
              hiddenSet={sessionAttrs.hidden}
              backgroundSet={sessionAttrs.background}
              scheduleIDs={sessionAttrs.scheduleIDs}
              onSessionSelect={handleSessionSelect}
              getSessionEvents={getSessionEvents}
              getSessionActivity={getSessionActivity}
              isSessionInActiveTurn={isSessionInActiveTurn}
              onJumpToSession={jumpToSession}
              onDismissAlert={dismissEvent}
              setSessionAttr={setSessionAttr}
              onSessionKilled={removeSessionFromLayout}
              layoutGroups={layoutGroups}
            />
          )}
          {/* Single shared slot: the active pane portals its mobile key bar here,
              so split views show one full-width bar instead of one per pane. */}
          <div id="mobile-keybar-slot" className="flex-none" />
          <SettingsDrawer
            open={settingsOpen}
            onClose={closeSettings}
            pushState={pushState}
            onPushSubscribe={pushSubscribe}
            onPushUnsubscribe={pushUnsubscribe}
            onLogout={onLogout}
            version={serverVersion}
            updateAvailable={updateAvailable}
            binaryUpdate={selfUpdate.status}
            onApplyUpdate={selfUpdate.apply}
            updateApplying={selfUpdate.applying}
            updateRestartMode={selfUpdate.restartMode}
            updateError={selfUpdate.error}
            updateChecking={selfUpdate.checking}
            onCheckUpdate={selfUpdate.checkNow}
          />
        </div>
        {wiki.target && (
          <WikiPanel
            filePath={wiki.target.path}
            openNonce={wiki.target.nonce}
            sessionCwd={wiki.target.cwd}
            hostId={wiki.target.hostId}
            session={wiki.target.session}
            onClose={() => wiki.closePanel()}
          />
        )}
      </div>
    </div>
  )
}

function AppV2({ onLogout, authenticated }: { onLogout?: () => void; authenticated: boolean }) {
  // V2 mode: normalized catalog + workspace state via useV2State.
  // Simpler than AppLegacy: only one layout (no groups/singleView).
  const v2State = useV2State({ enabled: true })
  const { state, paneTree, activeKey, layoutId } = v2State
  const currentView: View = paneTree ? 'session' : 'overview'

  // Shared non-session hooks (same as AppLegacy)
  const { prefs } = usePreferences()
  const wikiEnabled = !prefs.wiki_disabled
  const { events: allToolEvents, handleEvent: handleToolEvent, getSessionEvents, sessionNeedsAttention, isSessionInActiveTurn, dismissEvent, dismissAll: dismissAllEvents } = useToolEvents()
  const { getSessionActivity, handleActivityEvent } = useActivity()
  const { pushState, subscribe: pushSubscribe, unsubscribe: pushUnsubscribe } = usePushNotifications()
  const { processToolEvent } = useNotifications(pushState === 'subscribed')
  const { hosts, refresh: refreshHosts } = useHosts()
  const { prefs: _ } = usePreferences() // already have prefs above
  const wiki = useWikiController(undefined as any, wikiEnabled) // v2 doesn't have workspace object
  const { sets: sessionAttrs, setAttr: setSessionAttr, refresh: refreshSessionAttrs } = useSessionAttrs(authenticated)
  const crashedHook = useCrashedSessions()
  const selfUpdate = useSelfUpdate(null)
  const wikiEnabledRef = useRef(wikiEnabled)
  wikiEnabledRef.current = wikiEnabled

  useEffect(() => {
    if (!wikiEnabled) wiki.closePanel()
  }, [wikiEnabled, wiki.closePanel])

  const [serverVersion, setServerVersion] = useState<string | null>(null)
  const loadedVersionRef = useRef<string | null>(null)
  const updateAvailable = loadedVersionRef.current !== null && serverVersion !== null && serverVersion !== loadedVersionRef.current
  const [newSessionModalOpen, setNewSessionModalOpen] = useState(false)
  const terminalContainerRef = useRef<HTMLDivElement>(null)
  const [sidebarCollapsed, setSidebarCollapsed] = useState(() => {
    try { return localStorage.getItem('termyard:sidebar-collapsed') === 'true' } catch { return false }
  })
  const [sidebarWidth, setSidebarWidth] = useState(() => {
    try {
      const v = parseInt(localStorage.getItem('termyard:sidebar-width') || '', 10)
      if (!Number.isNaN(v)) return Math.min(560, Math.max(200, v))
    } catch {}
    return 288
  })
  const handleSidebarWidth = useCallback((w: number) => {
    setSidebarWidth(w)
    try { localStorage.setItem('termyard:sidebar-width', String(w)) } catch {}
  }, [])
  const [terminalFullscreen, setTerminalFullscreen] = useState(false)
  const [helpOpen, setHelpOpen] = useState(false)
  const [quickSwitcherOpen, setQuickSwitcherOpen] = useState(false)
  const [portForwardsOpen, setPortForwardsOpen] = useState(false)
  const [schedulesOpen, setSchedulesOpen] = useState(false)
  const [mainDragOver, setMainDragOver] = useState<{ type: 'new-session' | 'sidebar'; zone: 'left' | 'right' | 'top' | 'bottom' | 'center' } | null>(null)
  const mainDragOverRef = useRef<{ type: 'new-session' | 'sidebar'; zone: 'left' | 'right' | 'top' | 'bottom' | 'center' } | null>(null)

  const lastActivityRef = useRef<number>(Date.now())
  useEffect(() => {
    if (!onLogout || !prefs.lock_timeout_minutes) return
    const idleMs = prefs.lock_timeout_minutes * 60 * 1000
    const onActivity = () => { lastActivityRef.current = Date.now() }
    const events = ['keydown', 'click', 'scroll', 'touchstart', 'mousemove'] as const
    const opts: AddEventListenerOptions = { passive: true, capture: true }
    events.forEach(e => document.addEventListener(e, onActivity, opts))
    const checkInterval = setInterval(() => {
      const elapsed = Date.now() - lastActivityRef.current
      if (elapsed >= idleMs) onLogout()
    }, 30_000)
    return () => {
      events.forEach(e => document.removeEventListener(e, onActivity, opts as EventListenerOptions))
      clearInterval(checkInterval)
    }
  }, [onLogout, prefs.lock_timeout_minutes])

  useEffect(() => {
    localStorage.setItem('termyard:sidebar-collapsed', String(sidebarCollapsed))
  }, [sidebarCollapsed])

  // getTerminalIdentity: converts legacy sessionKey to v2 catalog lookup.
  //
  // Invariant: in v2 mode a terminal must NEVER silently fall back to legacy
  // name-based routing. terminalPool.checkout() only rejects an identity when
  // ownerId or generation is present without sessionId; a fully-empty object
  // would pass through undetected as "no v2 identity supplied" (the legacy
  // caller's shape) even though this IS the v2 caller. So when a session
  // cannot be resolved (unknown ref, or catalog not yet bootstrapped), we
  // still surface the catalog's own owner id (always known once bootstrapped)
  // with no sessionId, which trips that invariant deliberately instead of
  // silently degrading to legacy routing.
  const getTerminalIdentity = useCallback((legacyKey: string) => {
    if (!legacyKey) return {}
    const ref = keyToSessionRef(legacyKey)
    const session = selectSessionByRef(state.catalog, ref)
    if (!session) {
      return state.catalog.owner ? { ownerId: state.catalog.owner } : {}
    }
    // generation is the per-session daemon binding generation from the session's
    // compat field, NOT the websocket connection generation (state.catalog.generation).
    // If the session has no daemon generation yet (e.g., pending), we return empty
    // string; this triggers the invariant check in terminalPool.checkout().
    const sessionGeneration = session._compat?.generation ?? ''
    return {
      sessionId: session.id,
      ownerId: session.owner,
      generation: String(sessionGeneration),
    }
  }, [state.catalog])

  const [toasts, setToasts] = useState<Toast[]>([])
  const toastIdRef = useRef(0)
  const dismissToast = useCallback((id: number) => setToasts(t => t.filter(x => x.id !== id)), [])

  useEffect(() => {
    const onToast = (e: Event) => {
      const d = (e as CustomEvent).detail || {}
      setToasts(t => [...t, {
        id: ++toastIdRef.current,
        severity: d.severity === 'error' || d.severity === 'warn' ? d.severity : 'info',
        source: d.source || 'termyard',
        message: d.message || '',
      }].slice(-4))
    }
    window.addEventListener('termyard:toast', onToast)
    return () => window.removeEventListener('termyard:toast', onToast)
  }, [])

  const onEvent = useCallback((evt: any) => {
    if (evt.type === 'notice') {
      const d = evt.data || {}
      setToasts(t => [{
        id: ++toastIdRef.current,
        severity: d.severity === 'error' || d.severity === 'warn' ? d.severity : 'info',
        source: d.source || 'server',
        message: d.message || '',
        session: evt.session || undefined,
      }, ...t].slice(-4))
    } else if (evt.type === 'welcome') {
      const v = evt.version || null
      if (!loadedVersionRef.current) loadedVersionRef.current = v
      setServerVersion(v)
    } else if (evt.type === 'tool-event') {
      handleToolEvent(evt)
      processToolEvent(evt)
      window.dispatchEvent(new CustomEvent('termyard:artifacts', { detail: evt }))
    } else if (evt.type === 'artifacts') {
      window.dispatchEvent(new CustomEvent('termyard:artifacts', { detail: evt }))
    } else if (evt.type === 'activity') {
      handleActivityEvent(evt.snapshots || [])
    } else if (evt.type === 'recovery-started' || evt.type === 'recovery-finished' || evt.type === 'session-order-updated' || evt.type === 'groups-updated') {
      // v2 doesn't use these, but listen silently
    } else if (['peer-connected', 'peer-disconnected'].includes(evt.type)) {
      refreshHosts()
    } else if (evt.type === 'update-status') {
      // ignore
    } else if (evt.type === 'session-attrs-updated') {
      refreshSessionAttrs()
    } else if (evt.type === 'sessions-crashed') {
      crashedHook.refresh()
    }
  }, [handleToolEvent, processToolEvent, handleActivityEvent, refreshSessionAttrs, refreshHosts, crashedHook.refresh])

  const { connected } = useWebSocket('/ws/events', onEvent)

  useEffect(() => {
    const needsAttention = allToolEvents.some(evt => evt.status === 'waiting' || evt.status === 'error' || evt.status === 'stuck')
    document.title = needsAttention ? 'Termyard - Attention needed' : 'Termyard'
  }, [allToolEvents])

  useEffect(() => {
    if (currentView !== 'session') setTerminalFullscreen(false)
  }, [currentView])

  const hasMultipleHosts = hosts.length > 1
  const localHost = hosts.find(h => h.local)
  const localHostId = localHost?.id
  const localHostName = localHost?.name

  // Single layout group (v2 doesn't have multiple groups)
  const layoutGroups = useMemo<LayoutGroup[]>(() => {
    if (!layoutId) return []
    return [{
      id: layoutId,
      leaves: paneTree ? getLeaves(paneTree) : [],
      isActive: currentView === 'session',
      activeKey: activeKey,
      name: undefined,
    }]
  }, [layoutId, paneTree, activeKey, currentView])

  const refocusTerminal = useCallback(() => {
    requestAnimationFrame(() => {
      const textarea = terminalContainerRef.current?.querySelector('textarea.xterm-helper-textarea') as HTMLTextAreaElement | null
      textarea?.focus()
    })
  }, [])

  const handleSessionSelect = (session: Session) => {
    const key = sessionKey(session)
    const { host, name } = parseSessionKey(key)
    const path = host
      ? `/session/${encodeURIComponent(host)}/${encodeURIComponent(name)}`
      : `/session/${encodeURIComponent(name)}`
    if (window.location.pathname !== path) window.history.pushState(null, '', path)
    if (layoutId && activeKey !== key) {
      v2State.workspaceCommand(layoutId, { action: 'select', ref: keyToSessionRef(key) })
    }
    setTimeout(refocusTerminal, 150)
  }

  const handleCreateSession = useCallback(async (name: string, path: string, command: string, hostId?: string, _wb?: string, _ag?: string): Promise<string | null> => {
    setNewSessionModalOpen(false)
    try {
      const res = await fetch('/api/session/new', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name, path, command, host: hostId || undefined }),
      })
      if (!res.ok) return null
      const payload = await res.json().catch(() => null)
      return payload?.name || name
    } catch (err) {
      console.error('Failed to create session:', err)
      return null
    }
  }, [])

  const toggleFullscreen = useCallback(() => {
    setTerminalFullscreen(f => !f)
  }, [])

  const glance = useMemo(() => {
    const allSessions = Array.from(state.catalog.sessionsByRef.values())
    let parked = 0, working = 0, waiting = 0
    for (const sess of allSessions) {
      const key = sessionRefToKey(sess.ref)
      const signal = sessionSignal(undefined as any, getSessionEvents(key), getSessionActivity(key), isSessionInActiveTurn(key))
      if (signal.state === 'needs_you') waiting++
      else if (signal.state === 'working') working++
      else parked++
    }
    return { parked, working, waiting }
  }, [state.catalog.sessionsByRef, getSessionEvents, getSessionActivity, isSessionInActiveTurn, allToolEvents])

  const openNewSessionModal = useCallback(() => {
    setNewSessionModalOpen(true)
  }, [])

  const openSettings = useCallback(() => {
    if (window.location.pathname !== '/settings') window.history.pushState(null, '', '/settings')
  }, [])

  const closeSettings = useCallback(() => {
    window.history.pushState(null, '', '/')
  }, [])

  return (
    <div className="flex flex-col h-full w-full bg-background text-foreground relative">
      <Toasts toasts={toasts} onDismiss={dismissToast} />
      {crashedHook.crashedSessions.length > 0 && (
        <RecoveryPanel
          crashedSessions={crashedHook.crashedSessions}
          onRecover={crashedHook.recover}
          onDismiss={crashedHook.dismiss}
          onDismissAll={crashedHook.dismissAll}
        />
      )}
      {helpOpen && <HelpModal onClose={() => setHelpOpen(false)} />}
      {portForwardsOpen && <PortForwardModal onClose={() => setPortForwardsOpen(false)} />}
      {schedulesOpen && <ScheduleModal onClose={() => setSchedulesOpen(false)} />}
      {quickSwitcherOpen && (
        <QuickSwitcher
          sessions={[]}
          waitingEvents={allToolEvents}
          onSelect={() => setQuickSwitcherOpen(false)}
          onOverview={() => setQuickSwitcherOpen(false)}
          onCreateSession={() => {
            openNewSessionModal()
            setQuickSwitcherOpen(false)
          }}
          onClose={() => setQuickSwitcherOpen(false)}
        />
      )}
      {newSessionModalOpen && (
        <NewSessionModal
          hosts={hosts}
          sessions={[]}
          onCreateSession={handleCreateSession}
          onClose={() => setNewSessionModalOpen(false)}
        />
      )}
      {(!terminalFullscreen) && (
        <TopBar
          currentView={currentView}
          settingsActive={false}
          selfUpdateAvailable={selfUpdate.status?.update_available ?? false}
          updateVersion={selfUpdate.status?.latest_version}
          onApplyUpdate={selfUpdate.apply}
          updateApplying={selfUpdate.applying}
          onDismissUpdate={selfUpdate.dismiss}
          onOverview={() => window.history.pushState(null, '', '/')}
          onSettings={openSettings}
          onWiki={wikiEnabled ? wiki.togglePanel : undefined}
          onHelp={() => setHelpOpen(true)}
          onNewSession={openNewSessionModal}
          onPortForwards={() => setPortForwardsOpen(true)}
          onSchedules={() => setSchedulesOpen(true)}
          events={allToolEvents}
          connected={connected === true}
          onJumpToSession={() => {}}
          onDismiss={dismissEvent}
          onDismissAll={dismissAllEvents}
          panesCount={paneTree ? getLeaves(paneTree).length : 0}
          onSplitPane={() => { openNewSessionModal() }}
          glance={glance}
        />
      )}
      <div className="flex-1 flex overflow-hidden">
        {!terminalFullscreen && (
          <Sidebar
            sessions={Array.from(state.catalog.sessionsByRef.values()).map(s => ({
              id: s.id,
              name: s._compat?.name || s.id,
              host: s.owner,
              windows: [],
              created: s.created_at,
              attached: true,
              last_activity: new Date().toISOString(),
            } as Session))}
            selectedSession={activeKey}
            collapsed={sidebarCollapsed}
            selfUpdateAvailable={selfUpdate.status?.update_available ?? false}
            collapseMode={(prefs.sidebar.collapse_mode || 'small') as 'small' | 'hidden'}
            width={sidebarWidth}
            onWidthChange={handleSidebarWidth}
            hasMultipleHosts={hasMultipleHosts}
            localHostId={localHostId}
            hosts={hosts}
            onSessionSelect={handleSessionSelect}
            getSessionEvents={getSessionEvents}
            sessionNeedsAttention={sessionNeedsAttention}
            isSessionInActiveTurn={isSessionInActiveTurn}
            getSessionActivity={getSessionActivity}
            agentCount={0}
            glance={glance}
            onToggleCollapse={() => setSidebarCollapsed(c => !c)}
            layoutGroups={layoutGroups}
            sessionOrderRanks={{}}
            setSessionOrderRank={() => {}}
            onSwitchGroup={() => {}}
            onRenameGroup={() => {}}
            forceAiName={async () => false}
            namingGroupId={null}
            onPairSessions={() => {}}
            onRemoveFromSplit={() => {}}
            onSessionKilled={() => {}}
            sessionAttrs={sessionAttrs}
            setSessionAttr={setSessionAttr}
            pruningSuspended={false}
            onQuickShell={() => {}}
            crashedCount={crashedHook.crashedSessions.length}
            onCrashedClick={() => crashedHook.refresh()}
          />
        )}
        <div className="flex-1 flex flex-col overflow-hidden relative">
          {currentView === 'session' && paneTree ? (
            <TiledView
              tree={paneTree}
              activeKey={activeKey}
              onActivate={(key) => {
                if (layoutId && activeKey !== key) {
                  v2State.workspaceCommand(layoutId, { action: 'select', ref: keyToSessionRef(key) })
                }
                refocusTerminal()
              }}
              onClose={(key) => {
                if (layoutId) {
                  v2State.workspaceCommand(layoutId, { action: 'remove', ref: keyToSessionRef(key) })
                }
              }}
              onPopOut={() => {}}
              onSplit={() => { openNewSessionModal() }}
              onRatioChange={() => {}}
              fullscreen={terminalFullscreen}
              onToggleFullscreen={toggleFullscreen}
              terminalContainerRef={terminalContainerRef}
              onDropSession={() => {}}
              onDropNewSession={() => {}}
              onSwapPanes={() => {}}
              onMovePanes={() => {}}
              getTerminalIdentity={getTerminalIdentity}
              onOpenFile={wiki.openFile}
            />
          ) : (
            <Overview
              sessions={Array.from(state.catalog.sessionsByRef.values()).map(s => ({
                id: s.id,
                name: s._compat?.name || s.id,
                host: s.owner,
                windows: [],
                created: s.created_at,
                attached: true,
                last_activity: new Date().toISOString(),
              } as Session))}
              onOpenFile={wiki.openFile}
              hosts={hosts}
              hiddenSet={sessionAttrs.hidden}
              backgroundSet={sessionAttrs.background}
              scheduleIDs={sessionAttrs.scheduleIDs}
              onSessionSelect={handleSessionSelect}
              getSessionEvents={getSessionEvents}
              getSessionActivity={getSessionActivity}
              isSessionInActiveTurn={isSessionInActiveTurn}
              onJumpToSession={() => {}}
              onDismissAlert={dismissEvent}
              setSessionAttr={setSessionAttr}
              onSessionKilled={() => {}}
              layoutGroups={layoutGroups}
            />
          )}
          <div id="mobile-keybar-slot" className="flex-none" />
          <SettingsDrawer
            open={false}
            onClose={closeSettings}
            pushState={pushState}
            onPushSubscribe={pushSubscribe}
            onPushUnsubscribe={pushUnsubscribe}
            onLogout={onLogout}
            version={serverVersion}
            updateAvailable={updateAvailable}
            binaryUpdate={selfUpdate.status}
            onApplyUpdate={selfUpdate.apply}
            updateApplying={selfUpdate.applying}
            updateRestartMode={selfUpdate.restartMode}
            updateError={selfUpdate.error}
            updateChecking={selfUpdate.checking}
            onCheckUpdate={selfUpdate.checkNow}
          />
        </div>
        {wiki.target && (
          <WikiPanel
            filePath={wiki.target.path}
            openNonce={wiki.target.nonce}
            sessionCwd={wiki.target.cwd}
            hostId={wiki.target.hostId}
            session={wiki.target.session}
            onClose={() => wiki.closePanel()}
          />
        )}
      </div>
    </div>
  )
}

export default function App() {
  const prefsProvider = usePreferencesProvider()
  const { loading, authRequired, needsSetup, authenticated, error: authError, setup, login, logout } = useAuth()
  const [showOnboarding, setShowOnboarding] = useState(false)

  useEffect(() => {
    const syncViewport = () => {
      const viewport = window.visualViewport
      const height = viewport?.height ?? window.innerHeight
      const width = viewport?.width ?? window.innerWidth
      document.documentElement.style.setProperty('--app-height', `${Math.round(height)}px`)
      document.documentElement.style.setProperty('--app-width', `${Math.round(width)}px`)
    }

    syncViewport()
    window.addEventListener('resize', syncViewport)
    window.visualViewport?.addEventListener('resize', syncViewport)
    window.visualViewport?.addEventListener('scroll', syncViewport)

    return () => {
      window.removeEventListener('resize', syncViewport)
      window.visualViewport?.removeEventListener('resize', syncViewport)
      window.visualViewport?.removeEventListener('scroll', syncViewport)
    }
  }, [])

  // Re-fetch preferences after login (initial fetch may have gotten 401)
  useEffect(() => {
    if (authenticated) {
      prefsProvider.refetch()
    }
  }, [authenticated]) // eslint-disable-line react-hooks/exhaustive-deps

  // Apply last-used theme immediately (before auth) so login page is themed
  useEffect(() => {
    try {
      const cached = localStorage.getItem('termyard:theme')
      if (cached) {
        applyTheme(cached)
      }
    } catch {}
  }, [])

  // Apply theme when preferences load or theme changes, and cache for login page
  useEffect(() => {
    if (prefsProvider.loaded) {
      applyTheme(prefsProvider.prefs.theme)
      try {
        localStorage.setItem('termyard:theme', prefsProvider.prefs.theme)
      } catch {}
    }
  }, [prefsProvider.loaded, prefsProvider.prefs.theme])

  if (loading) {
    return <div className="flex items-center justify-center h-full w-full bg-background" />
  }

  if (authRequired && needsSetup) {
    const handleSetup = async (password: string) => {
      const ok = await setup(password)
      if (ok) setShowOnboarding(true)
      return ok
    }
    return <Login mode="setup" error={authError} onSubmit={handleSetup} />
  }

  if (authRequired && !authenticated) {
    return <Login mode="login" error={authError} onSubmit={login} />
  }

  if (authenticated && showOnboarding) {
    return (
      <PreferencesContext.Provider value={prefsProvider}>
        <Setup fullPage onComplete={() => {
          setShowOnboarding(false)
          try { localStorage.setItem('termyard:setup-seen', 'true') } catch {}
        }} />
      </PreferencesContext.Provider>
    )
  }

  const v2Enabled = isV2StateEnabled()

  return (
    <PreferencesContext.Provider value={prefsProvider}>
      {v2Enabled ? (
        <AppV2 onLogout={authRequired ? logout : undefined} authenticated={authRequired ? authenticated : true} />
      ) : (
        <AppLegacy onLogout={authRequired ? logout : undefined} authenticated={authRequired ? authenticated : true} />
      )}
    </PreferencesContext.Provider>
  )
}
