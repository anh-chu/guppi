import { useState, useEffect, useCallback, useRef } from 'react'
import { Sidebar } from './components/Sidebar'
import { Terminal } from './components/Terminal'
import { Overview } from './components/Overview'
import { NewSessionModal } from './components/NewSessionModal'
import { PortForwardModal } from './components/PortForwardModal'
import { TopBar } from './components/TopBar'
import { TiledView } from './components/TiledView'
import { PaneTree, getLeaves, findLeaf, splitLeaf, insertBesideLeaf, removeLeaf, replaceLeaf, updateRatio, popOut, swapLeaves, movePane } from './lib/paneTree'
import { StatusBar } from './components/StatusBar'
import { Settings } from './components/Settings'
import { HelpModal } from './components/HelpModal'
import { QuickSwitcher } from './components/QuickSwitcher'
import { Login } from './components/Login'
import { Setup } from './components/Setup'
import { TrustCertificate } from './components/TrustCertificate'
import { useSessions, Session, sessionKey, parseSessionKey } from './hooks/useSessions'
import { useHosts } from './hooks/useHosts'
import { useToolEvents } from './hooks/useToolEvents'
import { useActivity } from './hooks/useActivity'
import { useNotifications } from './hooks/useNotifications'
import { useWebSocket } from './hooks/useWebSocket'
import { usePushNotifications } from './hooks/usePushNotifications'
import { usePreferencesProvider, usePreferences, PreferencesContext } from './hooks/usePreferences'
import { useAuth } from './hooks/useAuth'
import { applyTheme } from './theme'

type View = 'overview' | 'session' | 'settings' | 'setup'

