import { useState, useEffect, useCallback, useRef, useMemo } from 'react'
import { tinykeys } from 'tinykeys'
import { Sidebar } from './components/Sidebar'
import { Terminal } from './components/Terminal'
import { Overview } from './components/Overview'
import { NewSessionModal, type NewSessionInput } from './components/NewSessionModal'
import { PortForwardModal } from './components/PortForwardModal'
import { ScheduleModal } from './components/ScheduleModal'
import { TopBar } from './components/TopBar'
import { TiledView } from './components/TiledView'
import { WikiPanel } from './components/WikiPanel'
import { PaneTree, getLeaves } from './lib/paneTree'
import { SettingsDrawer } from './components/SettingsDrawer'
import { HelpModal } from './components/HelpModal'
import { QuickSwitcher } from './components/QuickSwitcher'
import { Login } from './components/Login'
import { Setup } from './components/Setup'
// Hosts now come from sessionState instead of useHosts polling
import { useToolEvents } from './hooks/useToolEvents'
import { useActivity } from './hooks/useActivity'
import { useNotifications } from './hooks/useNotifications'
import { useWebSocket } from './hooks/useWebSocket'
import { usePushNotifications } from './hooks/usePushNotifications'
import { usePreferences, PreferencesContext } from './hooks/usePreferences'
import { useAuth } from './hooks/useAuth'
import { useWikiController } from './hooks/useWikiController'
import { WIKI_HISTORY_MAX, type WikiTarget, type WikiState } from './state/wiki'
import { Toasts, Toast } from './components/Toasts'
import { RecoveryPanel } from './components/RecoveryPanel'
import { useCrashedSessions } from './hooks/useCrashedSessions'
import { useSelfUpdate, type UpdateStatus } from './hooks/useSelfUpdate'
import { applyTheme } from './theme'
import { terminalPool, keyFor as poolKeyFor } from './lib/terminalPool'
import { keyToSessionRef, splitIdAtPath } from './state/session/paneTreeAdapter'
import type { SessionRef } from './state/session/types'
import { sessionRefToKey } from './state/session/paneTreeAdapter'
import { selectSessionByRef } from './state/session/projections'
import { useSessionState } from './hooks/useSessionState'
import { toSessionView, toPresentationAttrs, sessionViewSignal, type SessionView } from './state/session/viewModel'

type View = 'overview' | 'session' | 'settings' | 'setup'

