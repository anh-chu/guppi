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
      isSessionInActiveTurn={() => false}
      isSessionRecentlyDone={() => false}
      getSessionActivity={() => undefined}
      sessionOrderRanks={{}}
      setSessionOrderRank={() => {}}
      sessionAttrs={sessionAttrs}
      setSessionAttr={() => {}}
      backgroundSession={async () => {}}
      filterProtectionActive={false}
      layoutGroups={layoutGroups}
      {...props}
    />,
  )
}

describe('Sidebar hover expansion', () => {
  beforeEach(() => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve('') }),
    )
    Object.defineProperty(window, 'matchMedia', {
      writable: true,
      value: vi.fn().mockImplementation((query: string) => ({
        matches: /hover: hover|pointer: fine/.test(query),
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

  it('temporarily expands the narrow sidebar while hovered', async () => {
    const onToggleCollapse = vi.fn()
    const { container } = renderSidebar({ collapsed: true, onToggleCollapse })
    const aside = container.querySelector('aside') as HTMLElement

    expect(aside.className).toContain('w-11')
    expect(aside.parentElement?.className).toContain('max-w-11')
    expect(screen.queryByTitle('Collapse sidebar')).toBeNull()

    fireEvent.mouseEnter(aside)

    expect(aside.parentElement?.className).toContain('w-11')
    expect(aside.className).toContain('absolute')
    expect(aside.style.width).toBe('288px')
    expect(screen.getByTitle('Collapse sidebar')).toBeTruthy()
    expect(onToggleCollapse).not.toHaveBeenCalled()

    fireEvent.mouseLeave(aside)
    await waitFor(() => expect(aside.className).toContain('w-11'))

    expect(aside.parentElement?.className).toContain('w-11')
    expect(aside.className).toContain('w-11')
    expect(aside.className).not.toContain('absolute')
    expect(aside.style.width).toBe('')
    expect(screen.getByTitle('Expand sidebar')).toBeTruthy()
    expect(onToggleCollapse).not.toHaveBeenCalled()
  })

  it('keeps collapsed session initial fully visible inside the 44px rail', () => {
    const { container } = renderSidebar({ collapsed: true, selectedSession: 's1' })
    const outer = container.querySelector('div.w-11.max-w-11') as HTMLElement
    expect(outer).toBeTruthy()
    // Initial letter 'S' (from 's1') must appear in the collapsed rail
    expect(container.textContent).toContain('S')
    // No avatar circle, no tooltip
    expect(container.querySelector('.rounded-full.h-6.w-6')).toBeNull()
    expect(screen.queryByRole('tooltip')).toBeNull()
    const sessionRow = container.querySelector('[data-session-key="s1"] > div') as HTMLElement
    expect(sessionRow.className).not.toContain('border-white/20')
  })

  it('does not hover-expand hidden mode', () => {
    const { container } = renderSidebar({ collapsed: true, collapseMode: 'hidden', onToggleCollapse: vi.fn() })
    const aside = container.querySelector('aside') as HTMLElement

    fireEvent.mouseEnter(aside)

    expect(aside.className).toContain('w-0')
    expect(screen.getByTitle('Expand sidebar')).toBeTruthy()
    expect(screen.queryByTitle('Collapse sidebar')).toBeNull()
  })
})

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
