// @vitest-environment jsdom
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { Sidebar } from './Sidebar'
import type { Session } from '../lib/session'
import type { SessionPresentationAttrs } from '../state/session/viewModel'

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

const sessionAttrs: SessionPresentationAttrs = {
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

  it('hides AI rename but keeps Hide/Background controls (set_presentation is wired)', () => {
    renderSidebar({})

    openContextMenuForRow()
    expect(screen.queryByText('AI rename')).toBeNull()
    expect(screen.queryByText('Hide')).not.toBeNull()
    expect(screen.queryByText('Background')).not.toBeNull()
  })
})