// SessionApp is the sole production UI: App.tsx renders it unconditionally
// once authentication/setup has resolved.
function SessionApp({ onLogout, authenticated }: { onLogout?: () => void; authenticated: boolean }) {
  // Normalized catalog + workspace state via useSessionState. Only one
  // layout exists (no groups/singleView).
  const sessionState = useSessionState()
  const { state, paneTree, activeKey, layoutId } = sessionState

  // Local navigation state. There is no workspace reducer, so the view mode
  // is tracked locally and kept in sync with the URL. It must NOT be derived
  // purely from paneTree existence: once any session/layout has ever been
  // created, paneTree is permanently non-null, which would make "Overview"
  // unreachable forever (this was the original bug -- clicking Overview only
  // changed the URL without changing what was rendered).
  const [viewMode, setViewMode] = useState<'overview' | 'session'>(() =>
    window.location.pathname.startsWith('/session/') || window.location.pathname === '/session' ? 'session' : 'overview',
  )
  const [settingsOpen, setSettingsOpen] = useState(() => window.location.pathname === '/settings')
  useEffect(() => {
    const onPopState = () => {
      const path = window.location.pathname
      setSettingsOpen(path === '/settings')
      if (path === '/' || path === '') setViewMode('overview')
      else if (path.startsWith('/session')) setViewMode('session')
    }
    window.addEventListener('popstate', onPopState)
    return () => window.removeEventListener('popstate', onPopState)
  }, [])
  // Fall back to overview if there is genuinely no session to show yet
  // (e.g. before the first session is ever created), even if the URL claims
  // otherwise.
  // remotePaneKey: a browser-only "standalone remote pane" projection.
  //
  // The workspace model is local-authority only -- ApplyWorkspaceCommand's
  // `select` (and every other workspace mutation) can only ever target a
  // leaf of THIS node's own layout tree. A remote-owned session (visible in
  // the sidebar via the aggregate catalog, see AggregateCatalog/
  // MultiOwnerCatalogSnapshot) is never a leaf of the local layout, so
  // sending it through workspaceCommand('select') is guaranteed to be
  // rejected server-side as missing_target -- there is no legitimate way to
  // make the local workspace "contain" another node's pane.
  //
  // Instead, selecting a remote session sets remotePaneKey and renders it as
  // a synthetic single-leaf PaneTree that lives ONLY in this browser's local
  // view state (see renderTree below) -- never sent to any workspace command,
  // never part of paneTree. Terminal attachment still works because
  // getTerminalIdentity resolves the session from the catalog by ref (owner may
  // be remote) and terminalPool's buildUrl already forwards `host=<owner>` to
  // the existing /ws/daemon-session -> handleRemoteSession -> peer per-stream
  // relay path (pkg/server/routes_sessions.go), used regardless of
  // local-vs-remote.
  const [remotePaneKey, setRemotePaneKey] = useState<string | null>(null)
  const currentView: View = viewMode === 'session' && (paneTree || remotePaneKey) ? 'session' : 'overview'
  // Remembers which pane a "split" create should attach beside; set by
  // TiledView's onSplit, consumed and cleared by handleCreateSession.
  const splitTargetRef = useRef<{ key: string; direction: 'h' | 'v'; newFirst?: boolean } | null>(null)

  // Shared non-session hooks
  const { prefs } = usePreferences()
  const wikiEnabled = !prefs.wiki_disabled
  // SessionApp's session keys (SessionView.key / sessionRefToKey) are always
  // "ownerId/sessionId", never the raw peer transport fingerprint.
  // Build a hostIndex from canonical hosts for tool event normalization.
  const hostIndex = useMemo(() => {
    const byPeerId = new Map<string, any>()
    const byOwnerId = new Map<string, any>()
    let local: any | undefined
    const hosts = sessionState.hosts ?? []
    for (const h of hosts) {
      byPeerId.set(h.peer_id, h)
      byOwnerId.set(h.owner_id, h)
      if (h.local) local = h
    }
    return { hosts, local, byPeerId, byOwnerId }
  }, [sessionState.hosts])
  const { events: allToolEvents, handleEvent: handleToolEvent, getSessionEvents, sessionNeedsAttention, isSessionInActiveTurn, dismissEvent, dismissAll: dismissAllEvents } = useToolEvents(hostIndex)
  // Same OwnerID normalization rationale as useToolEvents(hostIndex) above --
  // the server's activity snapshot is keyed by peer fingerprint, but SessionApp
  // looks activity up by OwnerID/SessionID.
  const { getSessionActivity, handleActivityEvent } = useActivity(hostIndex)
  const { pushState, subscribe: pushSubscribe, unsubscribe: pushUnsubscribe } = usePushNotifications()
  const { processToolEvent } = useNotifications(pushState === 'subscribed')
  const { prefs: _ } = usePreferences() // already have prefs above
  // SessionApp has no workspace reducer, so useWikiController -- which
  // requires a real { state, actions } object -- gets its own small local
  // wiki-state stub here instead. This mirrors workspaceReducer.ts's
  // 'wiki/open'/'wiki/close' cases exactly (same WIKI_HISTORY_MAX-capped,
  // dedupe-by-path history) so the panel behaves identically to the legacy
  // path. Previously this was `undefined as any`, which crashed SessionApp
  // on every render (`Cannot destructure property 'state' of 'workspace' as
  // it is undefined`) -- found while building the multi-node E2E suite;
  // fixed here since it made the entire UI path non-functional, not
  // something in-scope to leave broken or skip around.
  const [wikiState, setWikiState] = useState<WikiState>({ target: null, history: [] })
  const wikiWorkspaceLike = useMemo(
    () => ({
      state: { wiki: wikiState },
      actions: {
        openWiki: (target: WikiTarget) =>
          setWikiState((s) => ({
            target,
            history: [target, ...s.history.filter((t) => t.path !== target.path)].slice(0, WIKI_HISTORY_MAX),
          })),
        closeWiki: () => setWikiState((s) => ({ ...s, target: null })),
      },
    }),
    [wikiState],
  )
  const wiki = useWikiController(wikiWorkspaceLike, wikiEnabled)
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

  // getTerminalIdentity: converts a sessionKey to a catalog lookup.
  //
  // Invariant: a terminal must NEVER silently fall back to legacy
  // name-based routing. terminalPool.checkout() only rejects an identity when
  // ownerId or generation is present without sessionId; a fully-empty object
  // would pass through undetected as "no session identity supplied" (the
  // legacy caller's shape) even though this IS the session-state caller. So
  // when a session cannot be resolved (unknown ref, or catalog not yet
  // bootstrapped), we still surface the catalog's own owner id (always known
  // once bootstrapped) with no sessionId, which trips that invariant
  // deliberately instead of silently degrading to legacy routing.
  const getTerminalIdentity = useCallback((legacyKey: string) => {
    // Sessions are always daemon-backed; backend is a fixed constant here,
    // never resolved separately (TiledView must not fall back to legacy
    // name-based routing for any pane). ready=false gates TiledView into
    // rendering a loading state instead of mounting Terminal with a partial
    // identity that terminalPool could misroute.
    if (!legacyKey) return { ready: false, backend: 'daemon' as const }
    const ref = keyToSessionRef(legacyKey)
    const session = selectSessionByRef(state.catalog, ref)
    if (!session) return { ready: false, backend: 'daemon' as const }
    // generation is the per-session daemon binding generation from the session's
    // generation field, NOT the websocket connection generation (tracked per-owner in
    // state.catalog.ownerMeta). If the session has no daemon generation yet
    // (e.g., still pending), it is not ready to attach.
    const sessionGeneration = session.generation
    if (!sessionGeneration) return { ready: false, backend: 'daemon' as const }
    return {
      ready: true,
      backend: 'daemon' as const,
      sessionId: session.id,
      ownerId: session.owner,
      generation: String(sessionGeneration),
    }
  }, [state.catalog])

  // Friendly pane-header label: mirrors SessionView.label (displayName ->
  // id fallback) instead of parsing the immutable canonical session id out
  // of the key. Reads state.catalog directly (not the `sessionViews` memo,
  // which is declared later in this component) so this resolver is safe to
  // construct here.
  const getSessionLabel = useCallback((legacyKey: string) => {
    const ref = keyToSessionRef(legacyKey)
    const session = selectSessionByRef(state.catalog, ref)
    if (!session) return legacyKey
    const label = session.name
    return label && label.trim() !== '' ? label : session.id
  }, [state.catalog])

  const handleRenameSession = useCallback((legacyKey: string, label: string) => {
    const ref = keyToSessionRef(legacyKey)
    void sessionState.sessionCommand(ref, { action: 'label', label })
      .catch(err => console.error('label command failed:', err))
  }, [sessionState])

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
      // Canonical state doesn't use these, but listen silently
    } else if (['peer-connected', 'peer-disconnected'].includes(evt.type)) {
      // Host state now comes through the canonical state stream, no refresh needed
    } else if (evt.type === 'update-status') {
      // ignore
    } else if (evt.type === 'session-attrs-updated') {
      // Canonical state has no server-side session-attrs concept; the
      // backend never sends this event, and there is no local state to refresh.
    } else if (evt.type === 'sessions-crashed') {
      crashedHook.refresh()
    }
  }, [handleToolEvent, processToolEvent, handleActivityEvent, crashedHook.refresh])

  const { connected } = useWebSocket('/ws/events', onEvent)

  useEffect(() => {
    const needsAttention = allToolEvents.some(evt => evt.status === 'waiting' || evt.status === 'error' || evt.status === 'stuck')
    document.title = needsAttention ? 'Termyard - Attention needed' : 'Termyard'
  }, [allToolEvents])

  useEffect(() => {
    if (currentView !== 'session') setTerminalFullscreen(false)
  }, [currentView])

  const hasMultipleHosts = (sessionState.hosts ?? []).length > 1
  const localHost = sessionState.selectLocalHost ? sessionState.selectLocalHost() : hostIndex.local
  const localHostId = localHost?.peer_id
  const localHostName = localHost?.name

  const refocusTerminal = useCallback(() => {
    requestAnimationFrame(() => {
      const textarea = terminalContainerRef.current?.querySelector('textarea.xterm-helper-textarea') as HTMLTextAreaElement | null
      textarea?.focus()
    })
  }, [])

  const handleSessionSelect = (session: SessionView) => {
    // Built directly from the session's own ref -- SessionView.ref is the
    // canonical identity this view was derived from.
    const ref: SessionRef = session.ref
    const key = session.key
    const host = ref.owner ?? ''
    const name = ref.session
    const path = host
      ? `/session/${encodeURIComponent(host)}/${encodeURIComponent(name)}`
      : `/session/${encodeURIComponent(name)}`
    if (window.location.pathname !== path) window.history.pushState(null, '', path)
    setViewMode('session')
    // A remote-owned ref (owner set and not this node's own owner) is never a
    // leaf of the LOCAL layout tree, so workspaceCommand('select') against it
    // is guaranteed to be rejected server-side as missing_target. Render it as
    // a standalone browser-local pane instead (see remotePaneKey above)
    // rather than issuing a command that can never succeed.
    const isRemote = ref.owner !== null && state.catalog.localOwner !== null && ref.owner !== state.catalog.localOwner
    if (isRemote) {
      setRemotePaneKey(key)
    } else {
      setRemotePaneKey(null)
      if (layoutId && activeKey !== key) {
        void sessionState.workspaceCommand(layoutId, { action: 'select', ref })
          .catch(err => console.error('select command failed:', err))
      }
    }
    setTimeout(refocusTerminal, 150)
  }

  // Canonical session views, built straight from the catalog via
  // state/session/viewModel.ts. Every product component (Sidebar, Overview,
  // QuickSwitcher, NewSessionModal, SessionActionsMenu) now consumes these
  // directly -- there is no synthetic Session[] shim any more.
  const sessionViews = useMemo<SessionView[]>(
    () => Array.from(state.catalog.sessionsByRef.values()).map(record => toSessionView(record, hostIndex, state.catalog.localOwner)),
    [state.catalog.sessionsByRef, hostIndex, state.catalog.localOwner],
  )

  // Real hidden/background presentation state (see the block comment above
  // SessionApp for the set_presentation wiring this reads/writes).
  const sessionAttrs = useMemo(() => toPresentationAttrs(sessionViews), [sessionViews])
  const setSessionAttr = useCallback((key: string, next: { background?: boolean; hidden?: boolean }) => {
    const ref = keyToSessionRef(key)
    void sessionState.sessionCommand(ref, { action: 'set_presentation', ...next })
      .catch(err => console.error('set_presentation command failed:', err))
  }, [sessionState])

  const handleJumpToSession = useCallback((sessionKeyOrId: string, _windowIndex?: number, _pane?: string) => {
    const found = sessionViews.find(view => view.key === sessionKeyOrId)
    if (found) handleSessionSelect(found)
  }, [sessionViews, layoutId, activeKey])

  const handleKillSession = useCallback((key: string) => {
    const ref = keyToSessionRef(key)
    void sessionState.sessionCommand(ref, { action: 'kill' })
      .catch(err => console.error('kill command failed:', err))
  }, [sessionState])

  const handleQuickShell = useCallback(() => {
    void sessionState.createSession({}).then(result => {
      if (result.ref?.session) setTimeout(refocusTerminal, 150)
    }).catch(err => console.error('quick shell create failed:', err))
  }, [sessionState, refocusTerminal])

  const handleCreateSession = useCallback(async (input: NewSessionInput): Promise<void> => {
    // input.targetOwner is already the canonical OwnerID: HostSelect's option
    // values are OwnerIDs directly (see NewSessionModal), so no
    // fingerprint-to-owner lookup is needed here.
    //
    // Placement is one atomic step: when a split was requested (target set),
    // the split's target/direction/new_first are sent as part of THIS create
    // command (see CreateParams in pkg/state/session_commands.go), instead
    // of a separate follow-up workspace "split" command. Doing create then
    // split as two calls raced against create's own default placement
    // (whichever leaf create picked first) and the follow-up split was then
    // rejected as inserting a duplicate leaf for the ref create had just
    // placed.
    const target = splitTargetRef.current
    splitTargetRef.current = null
    await sessionState.createSession({
      name: input.name,
      shell: input.shell,
      cwd: input.cwd,
      worktreeBranch: input.worktreeBranch,
      agentType: input.agentType,
      layoutId: target ? (layoutId ?? undefined) : undefined,
      targetOwner: input.targetOwner,
      splitTarget: target ? keyToSessionRef(target.key) : undefined,
      splitDirection: target?.direction,
      splitNewFirst: target?.newFirst,
    })
    // Close only after the create resolved successfully; on failure the
    // promise rejects (see sessionState.createSession) and the modal stays
    // open with the error rendered by NewSessionModal itself.
    setNewSessionModalOpen(false)
    setViewMode('session')
    setRemotePaneKey(null)
    setTimeout(refocusTerminal, 150)
  }, [sessionState, layoutId, refocusTerminal])

  const toggleFullscreen = useCallback(() => {
    setTerminalFullscreen(f => !f)
  }, [])

  const glance = useMemo(() => {
    let parked = 0, working = 0, waiting = 0
    for (const view of sessionViews) {
      const signal = sessionViewSignal(view, getSessionEvents(view.key), getSessionActivity(view.key), isSessionInActiveTurn(view.key))
      if (signal.state === 'needs_you') waiting++
      else if (signal.state === 'working') working++
      else parked++
    }
    return { parked, working, waiting }
  }, [sessionViews, getSessionEvents, getSessionActivity, isSessionInActiveTurn, allToolEvents])

  const openNewSessionModal = useCallback(() => {
    setNewSessionModalOpen(true)
  }, [])

  const openSettings = useCallback(() => {
    if (window.location.pathname !== '/settings') window.history.pushState(null, '', '/settings')
    setSettingsOpen(true)
  }, [])

  const closeSettings = useCallback(() => {
    window.history.pushState(null, '', '/')
    setSettingsOpen(false)
  }, [])

  const goToOverview = useCallback(() => {
    if (window.location.pathname !== '/') window.history.pushState(null, '', '/')
    setViewMode('overview')
    setRemotePaneKey(null)
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
      {schedulesOpen && (
        <ScheduleModal
          onClose={() => setSchedulesOpen(false)}
          sessions={sessionViews}
          hosts={sessionState.hosts}
          killSession={(ref) => sessionState.sessionCommand(ref, { action: 'kill' }).then(() => undefined)}
        />
      )}
      {quickSwitcherOpen && (
        <QuickSwitcher
          sessions={sessionViews}
          waitingEvents={allToolEvents}
          onSelect={(sessionName, windowIndex) => {
            handleJumpToSession(sessionName, windowIndex)
            setQuickSwitcherOpen(false)
          }}
          onOverview={() => {
            goToOverview()
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
          hosts={sessionState.hosts}
          sessions={sessionViews}
          onCreateSession={handleCreateSession}
          onClose={() => setNewSessionModalOpen(false)}
        />
      )}
      {(!terminalFullscreen) && (
        <TopBar
          currentView={currentView}
          settingsActive={settingsOpen}
          selfUpdateAvailable={selfUpdate.status?.update_available ?? false}
          updateVersion={selfUpdate.status?.latest_version}
          onApplyUpdate={selfUpdate.apply}
          updateApplying={selfUpdate.applying}
          onDismissUpdate={selfUpdate.dismiss}
          onOverview={goToOverview}
          onSettings={openSettings}
          onWiki={wikiEnabled ? wiki.togglePanel : undefined}
          onHelp={() => setHelpOpen(true)}
          onNewSession={openNewSessionModal}
          onPortForwards={() => setPortForwardsOpen(true)}
          onSchedules={() => setSchedulesOpen(true)}
          events={allToolEvents}
          connected={connected === true}
          onJumpToSession={handleJumpToSession}
          onDismiss={dismissEvent}
          onDismissAll={dismissAllEvents}
          panesCount={paneTree ? getLeaves(paneTree).length : 0}
          onSplitPane={(direction) => {
            if (activeKey !== null) {
              splitTargetRef.current = { key: activeKey, direction }
            }
            openNewSessionModal()
          }}
          glance={glance}
        />
      )}
      <div className="flex-1 flex overflow-hidden">
        {!terminalFullscreen && (
          <Sidebar
            sessions={sessionViews}
            selectedSession={remotePaneKey ?? activeKey}
            collapsed={sidebarCollapsed}
            selfUpdateAvailable={selfUpdate.status?.update_available ?? false}
            collapseMode={(prefs.sidebar.collapse_mode || 'small') as 'small' | 'hidden'}
            width={sidebarWidth}
            onWidthChange={handleSidebarWidth}
            hasMultipleHosts={hasMultipleHosts}
            localHostId={localHostId}
            hosts={sessionState.hosts}
            onSessionSelect={handleSessionSelect}
            getSessionEvents={getSessionEvents}
            sessionNeedsAttention={sessionNeedsAttention}
            isSessionInActiveTurn={isSessionInActiveTurn}
            getSessionActivity={getSessionActivity}
            glance={glance}
            onToggleCollapse={() => setSidebarCollapsed(c => !c)}
            onSessionKilled={handleKillSession}
            sessionAttrs={sessionAttrs}
            setSessionAttr={setSessionAttr}
            pruningSuspended={false}
            onQuickShell={handleQuickShell}
            crashedCount={crashedHook.crashedSessions.length}
            onCrashedClick={() => crashedHook.refresh()}
            onRenameSession={handleRenameSession}
          />
        )}
        <div className="flex-1 flex flex-col overflow-hidden relative">
          {currentView === 'session' && (remotePaneKey || paneTree) ? (
            <TiledView
              tree={remotePaneKey ? { type: 'leaf', sessionKey: remotePaneKey } : paneTree}
              activeKey={remotePaneKey ?? activeKey}
              onActivate={(key) => {
                // remotePaneKey's synthetic tree has exactly one leaf, so this
                // only fires for the local paneTree case.
                if (!remotePaneKey && layoutId && activeKey !== key) {
                  void sessionState.workspaceCommand(layoutId, { action: 'select', ref: keyToSessionRef(key) })
                    .catch(err => console.error('select command failed:', err))
                }
                refocusTerminal()
              }}
              onClose={(key) => {
                // The standalone remote pane is not part of any workspace layout
                // (see remotePaneKey above), so "close" just drops the browser-
                // local projection rather than sending a workspace 'remove' that
                // would be rejected as missing_target. Falls back to Overview if
                // there is no local session to return to.
                if (remotePaneKey) {
                  setRemotePaneKey(null)
                  if (!paneTree) setViewMode('overview')
                  return
                }
                if (layoutId) {
                  void sessionState.workspaceCommand(layoutId, { action: 'remove', ref: keyToSessionRef(key) })
                    .catch(err => console.error('remove command failed:', err))
                }
              }}
              onKill={handleKillSession}
              onPopOut={(key) => {
                if (!remotePaneKey && layoutId) {
                  void sessionState.workspaceCommand(layoutId, { action: 'pop_out', ref: keyToSessionRef(key) })
                    .catch(err => console.error('pop_out command failed:', err))
                }
              }}
              onSplit={(key, direction) => {
                // Splitting beside a standalone remote pane is not supported: a
                // split target must be a leaf of the LOCAL layout tree, which
                // a remote-owned session never is.
                if (remotePaneKey) return
                splitTargetRef.current = { key, direction }
                openNewSessionModal()
              }}
              onRatioChange={(path, ratio) => {
                if (!remotePaneKey && layoutId) {
                  const record = state.workspace.record
                  const splitId = record ? splitIdAtPath(record.tree, path) : undefined
                  if (splitId) {
                    void sessionState.workspaceCommand(layoutId, { action: 'resize', split_id: splitId, ratio })
                      .catch(err => console.error('resize command failed:', err))
                  }
                }
              }}
              fullscreen={terminalFullscreen}
              onToggleFullscreen={toggleFullscreen}
              terminalContainerRef={terminalContainerRef}
              onSwapPanes={(a, b) => {
                if (!remotePaneKey && layoutId) {
                  void sessionState.workspaceCommand(layoutId, { action: 'swap', a: keyToSessionRef(a), b: keyToSessionRef(b) })
                    .catch(err => console.error('swap command failed:', err))
                }
              }}
              onMovePanes={(sourceKey, targetKey, edge) => {
                if (!remotePaneKey && layoutId) {
                  void sessionState.workspaceCommand(layoutId, {
                    action: 'move',
                    source: keyToSessionRef(sourceKey),
                    target: keyToSessionRef(targetKey),
                    edge,
                  }).catch(err => console.error('move command failed:', err))
                }
              }}
              getTerminalIdentity={getTerminalIdentity}
              getSessionLabel={getSessionLabel}
              onOpenFile={wiki.openFile}
            />
          ) : (
            <Overview
              sessions={sessionViews}
              onOpenFile={wiki.openFile}
              hosts={sessionState.hosts}
              hiddenSet={sessionAttrs.hidden}
              backgroundSet={sessionAttrs.background}
              scheduleIDs={sessionAttrs.scheduleIDs}
              onSessionSelect={handleSessionSelect}
              getSessionEvents={getSessionEvents}
              getSessionActivity={getSessionActivity}
              isSessionInActiveTurn={isSessionInActiveTurn}
              onJumpToSession={handleJumpToSession}
              onDismissAlert={dismissEvent}
              setSessionAttr={setSessionAttr}
              onSessionKilled={handleKillSession}
              onRenameSession={handleRenameSession}
            />
          )}
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

export default SessionApp
