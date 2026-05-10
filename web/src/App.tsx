import { useState, useEffect, useCallback, useRef } from 'react'
import { Sidebar } from './components/Sidebar'
import { Terminal } from './components/Terminal'
import { Overview } from './components/Overview'
import { QuickSwitcher } from './components/QuickSwitcher'
import { NewSessionModal } from './components/NewSessionModal'
import { TopBar } from './components/TopBar'
import { TiledView } from './components/TiledView'
import { PaneTree, getLeaves, findLeaf, splitLeaf, removeLeaf, replaceLeaf, updateRatio, popOut, swapLeaves, movePane } from './lib/paneTree'
import { StatusBar } from './components/StatusBar'
import { Settings } from './components/Settings'
import { HelpModal } from './components/HelpModal'
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
  const selectedSession = singleView ?? activeKey
  const hasMultipleHosts = hosts.length > 1
  const [serverVersion, setServerVersion] = useState<string | null>(null)
  const loadedVersionRef = useRef<string | null>(null)
  const updateAvailable = loadedVersionRef.current !== null && serverVersion !== null && serverVersion !== loadedVersionRef.current
  const [quickSwitcherOpen, setQuickSwitcherOpen] = useState(false)
  const [newSessionModalOpen, setNewSessionModalOpen] = useState(false)
  const terminalContainerRef = useRef<HTMLDivElement>(null)
  const [sidebarCollapsed, setSidebarCollapsed] = useState(() => {
    try { return localStorage.getItem('guppi:sidebar-collapsed') === 'true' } catch { return false }
  })
  const [terminalFullscreen, setTerminalFullscreen] = useState(false)
  const [helpOpen, setHelpOpen] = useState(false)
  const pendingSessionRef = useRef<string | null>(null)
  const splitTargetRef = useRef<{ key: string; direction: 'h' | 'v' } | null>(null)
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
      if (paneTree && currentView === 'session') {
        localStorage.setItem('guppi:pane-tree', JSON.stringify(paneTree))
        localStorage.setItem('guppi:active-key', activeKey || '')
      } else {
        localStorage.removeItem('guppi:pane-tree')
        localStorage.removeItem('guppi:active-key')
      }
    } catch {}
  }, [paneTree, activeKey, currentView])

  // Sync URL -> state on popstate (back/forward)
  useEffect(() => {
    const onPopState = () => {
      const { view, sessionKey } = getViewFromPath()
      setCurrentView(view)
      if (sessionKey) {
        setPaneTree(popOut(sessionKey))
        setActiveKey(sessionKey)
      } else {
        setPaneTree(null)
        setActiveKey(null)
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
      setPaneTree(null)
      setActiveKey(null)
      return
    }
    setSingleView(null)
    setPaneTree(popOut(sessKey))
    setActiveKey(sessKey)
  }, [])

  const handleDropSession = useCallback((sessKey: string) => {
    setSingleView(null)
    const currentActive = activeKeyRef.current
    setPaneTree(prev => {
      if (prev === null) return popOut(sessKey)
      if (currentActive !== null && findLeaf(prev, currentActive)) {
        return splitLeaf(prev, currentActive, 'h', sessKey)
      }
      // No active key – split the first leaf
      const leaves = getLeaves(prev)
      if (leaves.length > 0) {
        return splitLeaf(prev, leaves[0], 'h', sessKey)
      }
      return popOut(sessKey)
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

  // Navigate back to overview when the tree becomes empty
  useEffect(() => {
    if (paneTree === null && currentView === 'session') {
      navigateTo(null)
    }
  }, [paneTree, currentView, navigateTo])

  const openNewSessionModal = useCallback(() => {
    setQuickSwitcherOpen(false)
    setNewSessionModalOpen(true)
  }, [])

  // Global keyboard shortcuts
  useEffect(() => {
    const shortcut = prefs.quick_switcher_shortcut || 'ctrl+k'
    const onKeyDown = (e: KeyboardEvent) => {
      const mod = e.metaKey || e.ctrlKey

      // Quick switcher
      if (mod) {
        let match = false
        if (shortcut === 'ctrl+k' && e.key === 'k') match = true
        if (shortcut === 'ctrl+p' && e.key === 'p') match = true
        if (shortcut === 'ctrl+space' && e.key === ' ') match = true
        if (match) {
          e.preventDefault()
          setQuickSwitcherOpen(prev => !prev)
          return
        }
      }

      // Help: Cmd/Ctrl + ? or Cmd/Ctrl + / (Linux Ctrl+Shift+/ often doesn't produce '?')
      if (mod && (e.key === '?' || e.key === '/' || (e.shiftKey && e.code === 'Slash'))) {
        e.preventDefault()
        setHelpOpen(prev => !prev)
        return
      }

      // Overview: Cmd/Ctrl + H
      if (mod && e.key === 'h' && !e.shiftKey) {
        e.preventDefault()
        navigateTo(null)
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

      // Lock / Sign out: Cmd/Ctrl + L
      if (mod && e.key === 'l' && !e.shiftKey && onLogout) {
        e.preventDefault()
        onLogout()
        return
      }

      // Jump to next alert: Cmd/Ctrl + J
      if (mod && e.key === 'j' && !e.shiftKey) {
        e.preventDefault()
        const pending = allToolEvents.filter(ev => ev.status === 'waiting' || ev.status === 'error')
        if (pending.length === 0) return
        const currentIdx = selectedSession
          ? pending.findIndex(ev => (ev.host ? `${ev.host}/${ev.session}` : ev.session) === selectedSession)
          : -1
        const next = pending[(currentIdx + 1) % pending.length]
        const sessKey = next.host ? `${next.host}/${next.session}` : next.session
        navigateTo(sessKey, 'session')
        if (next.window !== undefined) {
          const { host, name } = parseSessionKey(sessKey)
          fetch('/api/session/select-window', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ host: host || undefined, session: name, window: next.window, pane: next.pane || undefined }),
          }).catch(() => {})
        }
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

      // Close active pane: Cmd/Ctrl + Shift + W
      if (mod && e.shiftKey && (e.key === 'w' || e.key === 'W')) {
        e.preventDefault()
        if (activeKey !== null) {
          closePane(activeKey)
        }
        return
      }

      // Previous pane: Cmd/Ctrl + Shift + [ or Cmd/Ctrl + Alt + Left
      if (mod && ((e.shiftKey && e.key === '[') || (e.altKey && e.key === 'ArrowLeft'))) {
        e.preventDefault()
        if (activeKey !== null) {
          const leaves = paneTree ? getLeaves(paneTree) : []
          if (leaves.length > 1) {
            const idx = leaves.indexOf(activeKey)
            if (idx >= 0) {
              setActiveKey(leaves[(idx - 1 + leaves.length) % leaves.length])
            }
          }
        }
        return
      }

      // Next pane: Cmd/Ctrl + Shift + ] or Cmd/Ctrl + Alt + Right
      if (mod && ((e.shiftKey && e.key === ']') || (e.altKey && e.key === 'ArrowRight'))) {
        e.preventDefault()
        if (activeKey !== null) {
          const leaves = paneTree ? getLeaves(paneTree) : []
          if (leaves.length > 1) {
            const idx = leaves.indexOf(activeKey)
            if (idx >= 0) {
              setActiveKey(leaves[(idx + 1) % leaves.length])
            }
          }
        }
        return
      }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [prefs.quick_switcher_shortcut, navigateTo, onLogout, allToolEvents, selectedSession, paneTree, activeKey, closePane, openNewSessionModal])

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

  const selectSession = useCallback((sk: string) => {
    const { host, name } = parseSessionKey(sk)
    const path = host
      ? `/session/${encodeURIComponent(host)}/${encodeURIComponent(name)}`
      : `/session/${encodeURIComponent(name)}`
    if (window.location.pathname !== path) window.history.pushState(null, '', path)
    setCurrentView('session')
    if (paneTree && findLeaf(paneTree, sk)) {
      // In the split — focus it, clear singleView to show split
      setSingleView(null)
      setActiveKey(sk)
    } else {
      // Outside the split — show alone, don't touch paneTree or activeKey
      setSingleView(sk)
    }
  }, [paneTree])

  const handleSessionSelect = (session: Session) => {
    selectSession(sessionKey(session))
  }

  const handlePairSessions = useCallback((draggedKey: string, targetKey: string) => {
    setSingleView(null)
    setPaneTree(prev => {
      if (prev && findLeaf(prev, targetKey)) {
        // Target already in layout — add dragged beside it
        return splitLeaf(prev, targetKey, 'h', draggedKey)
      }
      if (prev && findLeaf(prev, draggedKey)) {
        // Dragged already in layout — add target beside it
        return splitLeaf(prev, draggedKey, 'h', targetKey)
      }
      // Neither in layout — fresh pair
      return { type: 'split', direction: 'h', ratio: 0.5, first: { type: 'leaf', sessionKey: draggedKey }, second: { type: 'leaf', sessionKey: targetKey } }
    })
    setActiveKey(draggedKey)
    const { host, name } = parseSessionKey(draggedKey)
    const path = host
      ? `/session/${encodeURIComponent(host)}/${encodeURIComponent(name)}`
      : `/session/${encodeURIComponent(name)}`
    if (window.location.pathname !== path) window.history.pushState(null, '', path)
    setCurrentView('session')
  }, [])

  const refocusTerminal = useCallback(() => {
    requestAnimationFrame(() => {
      const textarea = terminalContainerRef.current?.querySelector('textarea.xterm-helper-textarea') as HTMLTextAreaElement | null
      textarea?.focus()
    })
  }, [])

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

  const closeQuickSwitcher = useCallback(() => {
    setQuickSwitcherOpen(false)
    if (selectedSession) refocusTerminal()
  }, [selectedSession, refocusTerminal])

  const handleQuickSwitch = useCallback(async (sessKey: string, windowIndex?: number) => {
    setQuickSwitcherOpen(false)
    selectSession(sessKey)
    if (windowIndex !== undefined) {
      const { host, name } = parseSessionKey(sessKey)
      try {
        await fetch('/api/session/select-window', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ host: host || undefined, session: name, window: windowIndex }),
        })
      } catch (err) {
        console.error('Failed to select window:', err)
      }
    }
    // Refocus after navigation and window switch settle
    setTimeout(() => refocusTerminal(), 200)
  }, [selectSession, refocusTerminal])

  const handleCreateSession = useCallback(async (name: string, path: string, command: string, hostId?: string, agentType?: string) => {
    setNewSessionModalOpen(false)
    try {
      const res = await fetch('/api/session/new', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name, path, command, host: hostId || undefined, agent_type: agentType || undefined }),
      })
      if (res.ok) {
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
              return splitLeaf(prev, target.key, target.direction, sessKey)
            }
            return prev
          })
          setActiveKey(sessKey)
          await refresh()
          pendingSessionRef.current = null
          setTimeout(() => refocusTerminal(), 300)
        } else {
          navigateTo(sessKey)
          await refresh()
          pendingSessionRef.current = null
          setTimeout(() => refocusTerminal(), 300)
        }
      }
    } catch (err) {
      console.error('Failed to create session:', err)
      pendingSessionRef.current = null
    }
  }, [navigateTo, refresh, refocusTerminal])

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
      {quickSwitcherOpen && (
        <QuickSwitcher
          sessions={sessions}
          waitingEvents={allToolEvents.filter(e => e.status === 'waiting')}
          onSelect={handleQuickSwitch}
          onOverview={() => { closeQuickSwitcher(); navigateTo(null) }}
          onCreateSession={openNewSessionModal}
          onClose={closeQuickSwitcher}
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
            splitPanes={paneTree ? getLeaves(paneTree) : []}
            onPairSessions={handlePairSessions}
          />
        )}
        <div className="flex-1 flex flex-col overflow-hidden">
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
