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
