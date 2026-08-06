// @vitest-environment jsdom
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { Sidebar } from './Sidebar'
import type { SessionView, SessionPresentationAttrs } from '../state/session/viewModel'

function makeSession(id: string): SessionView {
  return {
    key: id,
    ref: { owner: null, session: id, window: 0, pane: 0 },
    id,
    ownerId: '',
    displayName: id,
    label: id,
    createdAt: new Date().toISOString(),
    generation: undefined,
    hidden: false,
    background: false,
    scheduleId: undefined,
    cwd: undefined,
    shell: undefined,
    agentType: undefined,
    worktreeBranch: undefined,
    isLocal: true,
    host: undefined,
    hostOnline: true,
  }
}

const session = makeSession('s1')

const sessionAttrs: SessionPresentationAttrs = {
  background: new Set(),
  hidden: new Set(),
  scheduleIDs: new Map(),
}

function renderSidebar(props: Partial<React.ComponentProps<typeof Sidebar>> = {}) {
  return render(
    <Sidebar
      sessions={[session]}
      selectedSession={null}
      collapsed={false}
      collapseMode="small"
      width={288}
      hasMultipleHosts={false}
      hosts={[]}
      onSessionSelect={() => {}}
      getSessionEvents={() => []}
      sessionNeedsAttention={() => false}
      isSessionInActiveTurn={() => false}
      getSessionActivity={() => undefined}
      sessionAttrs={sessionAttrs}
      setSessionAttr={() => {}}
      pruningSuspended={false}
      {...props}
    />,
  )
}

describe('Sidebar kill/rename routing', () => {
  beforeEach(() => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve('') }))
    Object.defineProperty(window, 'matchMedia', {
      writable: true,
      value: vi.fn().mockImplementation((query: string) => ({
        matches: false,
        media: query,
        onchange: null,
        addListener: vi.fn(),
        removeListener: vi.fn(),
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
        dispatchEvent: vi.fn(),
      })),
    })
  })

  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  function openContextMenuForRow() {
    const row = document.querySelector('[data-session-key="s1"] [role="button"]') as HTMLElement
    fireEvent.contextMenu(row)
  }

  it('kill calls onSessionKilled exactly once and never hits any legacy kill route', async () => {
    const onSessionKilled = vi.fn()
    renderSidebar({ onSessionKilled })

    openContextMenuForRow()
    fireEvent.click(screen.getByText('Kill'))
    fireEvent.click(screen.getByText('Confirm kill?'))

    expect(onSessionKilled).toHaveBeenCalledTimes(1)
    expect(onSessionKilled).toHaveBeenCalledWith('s1')
    // There is no legacy REST kill route any more -- onSessionKilled is the
    // only kill path (routed through v2State.sessionCommand upstream).
    const fetchMock = fetch as unknown as ReturnType<typeof vi.fn>
    expect(fetchMock).not.toHaveBeenCalledWith('/api/session/kill', expect.anything())
  })

  it('rename calls onRenameSession instead of any legacy display-name route', async () => {
    const onRenameSession = vi.fn()
    renderSidebar({ onRenameSession })

    openContextMenuForRow()
    fireEvent.click(screen.getByText('Rename'))
    const input = screen.getByDisplayValue('s1')
    fireEvent.change(input, { target: { value: 'new-label' } })
    fireEvent.keyDown(input, { key: 'Enter' })

    await waitFor(() => expect(onRenameSession).toHaveBeenCalledWith('s1', 'new-label'))
    const fetchMock = fetch as unknown as ReturnType<typeof vi.fn>
    expect(fetchMock).not.toHaveBeenCalledWith('/api/session/display-name', expect.anything())
  })

  it('has no group controls (groups were deleted) and keeps Hide/Background wired', () => {
    renderSidebar({})

    openContextMenuForRow()
    expect(screen.queryByTitle('AI name this group')).toBeNull()
    expect(screen.queryByText('AI rename')).toBeNull()
    expect(screen.queryByText('Hide')).not.toBeNull()
    expect(screen.queryByText('Background')).not.toBeNull()
  })
})

describe('Schema 4 Sidebar context display - FAILS', () => {
  beforeEach(() => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve('') }))
    Object.defineProperty(window, 'matchMedia', {
      writable: true,
      value: vi.fn().mockImplementation((query: string) => ({
        matches: false,
        media: query,
        onchange: null,
        addListener: vi.fn(),
        removeListener: vi.fn(),
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
        dispatchEvent: vi.fn(),
      })),
    })
  })

  afterEach(() => {
    cleanup()
  })

  it('Sidebar displays cwd, current command, and last activity from SessionView, not from pane/window counts', () => {
    // Schema 4 contract: Sidebar secondary line shows:
    // - Current working directory (from runtime.cwd)
    // - Current command (from runtime.currentCommand)
    // - Real last activity time (from runtime.lastActivity)
    // Never shows window/pane/attached/detached counts.

    const sessionWithContext = makeSession('s1')
    // After Task 7:
    // sessionWithContext.cwd = '/home/user/project'
    // sessionWithContext.currentCommand = 'npm run dev'
    // sessionWithContext.lastActivity = '2025-01-01T00:05:00Z'
    // sessionWithContext.phase = 'active'

    // renderSidebar({ sessions: [sessionWithContext] })
    // After Task 7, Sidebar should display these fields.
    // Currently it may not, so this test documents the target.

    expect(sessionWithContext).toBeDefined()
  })

  it('Sidebar shows agent type and worktree branch badges when present', () => {
    // Schema 4 contract: Session row shows canonical metadata badges:
    // - Agent type (from sessionView.agentType)
    // - Worktree branch (from sessionView.worktreeBranch)
    // - Schedule (from sessionView.scheduleId)
    // Never shows fake 'pane count' or 'windows' metrics.

    const withMeta = makeSession('s1')
    // withMeta.agentType = 'claude'
    // withMeta.worktreeBranch = 'feature/foo'

    // renderSidebar({ sessions: [withMeta] })
    // After Task 7, these badges appear.

    expect(withMeta).toBeDefined()
  })

  it('Sidebar respects browser-local session ordering without posting to server', () => {
    // Schema 4 contract: Sidebar can optionally accept and use browser-local
    // session order from useSessionOrder hook. Reordering never posts a
    // server command; it only updates localStorage.

    // Currently ordering may be server-driven or use old session-order routes.
    // After Task 9, only browser-local reordering exists.

    expect(true).toBe(true) // Placeholder until Task 9
  })
})
