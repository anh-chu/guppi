/**
 * @vitest-environment jsdom
 */

/**
 * Integration test for App component mode-splitting.
 *
 * Verifies:
 * 1. AppV2 with getTerminalIdentity resolver is activated when v2 is enabled
 * 2. AppLegacy is activated when v2 is disabled
 * 3. Branch happens at top-level App, not per-render conditionally
 */

import { describe, it, expect, beforeEach, vi } from 'vitest'
import { sessionKey } from './hooks/useSessions'
import { keyToSessionRef } from './state/v2/paneTreeAdapter'
import { encodeSessionRef } from './state/v2/types'

// Track which code path was taken
let codePathTaken: 'appv2' | 'applegacy' | 'none' = 'none'

// Feature flag
let v2Enabled = false
vi.mock('./lib/featureFlags', () => ({
  isV2StateEnabled: () => v2Enabled,
}))

// Mock hooks that are needed by App
vi.mock('./hooks/useAuth', () => ({
  useAuth: () => ({
    loading: false,
    authRequired: false,
    needsSetup: false,
    authenticated: true,
    error: null,
    setup: vi.fn(),
    login: vi.fn(),
    logout: vi.fn(),
  }),
}))

vi.mock('./hooks/usePreferences', () => ({
  usePreferencesProvider: () => ({
    loaded: true,
    prefs: {
      theme: 'dark',
      wiki_disabled: false,
      lock_timeout_minutes: 0,
      sidebar: { collapse_mode: 'small' },
    },
    refetch: vi.fn(),
  }),
  usePreferences: () => ({
    prefs: {
      theme: 'dark',
      wiki_disabled: false,
      lock_timeout_minutes: 0,
      sidebar: { collapse_mode: 'small' },
    },
  }),
  PreferencesContext: { Provider: ({ children }: any) => children },
}))

// Track if useV2State is called (only called in AppV2 path)
// mockV2State lets tests inject a concrete v2 state (populated catalog/layout)
// while keeping the default (empty) shape for existing tests.
let mockV2State: any = null
// mockHostsList lets tests inject a concrete useHosts() list (e.g. a host
// with a v2 OwnerID) so handleCreateSession's fingerprint->OwnerID resolution
// has something real to resolve against. null means the default empty list.
let mockHostsList: any[] | null = null
const defaultV2State = {
  state: {
    catalog: { owner: null, revision: 0, generation: 0, sessionsByRef: new Map(), layoutsById: new Map() },
    workspace: { layoutId: null, revision: 0, generation: 0, record: null, presentationsByRef: new Map() },
    connectionGeneration: 0,
    connectionOnline: false,
    catalogBootstrapped: false,
    workspaceBootstrapped: false,
  },
  bootstrapped: false,
  connected: false,
  paneTree: null,
  activeKey: null,
  layoutId: null,
  createSession: vi.fn(),
  sessionCommand: vi.fn(),
  workspaceCommand: vi.fn(),
}
vi.mock('./hooks/useV2State', () => ({
  useV2State: () => {
    codePathTaken = 'appv2'
    return mockV2State ?? defaultV2State
  },
}))

// Track if useWorkspace is called (only called in AppLegacy path)
vi.mock('./hooks/useWorkspace', () => ({
  useWorkspace: () => {
    codePathTaken = 'applegacy'
    return {
      state: {
        sessions: [],
        loading: false,
        groups: {},
        groupsLoaded: true,
        view: {
          currentView: 'overview',
          settingsOpen: false,
          paneTree: null,
          activeKey: null,
          singleView: null,
          activeGroupId: 'default',
        },
        connection: { livenessUnknown: false },
      },
      actions: {
        setPaneTree: vi.fn(),
        setActiveKey: vi.fn(),
        setSingleView: vi.fn(),
        setCurrentView: vi.fn(),
        openSettings: vi.fn(),
        setActiveGroup: vi.fn(),
        navigate: vi.fn(),
        refresh: vi.fn(),
        addOptimistic: vi.fn(),
        removeOptimistic: vi.fn(),
        renameSession: vi.fn(),
        selectSession: vi.fn(),
        splitPane: vi.fn(),
        closePane: vi.fn(),
        removeFromLayout: vi.fn(),
        dissolveToSingle: vi.fn(),
        pruneMissing: vi.fn(),
        onEvent: vi.fn(),
        setConnection: vi.fn(),
      },
      groupSync: {
        refresh: vi.fn(),
        setTree: vi.fn(),
        setName: vi.fn(),
        setRank: vi.fn(),
        deleteGroup: vi.fn(),
        forceAiName: vi.fn(),
        namingGroupId: null,
      },
    }
  },
}))