type LayoutGroup = {
  id: string
  tree: PaneTree
  activeKey: string | null
  name?: string
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

function AppInner({ onLogout }: { onLogout?: () => void }) {
  const { sessions, refresh } = useSessions()
  const { events: allToolEvents, handleEvent: handleToolEvent, getSessionEvents, sessionNeedsAttention, dismissEvent, dismissAll: dismissAllEvents } = useToolEvents()
  const { getSessionActivity, handleActivityEvent } = useActivity()
  const { pushState, subscribe: pushSubscribe, unsubscribe: pushUnsubscribe } = usePushNotifications()
  const { processToolEvent } = useNotifications(pushState === 'subscribed')
  const { hosts, refresh: refreshHosts } = useHosts()
  const [currentView, setCurrentView] = useState<View>(() => getViewFromPath().view)
  const [paneTree, setPaneTree] = useState<PaneTree | null>(() => {
    const urlKey = getViewFromPath().sessionKey
    if (!urlKey) return null
    try {
      const stored = localStorage.getItem('guppi:pane-tree')
      if (stored) {
        return JSON.parse(stored) as PaneTree  // always restore full split
      }
    } catch {}
    return popOut(urlKey)
  })
  const [activeKey, setActiveKey] = useState<string | null>(() => {
    const urlKey = getViewFromPath().sessionKey
    if (!urlKey) return null
    try {
      const stored = localStorage.getItem('guppi:pane-tree')
      const storedActiveKey = localStorage.getItem('guppi:active-key')
      if (stored && storedActiveKey) {
        const tree = JSON.parse(stored) as PaneTree
        if (findLeaf(tree, urlKey) && findLeaf(tree, storedActiveKey)) {
          return storedActiveKey
        }
      }
    } catch {}
    return urlKey
  })
  const [singleView, setSingleView] = useState<string | null>(() => {
    const urlKey = getViewFromPath().sessionKey
    if (!urlKey) return null
    try {
      const stored = localStorage.getItem('guppi:pane-tree')
      if (stored) {
        const tree = JSON.parse(stored) as PaneTree
        // If URL session is NOT in the stored split tree, it was a singleView
        if (!findLeaf(tree, urlKey)) return urlKey
      }
    } catch {}
    return null
  })
  const [savedGroups, setSavedGroups] = useState<LayoutGroup[]>(() => {
    try {
      const stored = localStorage.getItem('guppi:saved-groups')
      if (stored) return JSON.parse(stored)
    } catch {}
    return []
  })
  const [activeGroupId, setActiveGroupId] = useState<string>(() => {
    try {
      const stored = localStorage.getItem('guppi:active-group-id')
      if (stored) return stored
    } catch {}
    return Math.random().toString(36).slice(2)
  })
  const [activeGroupName, setActiveGroupName] = useState<string>(() => {
    try { return localStorage.getItem('guppi:active-group-name') || '' } catch { return '' }
  })
  const [groupOrder, setGroupOrder] = useState<string[]>(() => {
    try {
      const stored = localStorage.getItem('guppi:group-order')
      if (stored) {
        const parsed = JSON.parse(stored)
        if (Array.isArray(parsed) && parsed.length > 0) return parsed
      }
    } catch {}
    // Seed from existing group IDs (migration / first run)
    try {
      const activeId = localStorage.getItem('guppi:active-group-id')
      const savedStr = localStorage.getItem('guppi:saved-groups')
      const saved: LayoutGroup[] = savedStr ? JSON.parse(savedStr) : []
      const ids = [activeId, ...saved.map(g => g.id)].filter(Boolean) as string[]
      return Array.from(new Set(ids))
    } catch {}
    return []
  })
  const selectedSession = singleView ?? activeKey
  const hasMultipleHosts = hosts.length > 1
  const [serverVersion, setServerVersion] = useState<string | null>(null)
  const loadedVersionRef = useRef<string | null>(null)
  const updateAvailable = loadedVersionRef.current !== null && serverVersion !== null && serverVersion !== loadedVersionRef.current
  const [newSessionModalOpen, setNewSessionModalOpen] = useState(false)
  const terminalContainerRef = useRef<HTMLDivElement>(null)
  const [sidebarCollapsed, setSidebarCollapsed] = useState(() => {
    try { return localStorage.getItem('guppi:sidebar-collapsed') === 'true' } catch { return false }
  })
  const [terminalFullscreen, setTerminalFullscreen] = useState(false)
  const [helpOpen, setHelpOpen] = useState(false)
  const [quickSwitcherOpen, setQuickSwitcherOpen] = useState(false)
  const [portForwardsOpen, setPortForwardsOpen] = useState(false)
  const [dragNewSessionOver, setDragNewSessionOver] = useState(false)
  const pendingSessionRef = useRef<string | null>(null)
  const splitTargetRef = useRef<{ key: string; direction: 'h' | 'v'; newFirst?: boolean } | null>(null)
  const activeKeyRef = useRef(activeKey)
  activeKeyRef.current = activeKey
  const { prefs } = usePreferences()

  // Auto-lock: idle detection + optional background accelerator
  const lastActivityRef = useRef<number>(Date.now())
  useEffect(() => {
    if (!onLogout || !prefs.lock_timeout_minutes) return

    const idleMs = prefs.lock_timeout_minutes * 60 * 1000
    const bgMs = prefs.lock_background_faster && prefs.lock_background_minutes
      ? prefs.lock_background_minutes * 60 * 1000
      : idleMs

    // Track user activity
    const onActivity = () => { lastActivityRef.current = Date.now() }
    const events = ['keydown', 'click', 'scroll', 'touchstart', 'mousemove'] as const
    const opts: AddEventListenerOptions = { passive: true, capture: true }
    events.forEach(e => document.addEventListener(e, onActivity, opts))

    // Check idle on an interval
    const checkInterval = setInterval(() => {
      const elapsed = Date.now() - lastActivityRef.current
      const timeout = document.hidden ? bgMs : idleMs
      if (elapsed >= timeout) {
        onLogout()
      }
    }, 30_000)

    // Also check immediately when returning from background
    const onVisibilityChange = () => {
      if (!document.hidden) {
        const elapsed = Date.now() - lastActivityRef.current
        if (elapsed >= bgMs) {
          onLogout()
        }
      }
    }
    document.addEventListener('visibilitychange', onVisibilityChange)

    return () => {
      events.forEach(e => document.removeEventListener(e, onActivity, opts as EventListenerOptions))
      clearInterval(checkInterval)
      document.removeEventListener('visibilitychange', onVisibilityChange)
    }
  }, [onLogout, prefs.lock_timeout_minutes, prefs.lock_background_faster, prefs.lock_background_minutes])

  // Persist sidebar state
  useEffect(() => {
    localStorage.setItem('guppi:sidebar-collapsed', String(sidebarCollapsed))
  }, [sidebarCollapsed])

  // Persist pane tree across reloads
  useEffect(() => {
    try {
      if (paneTree) {
        localStorage.setItem('guppi:pane-tree', JSON.stringify(paneTree))
        localStorage.setItem('guppi:active-key', activeKey || '')
      } else {
        localStorage.removeItem('guppi:pane-tree')
        localStorage.removeItem('guppi:active-key')
      }
    } catch {}
  }, [paneTree, activeKey])

  // Persist saved groups across reloads
  useEffect(() => {
    try {
      localStorage.setItem('guppi:saved-groups', JSON.stringify(savedGroups))
      localStorage.setItem('guppi:active-group-id', activeGroupId)
      localStorage.setItem('guppi:active-group-name', activeGroupName)
      localStorage.setItem('guppi:group-order', JSON.stringify(groupOrder))
    } catch {}
  }, [savedGroups, activeGroupId, activeGroupName, groupOrder])

  // Sync URL -> state on popstate (back/forward)
  useEffect(() => {
    const onPopState = () => {
      const { view, sessionKey } = getViewFromPath()
      setCurrentView(view)
      if (sessionKey) {
        setPaneTree(prev => {
          if (prev && findLeaf(prev, sessionKey)) { setActiveKey(sessionKey); setSingleView(null); return prev }
          setSingleView(sessionKey); return prev
        })
      } else {
        setSingleView(null)
        // paneTree and activeKey untouched — split persists in background
      }
    }
    window.addEventListener('popstate', onPopState)
    return () => window.removeEventListener('popstate', onPopState)
  }, [])

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
    setCurrentView(view || (sessKey ? 'session' : 'overview'))
    if (!sessKey) {
      setSingleView(null)
      // paneTree and activeKey intentionally untouched — split persists
      return
    }
    // sessKey path: kept for safety but selectSession() is preferred
    setSingleView(sessKey)
  }, [])

  const handleDropSession = useCallback((sessKey: string, targetKey: string, edge: 'left'|'right'|'top'|'bottom'|'center') => {
    setSingleView(null)
    const currentActive = activeKeyRef.current
    setPaneTree(prev => {
      if (prev === null) return popOut(sessKey)
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
  }, [])

  const closePane = useCallback((sessKey: string) => {
    setPaneTree(prev => {
      if (prev === null) return null
      const newTree = removeLeaf(prev, sessKey)
      if (newTree === null) {
        setActiveKey(null)
        return null
      }
      // If the closed pane was the active one, pick the first leaf
      if (sessKey === activeKeyRef.current) {
        const leaves = getLeaves(newTree)
        setActiveKey(leaves[0] || null)
      }
      return newTree
    })
  }, [])

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
    closePane(sessKey)
    try {
      await fetch('/api/session/kill', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ id: session.id }),
      })
    } catch (err) {
      console.error('Failed to kill session:', err)
    }
  }, [closePane])

  // Navigate back to overview when the tree becomes empty (but not if singleView is active)
  useEffect(() => {
    if (paneTree === null && !singleView && currentView === 'session') {
      // Remove empty group from order
      const newOrder = groupOrder.filter(id => id !== activeGroupId)
      setGroupOrder(newOrder)
      if (savedGroups.length > 0) {
        // Pick next in stable order
        const nextId = newOrder.find(id => savedGroups.some(g => g.id === id)) ?? savedGroups[0].id
        const next = savedGroups.find(g => g.id === nextId)!
        setSavedGroups(prev => prev.filter(g => g.id !== nextId))
        setPaneTree(next.tree)
        setActiveKey(next.activeKey)
        setActiveGroupId(nextId)
        setActiveGroupName(next.name ?? '')
        const focusKey = next.activeKey ?? getLeaves(next.tree)[0] ?? null
        if (focusKey) {
          const { host, name } = parseSessionKey(focusKey)
          const path = host
            ? `/session/${encodeURIComponent(host)}/${encodeURIComponent(name)}`
            : `/session/${encodeURIComponent(name)}`
          if (window.location.pathname !== path) window.history.pushState(null, '', path)
        }
      } else {
        navigateTo(null)
      }
    }
  }, [paneTree, singleView, currentView, savedGroups, groupOrder, activeGroupId, navigateTo])

  // Dissolve active group to standalone when only 1 session remains
  useEffect(() => {
    if (!paneTree) return
    const leaves = getLeaves(paneTree)
    if (leaves.length !== 1) return
    if (splitTargetRef.current) return // split pending — don't dissolve yet
    const lastLeaf = leaves[0]
    setSingleView(lastLeaf)
    setActiveKey(null)
    setPaneTree(null)
    setGroupOrder(prev => prev.filter(id => id !== activeGroupId))
    const { host, name } = parseSessionKey(lastLeaf)
    const path = host
      ? `/session/${encodeURIComponent(host)}/${encodeURIComponent(name)}`
      : `/session/${encodeURIComponent(name)}`
    if (window.location.pathname !== path) window.history.replaceState(null, '', path)
  }, [paneTree, activeGroupId])

  // Refs for latest values used in keyboard shortcuts (avoids effect churn)

  const sessionsRef = useRef(sessions)
  sessionsRef.current = sessions
  const selectedSessionRef = useRef(selectedSession)
  selectedSessionRef.current = selectedSession
  const savedGroupsRef = useRef(savedGroups)
  savedGroupsRef.current = savedGroups
  const setActiveKeyRef = useRef(setActiveKey)
  setActiveKeyRef.current = setActiveKey
  const switchToGroupRef = useRef<((id: string) => void) | null>(null)

  const openNewSessionModal = useCallback(() => {
    setNewSessionModalOpen(true)
  }, [])

  // Global keyboard shortcuts
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      const mod = e.metaKey || e.ctrlKey

      // Help: Cmd/Ctrl + ? or Cmd/Ctrl + / (Linux Ctrl+Shift+/ often doesn't produce '?')
      if (mod && (e.key === '?' || e.key === '/' || (e.shiftKey && e.code === 'Slash'))) {
        e.preventDefault()
        setHelpOpen(prev => !prev)
        return
      }

      // Toggle sidebar: Cmd/Ctrl + \
      if (mod && e.key === '\\') {
        e.preventDefault()
        setSidebarCollapsed(c => !c)
        return
      }

      // Settings: Cmd/Ctrl + ,
      if (mod && e.key === ',') {
        e.preventDefault()
        navigateTo(null, 'settings')
        return
      }

      // Split pane: Cmd/Ctrl + Shift + \
      if (mod && e.shiftKey && e.key === '\\') {
        e.preventDefault()
        if (activeKey !== null) {
          splitTargetRef.current = { key: activeKey, direction: 'h' }
          openNewSessionModal()
        }
        return
      }

      // Quick Switcher: Cmd/Ctrl + K
      if (mod && e.key === 'k') {
        e.preventDefault()
        setQuickSwitcherOpen(true)
        return
      }

      // Overview: Cmd/Ctrl + Shift + O
      if (mod && e.shiftKey && (e.key === 'O' || e.key === 'o')) {
        e.preventDefault()
        navigateTo(null)
        return
      }

      const cycleTo = (targetKey: string) => {
        // If target belongs to a saved group, switch to that group first
        const group = savedGroupsRef.current.find(g => findLeaf(g.tree, targetKey))
        if (group) {
          switchToGroupRef.current?.(group.id)
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

      // Cycle sessions: Cmd/Ctrl + Shift + ]
      if (mod && e.shiftKey && (e.key === ']' || e.code === 'BracketRight')) {
        e.preventDefault()
        const els = document.querySelectorAll('[data-session-key]')
        const skeys: string[] = []
        els.forEach(el => skeys.push(el.getAttribute('data-session-key')!))
        if (skeys.length > 0) {
          const current = selectedSessionRef.current
          const idx = current ? skeys.indexOf(current) : -1
          const nextIdx = idx >= 0 ? (idx + 1) % skeys.length : 0
          cycleTo(skeys[nextIdx])
        }
        return
      }

      // Cycle sessions: Cmd/Ctrl + Shift + [
      if (mod && e.shiftKey && (e.key === '[' || e.code === 'BracketLeft')) {
        e.preventDefault()
        const els = document.querySelectorAll('[data-session-key]')
        const skeys: string[] = []
        els.forEach(el => skeys.push(el.getAttribute('data-session-key')!))
        if (skeys.length > 0) {
          const current = selectedSessionRef.current
          const idx = current ? skeys.indexOf(current) : -1
          const prevIdx = idx > 0 ? idx - 1 : skeys.length - 1
          cycleTo(skeys[prevIdx])
        }
        return
      }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [navigateTo, activeKey, openNewSessionModal])

  // Listen for state events via WebSocket
  const onEvent = useCallback((evt: any) => {
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
      return
    }
    if (evt.type === 'activity') {
      handleActivityEvent(evt.snapshots || [])
      return
    }
    if (['session-added', 'session-removed', 'sessions-changed'].includes(evt.type)) {
      refresh()
    }
    if (['peer-connected', 'peer-disconnected'].includes(evt.type)) {
      refresh()
      refreshHosts()
    }
  }, [refresh, refreshHosts, handleToolEvent, processToolEvent, handleActivityEvent])

  const { connected } = useWebSocket('/ws/events', onEvent)

  // If a pane's session was removed, remove that pane
  // (don't bounce if we're waiting for a newly created session to appear)
  useEffect(() => {
    if (sessions.length === 0 || paneTree === null) return
    const validKeys = new Set(sessions.map(s => sessionKey(s)))
    const keysToRemove = getLeaves(paneTree).filter(
      k => k !== pendingSessionRef.current && !validKeys.has(k),
    )
    if (keysToRemove.length === 0) return
    setPaneTree(prev => {
      if (prev === null) return null
      let tree: PaneTree | null = prev
      for (const key of keysToRemove) {
        if (tree === null) break
        tree = removeLeaf(tree, key)
      }
      return tree
    })
    if (singleView && !validKeys.has(singleView)) setSingleView(null)
  }, [sessions, paneTree, singleView]) // eslint-disable-line react-hooks/exhaustive-deps

  // Prune saved groups from removed sessions
  useEffect(() => {
    if (sessions.length === 0) return
    const validKeys = new Set(sessions.map(s => sessionKey(s)))
    setSavedGroups(prev =>
      prev.map(group => {
        const keysToRemove = getLeaves(group.tree).filter(k => !validKeys.has(k))
        if (keysToRemove.length === 0) return group
        let tree: PaneTree | null = group.tree
        for (const key of keysToRemove) {
          if (tree) tree = removeLeaf(tree, key)
        }
        if (!tree) return null
        if (getLeaves(tree).length === 1) return null // dissolve to standalone
        const newActiveKey = group.activeKey && validKeys.has(group.activeKey)
          ? group.activeKey : getLeaves(tree)[0] ?? null
        return { ...group, tree, activeKey: newActiveKey }
      }).filter(Boolean) as LayoutGroup[]
    )
  }, [sessions])

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
    setCurrentView('session')
    if (paneTree && findLeaf(paneTree, sk)) {
      setSingleView(null)
      setActiveKey(sk)
    } else {
      setSingleView(sk)
    }
    // Refocus even when activeKey didn't change — Terminal auto-focus
    // on inactive panes may have stolen visual focus from the intended one.
    setTimeout(refocusTerminal, 150)
  }, [paneTree, refocusTerminal])
  selectSessionRef.current = selectSession

  const handleSessionSelect = (session: Session) => {
    selectSession(sessionKey(session))
  }

  const handlePairSessions = useCallback((draggedKey: string, targetKey: string) => {
    setSingleView(null)
    const inCurrentTree = paneTree && (findLeaf(paneTree, draggedKey) || findLeaf(paneTree, targetKey))
    if (!inCurrentTree) {
      // Neither session is in the active group — create a new background group
      const newId = Math.random().toString(36).slice(2)
      const newTree: PaneTree = { type: 'split', direction: 'h', ratio: 0.5,
        first: { type: 'leaf', sessionKey: draggedKey },
        second: { type: 'leaf', sessionKey: targetKey } }
      // Save current group if it has a tree
      if (paneTree) {
        setSavedGroups(prev => [...prev, { id: activeGroupId, tree: paneTree, activeKey, name: activeGroupName || undefined }])
      }
      setPaneTree(newTree)
      setActiveKey(draggedKey)
      setActiveGroupId(newId)
      setActiveGroupName('')
      setGroupOrder(prev => {
        // ensure current activeGroupId is in order, then append new group
        const withCurrent = prev.includes(activeGroupId) ? prev : [...prev, activeGroupId]
        return [...withCurrent, newId]
      })
      setSingleView(null)
      const { host, name } = parseSessionKey(draggedKey)
      const path = host ? `/session/${encodeURIComponent(host)}/${encodeURIComponent(name)}` : `/session/${encodeURIComponent(name)}`
      if (window.location.pathname !== path) window.history.pushState(null, '', path)
      setCurrentView('session')
      return
    }
    // Existing behavior: add to current group's tree
    setPaneTree(prev => {
      if (prev && findLeaf(prev, draggedKey) && findLeaf(prev, targetKey)) return prev
      if (prev && findLeaf(prev, targetKey)) return splitLeaf(prev, targetKey, 'h', draggedKey)
      if (prev && findLeaf(prev, draggedKey)) return splitLeaf(prev, draggedKey, 'h', targetKey)
      return { type: 'split', direction: 'h', ratio: 0.5,
        first: { type: 'leaf', sessionKey: draggedKey },
        second: { type: 'leaf', sessionKey: targetKey } }
    })
    setActiveKey(draggedKey)
    const { host, name } = parseSessionKey(draggedKey)
    const path = host ? `/session/${encodeURIComponent(host)}/${encodeURIComponent(name)}` : `/session/${encodeURIComponent(name)}`
    if (window.location.pathname !== path) window.history.pushState(null, '', path)
    setCurrentView('session')
  }, [paneTree, activeKey, activeGroupId, groupOrder])

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
    const targetGroup = savedGroups.find(g => g.id === groupId)
    if (!targetGroup) return
    // Save current active group if it has a tree
    if (paneTree) {
      setSavedGroups(prev => [
        ...prev.filter(g => g.id !== groupId),
        { id: activeGroupId, tree: paneTree, activeKey, name: activeGroupName || undefined }
      ])
    } else {
      setSavedGroups(prev => prev.filter(g => g.id !== groupId))
    }
    const targetKey = (focusKey && findLeaf(targetGroup.tree, focusKey))
      ? focusKey
      : (targetGroup.activeKey ?? getLeaves(targetGroup.tree)[0] ?? null)
    setPaneTree(targetGroup.tree)
    setActiveKey(targetKey)
    setActiveGroupId(groupId)
    setActiveGroupName(targetGroup.name ?? '')
    setSingleView(null)
    setCurrentView('session')
    // Navigate URL to the target leaf
    if (targetKey) {
      const { host, name } = parseSessionKey(targetKey)
      const path = host
        ? `/session/${encodeURIComponent(host)}/${encodeURIComponent(name)}`
        : `/session/${encodeURIComponent(name)}`
      if (window.location.pathname !== path) window.history.pushState(null, '', path)
    }
    setTimeout(refocusTerminal, 150)
  }, [savedGroups, activeGroupId, activeGroupName, paneTree, activeKey, refocusTerminal])
  switchToGroupRef.current = switchToGroup

  const renameGroup = useCallback((groupId: string, name: string) => {
    if (groupId === activeGroupId) {
      setActiveGroupName(name)
    } else {
      setSavedGroups(prev => prev.map(g => g.id === groupId ? { ...g, name: name || undefined } : g))
    }
  }, [activeGroupId])

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

  const navigateToSettings = useCallback(() => {
    navigateTo(null, 'settings')
  }, [navigateTo])

  const handleCreateSession = useCallback(async (name: string, path: string, command: string, hostId?: string, worktreeBranch?: string, agentType?: string): Promise<string | null> => {
    // For worktree sessions keep the modal open until we confirm success.
    if (!worktreeBranch) setNewSessionModalOpen(false)
    try {
      const res = await fetch('/api/session/new', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name, path, command, host: hostId || undefined, agent_type: agentType || undefined, worktree_branch: worktreeBranch || undefined }),
      })
      if (!res.ok) {
        if (worktreeBranch) {
          const msg = await res.text().catch(() => 'Failed to create worktree')
          return msg
        }
        return null
      }
      if (worktreeBranch) setNewSessionModalOpen(false)
      {
        const payload = await res.json().catch(() => null)
        const resolvedName = payload?.name || name
        const sessKey = hostId ? `${hostId}/${resolvedName}` : resolvedName
        pendingSessionRef.current = sessKey
        const target = splitTargetRef.current
        splitTargetRef.current = null
        if (target) {
          setPaneTree(prev => {
            if (prev === null) return popOut(sessKey)
            if (findLeaf(prev, target.key)) {
              return target.newFirst
                ? insertBesideLeaf(prev, target.key, target.direction, sessKey, true)
                : splitLeaf(prev, target.key, target.direction, sessKey)
            }
            return prev
          })
          setActiveKey(sessKey)
          await refresh()
          pendingSessionRef.current = null
          setTimeout(() => refocusTerminal(), 300)
        } else {
          selectSession(sessKey)
          await refresh()
          pendingSessionRef.current = null
          setTimeout(() => refocusTerminal(), 300)
        }
      }
    } catch (err) {
      console.error('Failed to create session:', err)
      pendingSessionRef.current = null
    }
    return null
  }, [selectSession, refresh, refocusTerminal])

  const handleDropNewSession = useCallback((targetKey: string, edge: 'left'|'right'|'top'|'bottom'|'center') => {
    let key = targetKey || activeKey

    // Dropping onto a singleView session (standalone, not in any group):
    // save current group to background and start a new group from singleView
    if (!targetKey && singleView) {
      key = singleView
      const newGroupId = Math.random().toString(36).slice(2)
      if (paneTree) {
        setSavedGroups(prev => [...prev, { id: activeGroupId, tree: paneTree, activeKey, name: activeGroupName || undefined }])
      }
      setGroupOrder(prev => {
        const withCurrent = paneTree && !prev.includes(activeGroupId) ? [...prev, activeGroupId] : prev
        return [...withCurrent, newGroupId]
      })
      setPaneTree(popOut(singleView))
      setActiveKey(singleView)
      setActiveGroupId(newGroupId)
      setActiveGroupName('')
      setSingleView(null)
    }

    if (key) {
      const direction: 'h' | 'v' = (edge === 'top' || edge === 'bottom') ? 'v' : 'h'
      const newFirst = edge === 'left' || edge === 'top'
      splitTargetRef.current = { key, direction, newFirst }
    }
    const { host } = key ? parseSessionKey(key) : { host: undefined }
    handleCreateSession('shell', '~', '', host || undefined)
  }, [singleView, activeKey, activeGroupId, paneTree, handleCreateSession])

  const toggleFullscreen = useCallback(() => {
    setTerminalFullscreen(f => !f)
  }, [])

  // Keep the browser title stable unless user attention is needed.
  useEffect(() => {
    const needsAttention = allToolEvents.some(
      evt => evt.status === 'waiting' || evt.status === 'error',
    )
    document.title = needsAttention ? 'Guppi - Attention needed' : 'Guppi'
  }, [allToolEvents])

  // Exit fullscreen when navigating away from terminal
  useEffect(() => {
    if (currentView !== 'session') {
      setTerminalFullscreen(false)
    }
  }, [currentView])

  const showingTerminal = currentView === 'session' && !!selectedSession

  return (
    <div className="flex flex-col h-full w-full bg-background text-foreground relative">
      {helpOpen && <HelpModal onClose={() => setHelpOpen(false)} />}
      {portForwardsOpen && (
        <PortForwardModal onClose={() => setPortForwardsOpen(false)} />
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
          sidebarCollapsed={sidebarCollapsed}
          onToggleCollapse={() => setSidebarCollapsed(c => !c)}
          onOverview={() => navigateTo(null)}
          onSettings={navigateToSettings}
          onNewSession={openNewSessionModal}
          onPortForwards={() => setPortForwardsOpen(true)}
          events={allToolEvents}
          connected={connected}
          onJumpToSession={jumpToSession}
          onDismiss={dismissEvent}
          onDismissAll={dismissAllEvents}
          panesCount={paneTree ? getLeaves(paneTree).length : 0}
          onSplitPane={() => {
            if (activeKey !== null) {
              splitTargetRef.current = { key: activeKey, direction: 'h' }
            }
            openNewSessionModal()
          }}
        />
      )}
      {/* Middle: Sidebar + Content */}
      <div className="flex-1 flex overflow-hidden">
        {!terminalFullscreen && (
          <Sidebar
            sessions={sessions}
            selectedSession={selectedSession}
            collapsed={sidebarCollapsed}
            collapseMode={(prefs.sidebar.collapse_mode || 'small') as 'small' | 'hidden'}
            hasMultipleHosts={hasMultipleHosts}
            onSessionSelect={handleSessionSelect}
            onSessionRenamed={(oldKey, newKey) => {
              setPaneTree(prev => {
                if (prev === null) return null
                return replaceLeaf(prev, oldKey, newKey)
              })
              if (activeKey === oldKey) {
                setActiveKey(newKey)
                const { host, name } = parseSessionKey(newKey)
                const path = host
                  ? `/session/${encodeURIComponent(host)}/${encodeURIComponent(name)}`
                  : `/session/${encodeURIComponent(name)}`
                window.history.replaceState(null, '', path)
              }
            }}
            getSessionEvents={getSessionEvents}
            sessionNeedsAttention={sessionNeedsAttention}
            getSessionActivity={getSessionActivity}
            layoutGroups={groupOrder
              .map(id => {
                if (id === activeGroupId && paneTree)
                  return { id, leaves: getLeaves(paneTree), isActive: currentView === 'session' && singleView === null, activeKey, name: activeGroupName || undefined }
                const g = savedGroups.find(g => g.id === id)
                if (g) return { id, leaves: getLeaves(g.tree), isActive: false, activeKey: g.activeKey, name: g.name }
                return null
              })
              .filter((g): g is { id: string; leaves: string[]; isActive: boolean; activeKey: string | null; name: string | undefined } => g !== null)
            }
            onSwitchGroup={switchToGroup}
            onRenameGroup={renameGroup}
            onPairSessions={handlePairSessions}
            onRemoveFromSplit={closePane}
          />
        )}
        <div
          className="flex-1 flex flex-col overflow-hidden relative"
          onDragOver={(e) => {
            if (e.dataTransfer.types.includes('application/x-guppi-new-session')) {
              e.preventDefault()
              e.dataTransfer.dropEffect = 'copy'
              setDragNewSessionOver(true)
            }
          }}
          onDragLeave={(e) => {
            if (!e.currentTarget.contains(e.relatedTarget as Node)) {
              setDragNewSessionOver(false)
            }
          }}
        >
          {dragNewSessionOver && (
            <div
              className="absolute inset-0 z-50 flex flex-col items-center justify-center gap-3"
              style={{ background: 'color-mix(in oklch, var(--primary) 8%, var(--canvas))', border: '2px dashed color-mix(in oklch, var(--primary) 40%, transparent)' }}
              onDragOver={(e) => {
                e.preventDefault()
                e.dataTransfer.dropEffect = 'copy'
              }}
              onDragLeave={(e) => {
                if (!e.currentTarget.contains(e.relatedTarget as Node)) {
                  setDragNewSessionOver(false)
                }
              }}
              onDrop={(e) => {
                e.preventDefault()
                setDragNewSessionOver(false)
                handleDropNewSession('', 'center')
              }}
            >
              <svg width="28" height="28" viewBox="0 0 24 24" fill="none" stroke="var(--primary)" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" style={{ opacity: 0.8 }}>
                <rect x="3" y="3" width="7" height="18" rx="1" />
                <rect x="14" y="3" width="7" height="18" rx="1" />
              </svg>
              <span className="text-[11px] font-bold tracking-widest uppercase" style={{ color: 'var(--primary)', opacity: 0.9 }}>Drop to split</span>
            </div>
          )}
          {currentView === 'setup' ? (
            <Setup onComplete={() => navigateTo(null)} />
          ) : currentView === 'settings' ? (
            <Settings pushState={pushState} onPushSubscribe={pushSubscribe} onPushUnsubscribe={pushUnsubscribe} onLogout={onLogout} />
          ) : currentView === 'session' && singleView ? (
            <div ref={terminalContainerRef} className="flex-1 flex flex-col overflow-hidden">
              <Terminal
                sessionName={parseSessionKey(singleView).name}
                hostId={parseSessionKey(singleView).host || undefined}
                fullscreen={terminalFullscreen}
                onToggleFullscreen={toggleFullscreen}
              />
            </div>
          ) : currentView === 'session' && paneTree ? (
            <TiledView
              tree={paneTree}
              activeKey={activeKey}
              onActivate={(key) => { setActiveKey(key); refocusTerminal() }}
              onClose={closePane}
              onKill={killPane}
              onPopOut={popOutPane}
              onSplit={(key, direction) => {
                splitTargetRef.current = { key, direction }
                openNewSessionModal()
              }}
              onRatioChange={(path, ratio) => {
                setPaneTree(prev => {
                  if (prev === null) return null
                  return updateRatio(prev, path, ratio)
                })
              }}
              fullscreen={terminalFullscreen}
              onToggleFullscreen={toggleFullscreen}
              terminalContainerRef={terminalContainerRef}
              onDropSession={handleDropSession}
              onDropNewSession={handleDropNewSession}
              onSwapPanes={(a, b) => setPaneTree(prev => prev ? swapLeaves(prev, a, b) : prev)}
              onMovePanes={(sourceKey, targetKey, edge) =>
                setPaneTree(prev => prev ? movePane(prev, sourceKey, targetKey, edge) : prev)
              }
            />
          ) : (
            <Overview
              sessions={sessions}
              hosts={hosts}
              onSessionSelect={handleSessionSelect}
              getSessionEvents={getSessionEvents}
              getSessionActivity={getSessionActivity}
              pendingAlerts={allToolEvents.filter(e => e.status === 'waiting' || e.status === 'error')}
              onJumpToSession={jumpToSession}
              onDismissAlert={dismissEvent}
            />
          )}
        </div>
      </div>
      {/* StatusBar - full width */}
      <StatusBar
        sessionCount={sessions.length}
        connected={connected}
        activeSession={selectedSession ? sessions.find(s => sessionKey(s) === selectedSession) ?? null : null}
        waitingCount={allToolEvents.filter(e => e.status === 'waiting').length}
        pushState={pushState}
        version={serverVersion}
        updateAvailable={updateAvailable}
        hosts={hosts}
        agentCount={allToolEvents.filter(e => e.auto_detected || e.status === 'waiting' || e.status === 'error').length}
        onHelp={() => setHelpOpen(true)}
      />
    </div>
  )
}

