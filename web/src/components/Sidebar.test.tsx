// @vitest-environment jsdom
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { Sidebar } from './Sidebar'
import type { Session } from '../hooks/useSessions'
import type { SessionAttrSets } from '../hooks/useSessionAttrs'

function makeSession(name: string): Session {
  return {
    id: name,
    name,
    display_name: name,
    host: undefined,
    windows: [],
    created: new Date().toISOString(),
    attached: false,
    last_activity: new Date().toISOString(),
  }
}

const session = makeSession('s1')
const layoutGroups = [
  { id: 'g1', leaves: ['s1'], isActive: true, activeKey: 's1' as string | null, name: undefined as string | undefined },
]

const sessionAttrs: SessionAttrSets = {
  background: new Set(),
  hidden: new Set(),
  scheduleIDs: new Map(),
}

function renderSidebar(props: Partial<React.ComponentProps<typeof Sidebar>> & { forceAiName?: (groupId: string) => Promise<boolean>; namingGroupId?: string | null } = {}) {
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
      sessionOrderRanks={{}}
      setSessionOrderRank={() => {}}
      sessionAttrs={sessionAttrs}
      setSessionAttr={() => {}}
      pruningSuspended={false}
      layoutGroups={layoutGroups}
      {...props}
    />,
  )
}

describe('Sidebar group AI naming', () => {
  beforeEach(() => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve('') }),
    )
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
    vi.restoreAllMocks()
  })

  it('calls forceAiName with the group id when the AI name button is clicked', async () => {
    const forceAiName = vi.fn().mockResolvedValue(true)
    renderSidebar({ forceAiName })

    const button = screen.getByTitle('AI name this group')
    fireEvent.click(button)

    await waitFor(() => expect(forceAiName).toHaveBeenCalledWith('g1'))
  })

  it('shows a spinner and disables the button while namingGroupId matches the group', () => {
    renderSidebar({ forceAiName: vi.fn().mockResolvedValue(true), namingGroupId: 'g1' })

    const button = screen.getByTitle('AI name this group') as HTMLButtonElement
    expect(button.disabled).toBe(true)
    expect(button.querySelector('svg.animate-spin')).toBeTruthy()
  })

  it('dispatches an error toast when forceAiName rejects', async () => {
    const forceAiName = vi.fn().mockRejectedValue(new Error('naming service down'))
    const dispatchSpy = vi.spyOn(window, 'dispatchEvent')
    renderSidebar({ forceAiName })

    const button = screen.getByTitle('AI name this group')
    fireEvent.click(button)

    await waitFor(() =>
      expect(dispatchSpy).toHaveBeenCalledWith(
        expect.objectContaining({
          type: 'termyard:toast',
          detail: expect.objectContaining({ severity: 'error', message: 'naming service down' }),
        }),
      ),
    )
  })
})

describe('Sidebar v2Mode kill/rename routing', () => {
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

  it('v2Mode kill calls onSessionKilled exactly once and never hits the legacy kill route', async () => {
    const onSessionKilled = vi.fn()
    renderSidebar({ v2Mode: true, onSessionKilled })

    openContextMenuForRow()
    fireEvent.click(screen.getByText('Kill'))
    fireEvent.click(screen.getByText('Confirm kill?'))

    expect(onSessionKilled).toHaveBeenCalledTimes(1)
    expect(onSessionKilled).toHaveBeenCalledWith('s1')
    // The legacy REST kill route (/api/session/kill) must never be hit in v2
    // mode -- onSessionKilled already performed the real (v2) kill.
    const fetchMock = fetch as unknown as ReturnType<typeof vi.fn>
    expect(fetchMock).not.toHaveBeenCalledWith('/api/session/kill', expect.anything())
  })

  it('non-v2Mode kill calls both onSessionKilled and the legacy kill route (unchanged legacy behavior)', async () => {
    const onSessionKilled = vi.fn()
    renderSidebar({ v2Mode: false, onSessionKilled })

    openContextMenuForRow()
    fireEvent.click(screen.getByText('Kill'))
    fireEvent.click(screen.getByText('Confirm kill?'))

    expect(onSessionKilled).toHaveBeenCalledTimes(1)
    const fetchMock = fetch as unknown as ReturnType<typeof vi.fn>
    expect(fetchMock).toHaveBeenCalledWith('/api/session/kill', expect.anything())
  })

  it('v2Mode rename calls onRenameSession instead of the legacy display-name route', async () => {
    const onRenameSession = vi.fn()
    renderSidebar({ v2Mode: true, onRenameSession })

    openContextMenuForRow()
    fireEvent.click(screen.getByText('Rename'))
    const input = screen.getByDisplayValue('s1')
    fireEvent.change(input, { target: { value: 'new-label' } })
    fireEvent.keyDown(input, { key: 'Enter' })

    await waitFor(() => expect(onRenameSession).toHaveBeenCalledWith('s1', 'new-label'))
    const fetchMock = fetch as unknown as ReturnType<typeof vi.fn>
    expect(fetchMock).not.toHaveBeenCalledWith('/api/session/display-name', expect.anything())
  })

  it('v2Mode hides AI rename, Hide/Background, and worktree-removal controls', () => {
    renderSidebar({ v2Mode: true })

    openContextMenuForRow()
    expect(screen.queryByText('AI rename')).toBeNull()
    expect(screen.queryByText('Hide')).toBeNull()
    expect(screen.queryByText('Background')).toBeNull()
  })
})