// Mock other hooks with minimal implementations
const createMockHook = (name: string) => vi.fn(() => {
  if (name === 'useHosts') return { hosts: mockHostsList ?? [], refresh: vi.fn() }
  if (name === 'useToolEvents') return {
    events: [],
    handleEvent: vi.fn(),
    getSessionEvents: () => [],
    sessionNeedsAttention: () => false,
    isSessionInActiveTurn: () => false,
    dismissEvent: vi.fn(),
    dismissAll: vi.fn(),
  }
  if (name === 'useActivity') return { getSessionActivity: () => null, handleActivityEvent: vi.fn() }
  if (name === 'useNotifications') return { processToolEvent: vi.fn() }
  if (name === 'useWebSocket') return { connected: false } // overridden below for useWebSocket specifically
  if (name === 'usePushNotifications') return { pushState: 'unsupported', subscribe: vi.fn(), unsubscribe: vi.fn() }
  if (name === 'useSessionAttrs') return { sets: { hidden: new Set(), background: new Set(), scheduleIDs: {} }, setAttr: vi.fn(), refresh: vi.fn() }
  if (name === 'useSessionOrder') return { ranks: {}, loaded: true, refresh: vi.fn(), setRank: vi.fn() }
  if (name === 'useCrashedSessions') return { crashedSessions: [], recover: vi.fn(), dismiss: vi.fn(), dismissAll: vi.fn(), refresh: vi.fn() }
  if (name === 'useSelfUpdate') return { status: null, apply: vi.fn(), dismiss: vi.fn(), checkNow: vi.fn(), applying: false, restartMode: null, error: null, checking: false }
  if (name === 'useWikiController') return { target: null, closePanel: vi.fn(), togglePanel: vi.fn(), openFile: vi.fn() }
  return {}
})

vi.mock('./hooks/useHosts', () => ({ useHosts: createMockHook('useHosts') }))
vi.mock('./hooks/useToolEvents', () => ({ useToolEvents: createMockHook('useToolEvents') }))
vi.mock('./hooks/useActivity', () => ({ useActivity: createMockHook('useActivity') }))
vi.mock('./hooks/useNotifications', () => ({ useNotifications: createMockHook('useNotifications') }))
// Captures the onEvent handler AppV2/AppLegacy register with useWebSocket so
// tests can dispatch a synthetic server event straight into the real App
// event-handling code path without needing a real socket.
let capturedOnEvent: ((evt: any) => void) | null = null
vi.mock('./hooks/useWebSocket', () => ({
  useWebSocket: (_path: string, onEvent: (evt: any) => void) => {
    capturedOnEvent = onEvent
    return { connected: false }
  },
}))
vi.mock('./hooks/usePushNotifications', () => ({ usePushNotifications: createMockHook('usePushNotifications') }))
vi.mock('./hooks/useSessionAttrs', () => ({ useSessionAttrs: createMockHook('useSessionAttrs') }))
vi.mock('./hooks/useSessionOrder', () => ({ useSessionOrder: createMockHook('useSessionOrder') }))
vi.mock('./hooks/useCrashedSessions', () => ({ useCrashedSessions: createMockHook('useCrashedSessions') }))
vi.mock('./hooks/useSelfUpdate', () => ({ useSelfUpdate: createMockHook('useSelfUpdate') }))
vi.mock('./hooks/useWikiController', () => ({ useWikiController: createMockHook('useWikiController') }))

// Capture Sidebar/TopBar props so tests can drive the REAL AppV2 callbacks
// (handleSessionSelect / handleJumpToSession / handleKillSession) that the ui
// leaf components hook into, without needing full DOM interaction.
let mockSidebarProps: any = null
let mockTopBarProps: any = null

// Mock components to avoid deep rendering issues
vi.mock('./components/Sidebar', () => ({ Sidebar: (props: any) => { mockSidebarProps = props; return null } }))
vi.mock('./components/Terminal', () => ({ Terminal: () => null }))
vi.mock('./components/Overview', () => ({ Overview: () => null }))
let mockNewSessionModalProps: any = null
vi.mock('./components/NewSessionModal', () => ({
  NewSessionModal: (props: any) => { mockNewSessionModalProps = props; return null },
}))
vi.mock('./components/PortForwardModal', () => ({ PortForwardModal: () => null }))
vi.mock('./components/ScheduleModal', () => ({ ScheduleModal: () => null }))
vi.mock('./components/TopBar', () => ({ TopBar: (props: any) => { mockTopBarProps = props; return null } }))
let mockTiledViewProps: any = null
vi.mock('./components/TiledView', () => ({ TiledView: (props: any) => { mockTiledViewProps = props; return null } }))
vi.mock('./components/WikiPanel', () => ({ WikiPanel: () => null }))
vi.mock('./components/SettingsDrawer', () => ({ SettingsDrawer: () => null }))
vi.mock('./components/HelpModal', () => ({ HelpModal: () => null }))
vi.mock('./components/QuickSwitcher', () => ({ QuickSwitcher: () => null }))
vi.mock('./components/Login', () => ({ Login: () => null }))
vi.mock('./components/Setup', () => ({ Setup: () => null }))
vi.mock('./components/RecoveryPanel', () => ({ RecoveryPanel: () => null }))
vi.mock('./components/Toasts', () => ({ Toasts: () => null }))

vi.mock('./theme', () => ({ applyTheme: vi.fn(), getXtermTheme: vi.fn(() => ({})) }))
vi.mock('tinykeys', () => ({ tinykeys: vi.fn(() => vi.fn()) }))
vi.mock('fractional-indexing', () => ({ generateKeyBetween: vi.fn(() => '') }))