export default function App() {
  const prefsProvider = usePreferencesProvider()
  const { loading, authRequired, needsSetup, authenticated, error: authError, setup, login, logout } = useAuth()
  const [showTrust, setShowTrust] = useState(() => window.location.pathname === '/trust')
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
      const cached = localStorage.getItem('guppi:theme')
      const cachedCustom = localStorage.getItem('guppi:custom-theme')
      if (cached) {
        applyTheme(cached, cachedCustom ? JSON.parse(cachedCustom) : undefined)
      }
    } catch {}
  }, [])

  // Apply theme when preferences load or theme/customizations change, and cache for login page
  useEffect(() => {
    if (prefsProvider.loaded) {
      applyTheme(prefsProvider.prefs.theme, prefsProvider.prefs.custom_theme)
      try {
        localStorage.setItem('guppi:theme', prefsProvider.prefs.theme)
        localStorage.setItem('guppi:custom-theme', JSON.stringify(prefsProvider.prefs.custom_theme || {}))
      } catch {}
    }
  }, [prefsProvider.loaded, prefsProvider.prefs.theme, prefsProvider.prefs.custom_theme])

  if (loading) {
    return <div className="flex items-center justify-center h-full w-full bg-background" />
  }

  if (showTrust || window.location.pathname === '/trust') {
    return <TrustCertificate onBack={() => { setShowTrust(false); window.history.pushState(null, '', '/') }} />
  }

  if (authRequired && needsSetup) {
    const handleSetup = async (password: string) => {
      const ok = await setup(password)
      if (ok) setShowOnboarding(true)
      return ok
    }
    return <Login mode="setup" error={authError} onSubmit={handleSetup} onTrustCert={() => { setShowTrust(true); window.history.pushState(null, '', '/trust') }} />
  }

  if (authRequired && !authenticated) {
    return <Login mode="login" error={authError} onSubmit={login} onTrustCert={() => { setShowTrust(true); window.history.pushState(null, '', '/trust') }} />
  }

  if (authenticated && showOnboarding) {
    return (
      <PreferencesContext.Provider value={prefsProvider}>
        <Setup fullPage onComplete={() => {
          setShowOnboarding(false)
          try { localStorage.setItem('guppi:setup-seen', 'true') } catch {}
        }} />
      </PreferencesContext.Provider>
    )
  }

  return (
    <PreferencesContext.Provider value={prefsProvider}>
      <AppInner onLogout={authRequired ? logout : undefined} />
    </PreferencesContext.Provider>
  )
}
