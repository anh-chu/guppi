// @vitest-environment jsdom
import { render, screen, fireEvent, cleanup } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { WikiPanel } from './WikiPanel'

// The panel never loads a real wiki-viewer in these tests; the mocked status
// marks it running so no poll retries are scheduled, and the mocked grant is
// unused because filePath is null (the panel roots at default_root instead).
const runningStatus = {
  installed: true,
  installing: false,
  running: true,
  version: '1.0.0',
  error: '',
  default_root: '/tmp',
}

function ok(body: unknown) {
  return { ok: true, json: async () => body, text: async () => '' }
}

class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}

beforeEach(() => {
  vi.stubGlobal(
    'fetch',
    vi.fn().mockResolvedValue(ok(runningStatus)),
  )
  vi.stubGlobal('ResizeObserver', ResizeObserverStub)
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockReturnValue({ width: 1000, height: 600 } as DOMRect)
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

function renderPanel() {
  return render(<WikiPanel filePath={null} onClose={vi.fn()} />)
}

describe('WikiPanel fullscreen', () => {
  it('toggles fullscreen via the desktop header button', () => {
    const { container } = renderPanel()
    const root = container.firstElementChild as HTMLElement

    // Not fullscreen on mount: the header shows "Enter fullscreen".
    expect(root.className).not.toContain('fixed inset-0 z-40')
    expect(screen.getByRole('button', { name: 'Enter fullscreen' })).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: 'Enter fullscreen' }))

    // Fullscreen now: the label flips and the root gets the fixed overlay class.
    expect(screen.getByRole('button', { name: 'Exit fullscreen' })).toBeTruthy()
    expect(root.className).toContain('fixed inset-0 z-40')

    fireEvent.click(screen.getByRole('button', { name: 'Exit fullscreen' }))

    expect(screen.getByRole('button', { name: 'Enter fullscreen' })).toBeTruthy()
    expect(root.className).not.toContain('fixed inset-0 z-40')
  })

  it('exits fullscreen when Escape is pressed', () => {
    const { container } = renderPanel()
    const root = container.firstElementChild as HTMLElement

    fireEvent.click(screen.getByRole('button', { name: 'Enter fullscreen' }))
    expect(root.className).toContain('fixed inset-0 z-40')

    fireEvent.keyDown(window, { key: 'Escape' })

    expect(root.className).not.toContain('fixed inset-0 z-40')
    expect(screen.getByRole('button', { name: 'Enter fullscreen' })).toBeTruthy()
  })

  it('stops resizing after the pointer is released', () => {
    const { container } = renderPanel()
    const root = container.firstElementChild as HTMLElement
    const divider = root.firstElementChild as HTMLElement
    Object.defineProperties(divider, {
      setPointerCapture: { value: vi.fn() },
      hasPointerCapture: { value: () => true },
      releasePointerCapture: { value: vi.fn() },
    })

    fireEvent.pointerDown(divider, { pointerId: 1, clientX: 100 })
    fireEvent.pointerMove(divider, { pointerId: 1, clientX: 0 })
    const widthAfterMove = root.style.width
    expect(widthAfterMove).toBe('580px')

    fireEvent.pointerUp(divider, { pointerId: 1 })
    fireEvent.pointerMove(divider, { pointerId: 1, clientX: -100 })

    expect(root.style.width).toBe(widthAfterMove)
  })
})