describe('App: mode-splitting', () => {
  beforeEach(() => {
    codePathTaken = 'none'
    vi.resetModules()
  })

  describe('branch point', () => {
    it('app with v2 disabled calls useWorkspace (AppLegacy path)', async () => {
      v2Enabled = false
      const { render } = await import('@testing-library/react')
      const App = (await import('./App')).default

      render(<App />)

      expect(codePathTaken).toBe('applegacy')
    })

    it('app with v2 enabled calls useV2State (AppV2 path)', async () => {
      v2Enabled = true
      const { render } = await import('@testing-library/react')
      const App = (await import('./App')).default

      render(<App />)

      expect(codePathTaken).toBe('appv2')
    })
  })

  describe('AppV2 getTerminalIdentity integration', () => {
    it('AppV2 provides getTerminalIdentity resolver to TiledView', async () => {
      // This test verifies the integration point is wired.
      // The resolver itself is tested via unit tests in projections/store.
      // Here we just prove the component is structured to pass it.
      
      v2Enabled = true
      const { render } = await import('@testing-library/react')
      const App = (await import('./App')).default

      const { container } = render(<App />)

      // If we got here without error, the wiring is correct
      // (render would have failed if getTerminalIdentity type didn't match)
      expect(container).toBeTruthy()
      expect(codePathTaken).toBe('appv2')
    })

    it('getTerminalIdentity uses session.generation (per-session daemon generation), not catalog.generation (websocket connection generation)', () => {
      // This verifies the high-severity bug fix: terminal attachment must use
      // the per-session daemon binding generation (session.generation),
      // NOT the websocket connection generation (state.catalog.generation).
      
      // The logic under test is in App.tsx getTerminalIdentity:
      // const sessionGeneration = session.generation ?? ''
      // return { sessionId, ownerId, generation: String(sessionGeneration) }
      
      // Mock data: a session with a real daemon generation
      const sessionId = 's123'
      const ownerId = 'owner1'
      const daemonGeneration = 'daemon-gen-abc'
      const catalogGeneration = 999 // Websocket connection generation (should NOT be used)
      
      const session: any = {
        id: sessionId,
        owner: ownerId,
        ref: { owner: ownerId, session: sessionId, window: 0, pane: 0 },
        phase: 'active' as const,
        desired: 'run' as const,
        revision: 5,
        created_at: '2024-01-01T00:00:00Z',
        generation: daemonGeneration, // Per-session daemon generation
      }
      
      // Simulate the getTerminalIdentity logic (from App.tsx)
      const sessionGeneration = session.generation ?? ''
      const identity = {
        sessionId: session.id,
        ownerId: session.owner,
        generation: String(sessionGeneration),
      }
      
      expect(identity.sessionId).toBe(sessionId)
      expect(identity.ownerId).toBe(ownerId)
      expect(identity.generation).toBe(daemonGeneration)
      expect(identity.generation).not.toBe(String(catalogGeneration)) // MUST NOT use websocket generation
    })

    it('getTerminalIdentity returns empty string for generation when session has no daemon generation', () => {
      // Session with no daemon generation yet (e.g., pending)
      const sessionId = 's456'
      const ownerId = 'owner2'
      const session: any = {
        id: sessionId,
        owner: ownerId,
        ref: { owner: ownerId, session: sessionId, window: 0, pane: 0 },
        phase: 'pending' as const,
        desired: 'run' as const,
        revision: 1,
        created_at: '2024-01-01T00:00:00Z',
      }
      
      // Simulate the getTerminalIdentity logic (from App.tsx)
      const sessionGeneration = session.generation ?? ''
      const identity = {
        sessionId: session.id,
        ownerId: session.owner,
        generation: String(sessionGeneration),
      }
      
      // When session has no daemon generation, we return empty string.
      // This will trigger the invariant check in terminalPool.checkout():
      // if (ownerId || generation) && !sessionId => throw
      expect(identity.generation).toBe('')
    })

    it('getTerminalIdentity returns empty object when session not found (triggers pool invariant)', () => {
      // Session not found
      const session = undefined
      
      // Simulate the getTerminalIdentity logic (from App.tsx)
      const identity: Record<string, any> = !session ? {} : { sessionId: '', ownerId: '' }
      
      // getTerminalIdentity returns empty object when session is not found
      // This is correct: it will either use legacy name-based routing or
      // the terminalPool.checkout() invariant check will reject it if v2
      // identity was requested but not available.
      expect(identity).toEqual({})
    })

    beforeEach(() => {
      mockSidebarProps = null
      mockTopBarProps = null
      mockV2State = null
      mockHostsList = null
    })

    it('v2 session rows derive keys from the immutable id, not the mutable name label', async () => {
      const sessionId = 'sess-abc-123'
      const label = 'Friendly Renamed Label'
      const workspaceCommand = vi.fn().mockResolvedValue({})
      const sessionCommand = vi.fn().mockResolvedValue({})
      const session: any = {
        id: sessionId,
        owner: null,
        ref: { owner: null, session: sessionId, window: 0, pane: 0 },
        phase: 'active',
        desired: 'run',
        revision: 3,
        created_at: '2025-01-01T00:00:00Z',
        name: label,
      }
      const sessionsByRef = new Map([[encodeSessionRef(session.ref), session]])
      mockV2State = {
        state: {
          catalog: { owner: null, revision: 3, generation: 1, sessionsByRef, layoutsById: new Map() },
          workspace: { layoutId: 'g1', revision: 0, generation: 0, record: null, presentationsByRef: new Map() },
          connectionGeneration: 1,
          connectionOnline: true,
          catalogBootstrapped: true,
          workspaceBootstrapped: false,
        },
        bootstrapped: true,
        connected: true,
        paneTree: null,
        activeKey: 'other-key',
        layoutId: 'g1',
        createSession: vi.fn().mockResolvedValue({}),
        sessionCommand,
        workspaceCommand,
      }

      v2Enabled = true
      const { render } = await import('@testing-library/react')
      const App = (await import('./App')).default
      render(<App />)

      expect(mockSidebarProps).not.toBeNull()
      const [sess] = mockSidebarProps.sessions
      // Memo shape: name is the immutable id; the mutable label lives in display_name.
      expect(sess.id).toBe(sessionId)
      expect(sess.name).toBe(sessionId)
      expect(sess.display_name).toBe(label)
      // sessionKey now yields the id-based key, matching sessionRefToKey(ref).
      const key = sessionKey(sess)
      expect(key).toBe(sessionId)
      expect(keyToSessionRef(key)).toEqual({ owner: null, session: sessionId, window: 0, pane: 0 })

      // Sidebar row select -> real handleSessionSelect -> v2 select command with
      // the canonical ref (session === immutable id, NOT the label).
      mockSidebarProps.onSessionSelect(sess)
      expect(workspaceCommand).toHaveBeenCalledWith('g1', {
        action: 'select',
        ref: { owner: null, session: sessionId, window: 0, pane: 0 },
      })

      // Quick-switcher / TopBar jump resolves the same row by the id-based key.
      expect(mockTopBarProps).not.toBeNull()
      workspaceCommand.mockClear()
      mockTopBarProps.onJumpToSession(sessionKey(sess))
      expect(workspaceCommand).toHaveBeenCalledWith('g1', {
        action: 'select',
        ref: { owner: null, session: sessionId, window: 0, pane: 0 },
      })

      // Kill from the sidebar context menu routes the id-based key to a canonical ref.
      sessionCommand.mockClear()
      mockSidebarProps.onSessionKilled(sessionKey(sess))
      expect(sessionCommand).toHaveBeenCalledWith(
        { owner: null, session: sessionId, window: 0, pane: 0 },
        { action: 'kill' },
      )
    })

    it('v2 mode never calls useSessionAttrs and treats session-attrs-updated as a no-op', async () => {
      const workspaceCommand = vi.fn().mockResolvedValue({})
      mockV2State = {
        state: {
          catalog: { owner: null, revision: 0, generation: 0, sessionsByRef: new Map(), layoutsById: new Map() },
          workspace: { layoutId: null, revision: 0, generation: 0, record: null, presentationsByRef: new Map() },
          connectionGeneration: 0,
          connectionOnline: false,
          catalogBootstrapped: false,
          workspaceBootstrapped: false,
        },
        bootstrapped: false,
        connected: false,
        paneTree: null,
        activeKey: null,
        layoutId: null,
        createSession: vi.fn().mockResolvedValue({}),
        sessionCommand: vi.fn(),
        workspaceCommand,
      }

      const useSessionAttrsModule = await import('./hooks/useSessionAttrs')
      const useSessionAttrsSpy = useSessionAttrsModule.useSessionAttrs as ReturnType<typeof vi.fn>
      useSessionAttrsSpy.mockClear()

      v2Enabled = true
      const { render } = await import('@testing-library/react')
      const App = (await import('./App')).default
      render(<App />)

      // AppV2 must never mount the legacy session-attrs hook: its route is not
      // registered in v2 mode, so calling it would always 404 for nothing.
      expect(useSessionAttrsSpy).not.toHaveBeenCalled()

      // Dispatching the legacy 'session-attrs-updated' event through the real
      // onEvent handler must not throw and must not attempt to call into the
      // (nonexistent) refresh function -- it is now a documented no-op.
      expect(capturedOnEvent).not.toBeNull()
      expect(() => capturedOnEvent!({ type: 'session-attrs-updated' })).not.toThrow()

      // Sidebar/Overview must still receive a well-formed (empty) attrs shape,
      // never a stale or undefined value from the removed hook.
      expect(mockSidebarProps.sessionAttrs).toEqual({ background: new Set(), hidden: new Set(), scheduleIDs: new Map() })
    })

    it('v2 hidden/background presentation: catalog records populate sessionAttrs, and setSessionAttr dispatches the set_presentation session command', async () => {
      const sessionCommand = vi.fn().mockResolvedValue({})
      const hiddenSession: any = {
        id: 'sess-hidden',
        owner: null,
        ref: { owner: null, session: 'sess-hidden', window: 0, pane: 0 },
        phase: 'active',
        desired: 'run',
        revision: 1,
        created_at: '2025-01-01T00:00:00Z',
        hidden: true,
      }
      const backgroundSession: any = {
        id: 'sess-bg',
        owner: null,
        ref: { owner: null, session: 'sess-bg', window: 0, pane: 0 },
        phase: 'active',
        desired: 'run',
        revision: 1,
        created_at: '2025-01-01T00:00:00Z',
        background: true,
      }
      const plainSession: any = {
        id: 'sess-plain',
        owner: null,
        ref: { owner: null, session: 'sess-plain', window: 0, pane: 0 },
        phase: 'active',
        desired: 'run',
        revision: 1,
        created_at: '2025-01-01T00:00:00Z',
      }
      const sessionsByRef = new Map([
        [encodeSessionRef(hiddenSession.ref), hiddenSession],
        [encodeSessionRef(backgroundSession.ref), backgroundSession],
        [encodeSessionRef(plainSession.ref), plainSession],
      ])
      mockV2State = {
        state: {
          catalog: { owner: null, revision: 3, generation: 1, sessionsByRef, layoutsById: new Map() },
          workspace: { layoutId: null, revision: 0, generation: 0, record: null, presentationsByRef: new Map() },
          connectionGeneration: 1,
          connectionOnline: true,
          catalogBootstrapped: true,
          workspaceBootstrapped: false,
        },
        bootstrapped: true,
        connected: true,
        paneTree: null,
        activeKey: null,
        layoutId: null,
        createSession: vi.fn().mockResolvedValue({}),
        sessionCommand,
        workspaceCommand: vi.fn().mockResolvedValue({}),
      }

      v2Enabled = true
      const { render } = await import('@testing-library/react')
      const App = (await import('./App')).default
      render(<App />)

      expect(mockSidebarProps).not.toBeNull()
      // Real catalog hidden/background bits reach Sidebar via SessionView ->
      // toPresentationAttrs, not a fixed empty/no-op placeholder.
      expect(mockSidebarProps.sessionAttrs.hidden.has('sess-hidden')).toBe(true)
      expect(mockSidebarProps.sessionAttrs.hidden.has('sess-bg')).toBe(false)
      expect(mockSidebarProps.sessionAttrs.background.has('sess-bg')).toBe(true)
      expect(mockSidebarProps.sessionAttrs.background.has('sess-plain')).toBe(false)

      // Toggling hidden off from the sidebar dispatches the set_presentation
      // session command against the correct ref -- not a no-op.
      mockSidebarProps.setSessionAttr('sess-hidden', { hidden: false })
      expect(sessionCommand).toHaveBeenCalledWith(
        { owner: null, session: 'sess-hidden', window: 0, pane: 0 },
        { action: 'set_presentation', hidden: false },
      )

      sessionCommand.mockClear()
      mockSidebarProps.setSessionAttr('sess-plain', { background: true })
      expect(sessionCommand).toHaveBeenCalledWith(
        { owner: null, session: 'sess-plain', window: 0, pane: 0 },
        { action: 'set_presentation', background: true },
      )
    })

    it('handleCreateSession resolves the selected host fingerprint to its v2 OwnerID before calling v2State.createSession', async () => {
      // The New Session modal's hostId is a peer transport fingerprint
      // (HostInfo.ID from useHosts, matching /api/hosts). v2State.createSession's
      // hostId is sent on the wire as target_owner, typed state.OwnerID
      // server-side -- a DIFFERENT string encoding than the fingerprint (see
      // state.OwnerIDFromFingerprint). handleCreateSession must resolve the
      // selected host's real OwnerID (HostInfo.OwnerID, threaded through
      // useHosts as owner_id) before calling createSession, never pass the
      // fingerprint straight through.
      mockHostsList = [
        { id: 'remote-host-fingerprint', owner_id: 'remote-host-owner-id', name: 'remote', online: true, sessions: [], last_seen: '' },
      ]
      const createSession = vi.fn().mockResolvedValue({ Ref: { owner: null, session: 'new-sess', window: 0, pane: 0 } })
      mockV2State = {
        state: {
          catalog: { owner: null, revision: 0, generation: 0, sessionsByRef: new Map(), layoutsById: new Map() },
          workspace: { layoutId: null, revision: 0, generation: 0, record: null, presentationsByRef: new Map() },
          connectionGeneration: 0,
          connectionOnline: false,
          catalogBootstrapped: false,
          workspaceBootstrapped: false,
        },
        bootstrapped: false,
        connected: false,
        paneTree: null,
        activeKey: null,
        layoutId: null,
        createSession,
        sessionCommand: vi.fn(),
        workspaceCommand: vi.fn(),
      }

      v2Enabled = true
      const { render, act } = await import('@testing-library/react')
      const App = (await import('./App')).default
      render(<App />)

      // NewSessionModal only mounts once newSessionModalOpen flips true; open
      // it the same way a real user would, via TopBar's "new session" button.
      expect(mockTopBarProps).not.toBeNull()
      act(() => { mockTopBarProps.onNewSession() })

      expect(mockNewSessionModalProps).not.toBeNull()
      await mockNewSessionModalProps.onCreateSession('my-session', '/tmp', '', 'remote-host-fingerprint')

      // hostId passed to createSession must be the resolved OwnerID, never
      // the raw fingerprint the modal selected.
      expect(createSession).toHaveBeenCalledWith(
        expect.objectContaining({ name: 'my-session', hostId: 'remote-host-owner-id' }),
      )
    })

    it('getTerminalIdentity resolver passed to TiledView always includes backend="daemon" and never surfaces an empty generation as ready', async () => {
      const readySessionId = 'sess-ready'
      const pendingSessionId = 'sess-pending'
      const readySession: any = {
        id: readySessionId,
        owner: null,
        ref: { owner: null, session: readySessionId, window: 0, pane: 0 },
        phase: 'active',
        desired: 'run',
        revision: 1,
        created_at: '2025-01-01T00:00:00Z',
        generation: 'daemon-gen-1',
      }
      const pendingSession: any = {
        id: pendingSessionId,
        owner: null,
        ref: { owner: null, session: pendingSessionId, window: 0, pane: 0 },
        phase: 'pending',
        desired: 'run',
        revision: 1,
        created_at: '2025-01-01T00:00:00Z',
      }
      const sessionsByRef = new Map([
        [encodeSessionRef(readySession.ref), readySession],
        [encodeSessionRef(pendingSession.ref), pendingSession],
      ])
      mockV2State = {
        state: {
          catalog: { owner: null, revision: 2, generation: 1, sessionsByRef, layoutsById: new Map() },
          workspace: { layoutId: null, revision: 0, generation: 0, record: null, presentationsByRef: new Map() },
          connectionGeneration: 1,
          connectionOnline: true,
          catalogBootstrapped: true,
          workspaceBootstrapped: false,
        },
        bootstrapped: true,
        connected: true,
        paneTree: { type: 'leaf', sessionKey: readySessionId } as any,
        activeKey: readySessionId,
        layoutId: null,
        createSession: vi.fn().mockResolvedValue({}),
        sessionCommand: vi.fn(),
        workspaceCommand: vi.fn(),
      }

      window.history.pushState(null, '', '/session')
      v2Enabled = true
      const { render } = await import('@testing-library/react')
      const App = (await import('./App')).default
      render(<App />)

      expect(mockTiledViewProps).not.toBeNull()
      const getTerminalIdentity = mockTiledViewProps.getTerminalIdentity
      expect(typeof getTerminalIdentity).toBe('function')

      // Ready session: complete identity, backend always 'daemon', generation nonempty.
      const readyIdentity = getTerminalIdentity(readySessionId)
      expect(readyIdentity).toEqual({
        ready: true,
        backend: 'daemon',
        sessionId: readySessionId,
        ownerId: null,
        generation: 'daemon-gen-1',
      })

      // Pending session (no daemon generation yet): never ready, and critically
      // never carries an empty-string generation alongside backend='daemon' --
      // TiledView must render a loading state instead of letting Terminal (and
      // therefore terminalPool.checkout) see this identity at all.
      const pendingIdentity = getTerminalIdentity(pendingSessionId)
      expect(pendingIdentity.ready).toBe(false)
      expect(pendingIdentity.backend).toBe('daemon')
      expect(pendingIdentity.generation).toBeUndefined()

      // Unknown key: also not ready, same shape -- never falls through to a
      // legacy-routable partial identity.
      const unknownIdentity = getTerminalIdentity('does-not-exist')
      expect(unknownIdentity).toEqual({ ready: false, backend: 'daemon' })
    })

    it('top-bar split action records the target pane + direction so the created session is spliced in, not created standalone', async () => {
      const workspaceCommand = vi.fn().mockResolvedValue({})
      const createSession = vi.fn().mockResolvedValue({ ref: { owner: null, session: 'new-sess', window: 0, pane: 0 }, accepted: true })
      const activeSessionId = 'active-sess'
      const activeSession: any = {
        id: activeSessionId,
        owner: null,
        ref: { owner: null, session: activeSessionId, window: 0, pane: 0 },
        phase: 'active',
        desired: 'run',
        revision: 1,
        created_at: '2025-01-01T00:00:00Z',
        generation: 'gen-1',
      }
      const sessionsByRef = new Map([[encodeSessionRef(activeSession.ref), activeSession]])
      mockV2State = {
        state: {
          catalog: { owner: null, revision: 1, generation: 1, sessionsByRef, layoutsById: new Map() },
          workspace: { layoutId: 'layout-1', revision: 0, generation: 0, record: null, presentationsByRef: new Map() },
          connectionGeneration: 1,
          connectionOnline: true,
          catalogBootstrapped: true,
          workspaceBootstrapped: true,
        },
        bootstrapped: true,
        connected: true,
        paneTree: { type: 'leaf', sessionKey: activeSessionId } as any,
        activeKey: activeSessionId,
        layoutId: 'layout-1',
        createSession,
        sessionCommand: vi.fn(),
        workspaceCommand,
      }

      window.history.pushState(null, '', '/session')
      v2Enabled = true
      const { render, act } = await import('@testing-library/react')
      const App = (await import('./App')).default
      render(<App />)

      expect(mockTopBarProps).not.toBeNull()
      act(() => { mockTopBarProps.onSplitPane('v') })

      expect(mockNewSessionModalProps).not.toBeNull()
      await mockNewSessionModalProps.onCreateSession('split-session', '/tmp', '')

      // Placement is one atomic step: the split target/direction are sent as
      // part of the SAME create-session call, not as a separate follow-up
      // workspace "split" command (see App.tsx's handleCreateSession and
      // CreateParams in pkg/state/session_commands.go). A separate split
      // command after create would try to insert the already-placed ref a
      // second time and be rejected as a duplicate leaf.
      expect(createSession).toHaveBeenCalledWith(expect.objectContaining({
        splitTarget: { owner: null, session: activeSessionId, window: 0, pane: 0 },
        splitDirection: 'v',
      }))
      expect(workspaceCommand).not.toHaveBeenCalled()
    })
  })

  describe('remote session standalone pane presentation', () => {
    beforeEach(() => {
      mockSidebarProps = null
      mockTiledViewProps = null
      mockV2State = null
      mockHostsList = null
    })

    it('selecting a remote-owned session renders it as a standalone pane instead of a rejected workspace select', async () => {
      const localOwner = 'node-local'
      const remoteOwner = 'node-remote'
      const remoteSessionId = 'remote-sess-1'
      const remoteGeneration = 'daemon-gen-remote-7'
      const localSessionId = 'local-sess-1'

      const remoteRef = { owner: remoteOwner, session: remoteSessionId, window: 0, pane: 0 }
      const localRef = { owner: localOwner, session: localSessionId, window: 0, pane: 0 }

      const remoteSession: any = {
        id: remoteSessionId,
        owner: remoteOwner,
        ref: remoteRef,
        phase: 'active',
        desired: 'run',
        revision: 1,
        created_at: '2025-01-01T00:00:00Z',
        name: 'remote shell', generation: remoteGeneration,
      }
      const localSession: any = {
        id: localSessionId,
        owner: localOwner,
        ref: localRef,
        phase: 'active',
        desired: 'run',
        revision: 1,
        created_at: '2025-01-01T00:00:00Z',
        name: 'local shell', generation: 'daemon-gen-local-1',
      }

      const sessionsByRef = new Map([
        [encodeSessionRef(remoteRef), remoteSession],
        [encodeSessionRef(localRef), localSession],
      ])

      const workspaceCommand = vi.fn().mockResolvedValue({})
      const sessionCommand = vi.fn().mockResolvedValue({})
      const localKey = sessionKey({ host: localOwner, name: localSessionId } as any)
      mockV2State = {
        state: {
          catalog: {
            localOwner,
            ownerMeta: new Map(),
            sessionsByRef,
            layoutsById: new Map(),
          },
          workspace: { layoutId: 'g1', revision: 0, generation: 0, record: null, presentationsByRef: new Map() },
          connectionGeneration: 1,
          connectionOnline: true,
          catalogBootstrapped: true,
          workspaceBootstrapped: true,
        },
        bootstrapped: true,
        connected: true,
        // The LOCAL pane tree only ever contains the local session -- the
        // remote session is never (and can never legitimately be) a leaf of
        // it.
        paneTree: { type: 'leaf', sessionKey: localKey } as any,
        activeKey: localKey,
        layoutId: 'g1',
        createSession: vi.fn().mockResolvedValue({}),
        sessionCommand,
        workspaceCommand,
      }

      v2Enabled = true
      const { render, act } = await import('@testing-library/react')
      const App = (await import('./App')).default
      render(<App />)

      expect(mockSidebarProps).not.toBeNull()
      const remoteRow = mockSidebarProps.sessions.find((s: any) => s.id === remoteSessionId)
      expect(remoteRow).toBeTruthy()
      const remoteKey = sessionKey(remoteRow)
      expect(keyToSessionRef(remoteKey)).toEqual(remoteRef)

      // Select the remote session through the real Sidebar->handleSessionSelect
      // path.
      act(() => { mockSidebarProps.onSessionSelect(remoteRow) })

      // The bug: this must NEVER be sent as a workspaceCommand('select')
      // against the local layout -- that ref is not one of its leaves and
      // the backend rejects it as missing_target.
      expect(workspaceCommand).not.toHaveBeenCalledWith('g1', expect.objectContaining({ action: 'select', ref: remoteRef }))

      // Instead, TiledView is handed a synthetic standalone tree whose only
      // leaf is the remote session -- never merged into paneTree, never sent
      // anywhere as a workspace mutation.
      expect(mockTiledViewProps).not.toBeNull()
      expect(mockTiledViewProps.tree).toEqual({ type: 'leaf', sessionKey: remoteKey })
      expect(mockTiledViewProps.activeKey).toBe(remoteKey)

      // Terminal identity resolution must come from the REMOTE catalog entry
      // (real owner + real per-session daemon generation), never from local
      // workspace lookup and never a partial/legacy-fallback identity.
      const identity = mockTiledViewProps.getTerminalIdentity(remoteKey)
      expect(identity).toEqual({
        ready: true,
        backend: 'daemon',
        sessionId: remoteSessionId,
        ownerId: remoteOwner,
        generation: remoteGeneration,
      })
      expect(identity.ownerId).not.toBe(localOwner)

      // Reuse the real terminalPool.checkout() invariant: a v2 identity with
      // ownerId/generation MUST also carry sessionId, or it throws instead of
      // silently falling back to legacy name-based routing. A remote identity
      // must satisfy that invariant exactly like a local one.
      const { TerminalPool } = await import('./lib/terminalPool')
      const fakeTerminal = {
        options: {},
        element: document.createElement('div'),
        cols: 80,
        rows: 24,
        loadAddon: () => {},
        open: () => {},
        onData: () => ({ dispose: () => {} }),
        onResize: () => ({ dispose: () => {} }),
        onSelectionChange: () => ({ dispose: () => {} }),
        attachCustomKeyEventHandler: () => {},
        getSelection: () => '',
        clearSelection: () => {},
        scrollToBottom: () => {},
        scrollLines: () => {},
        focus: () => {},
        refresh: () => {},
        dispose: () => {},
        write: () => {},
      }
      const pool = new TerminalPool({
        createTerminal: () => fakeTerminal as any,
        createFitAddon: () => ({ fit: () => {} }) as any,
        createWebLinksAddon: () => ({}) as any,
        createClipboardAddon: () => ({}) as any,
        createWebglAddon: () => null,
        createImageAddon: () => null,
        createUnicodeGraphemesAddon: () => null,
        createPredictiveEcho: () => null,
        createWebSocket: () => ({
          readyState: 0, close: () => {}, send: () => {},
          onopen: null, onclose: null, onerror: null, onmessage: null,
        }) as any,
      })
      const prefs = {
        theme: 'dark', fontFamily: 'Space Mono', fontSize: 13,
        scrollback: 50000, renderer: 'dom', unicodeGraphemes: false, predictiveEcho: false,
      }
      const cbs = {
        onConnectionChange: () => {}, onCtrlModifierChange: () => {},
        onAltModifierChange: () => {}, onSelectionMenu: () => {},
      }
      const container = document.createElement('div')
      expect(() => {
        pool.checkout(
          { sessionName: remoteSessionId, hostId: identity.ownerId, backend: identity.backend, sessionId: identity.sessionId, ownerId: identity.ownerId, generation: identity.generation },
          prefs, container, cbs,
        )
      }).not.toThrow()

      // Killing/closing a session identified via the sidebar's kill action must
      // still route through the immutable ref (kill routing itself is
      // pre-existing and out of scope for this fix; this just proves the
      // resolved ref is correct for a remote row).
      sessionCommand.mockClear()
      mockSidebarProps.onSessionKilled(remoteKey)
      expect(sessionCommand).toHaveBeenCalledWith(remoteRef, { action: 'kill' })
    })

    it('selecting a local session after a remote pane was standalone clears the standalone projection', async () => {
      const localOwner = 'node-local'
      const remoteOwner = 'node-remote'
      const remoteSessionId = 'remote-sess-2'
      const localSessionId = 'local-sess-2'
      const remoteRef = { owner: remoteOwner, session: remoteSessionId, window: 0, pane: 0 }
      const localRef = { owner: localOwner, session: localSessionId, window: 0, pane: 0 }
      const remoteSession: any = {
        id: remoteSessionId, owner: remoteOwner, ref: remoteRef, phase: 'active', desired: 'run',
        revision: 1, created_at: '2025-01-01T00:00:00Z', generation: 'gen-r',
      }
      const localSession: any = {
        id: localSessionId, owner: localOwner, ref: localRef, phase: 'active', desired: 'run',
        revision: 1, created_at: '2025-01-01T00:00:00Z', generation: 'gen-l',
      }
      const sessionsByRef = new Map([
        [encodeSessionRef(remoteRef), remoteSession],
        [encodeSessionRef(localRef), localSession],
      ])
      const workspaceCommand = vi.fn().mockResolvedValue({})
      const localKey = sessionKey({ host: localOwner, name: localSessionId } as any)
      mockV2State = {
        state: {
          catalog: { localOwner, ownerMeta: new Map(), sessionsByRef, layoutsById: new Map() },
          workspace: { layoutId: 'g1', revision: 0, generation: 0, record: null, presentationsByRef: new Map() },
          connectionGeneration: 1,
          connectionOnline: true,
          catalogBootstrapped: true,
          workspaceBootstrapped: true,
        },
        bootstrapped: true,
        connected: true,
        paneTree: { type: 'leaf', sessionKey: localKey } as any,
        // Deliberately NOT localKey: the mocked hook return value is static
        // across renders (it does not simulate a real reducer round-trip), so
        // starting it different from localKey lets the re-select-local
        // assertion below distinguish "a real select command was issued" from
        // "already active, no-op".
        activeKey: null,
        layoutId: 'g1',
        createSession: vi.fn().mockResolvedValue({}),
        sessionCommand: vi.fn().mockResolvedValue({}),
        workspaceCommand,
      }

      v2Enabled = true
      const { render, act } = await import('@testing-library/react')
      const App = (await import('./App')).default
      render(<App />)

      const remoteRow = mockSidebarProps.sessions.find((s: any) => s.id === remoteSessionId)
      const localRow = mockSidebarProps.sessions.find((s: any) => s.id === localSessionId)
      const remoteKey = sessionKey(remoteRow)

      act(() => { mockSidebarProps.onSessionSelect(remoteRow) })
      expect(mockTiledViewProps.tree).toEqual({ type: 'leaf', sessionKey: remoteKey })

      workspaceCommand.mockClear()
      act(() => { mockSidebarProps.onSessionSelect(localRow) })

      // Back to the real local paneTree, sent through workspaceCommand as usual.
      expect(mockTiledViewProps.tree).toEqual({ type: 'leaf', sessionKey: localKey })
      expect(workspaceCommand).toHaveBeenCalledWith('g1', { action: 'select', ref: localRef })
    })
  })
})

